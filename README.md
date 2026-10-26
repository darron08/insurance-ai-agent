# 通用保险 AI Agent

当前完成 M4：通过 Go 命令将本地文本型 PDF 解析、切分、幂等写入 PostgreSQL，再用 Eino 调用 Embedding API，批量保存向量并建立 pgvector HNSW 索引。

## 环境与运行

开发和验收固定使用 Go 1.27.3、Python 3、`pdfplumber 0.11.9` 和 Docker Compose。M4 默认连接本机运行的 `Qwen/Qwen3-Embedding-0.6B`，回环地址不需要 API Key；远程兼容服务仍需通过 `EMBEDDING_API_KEY` 提供密钥。`go.mod` 固定工具链版本，已有 Go 安装且允许自动工具链下载时，项目命令会自动使用 Go 1.27.3；可通过 `go version` 确认。首次运行前安装锁定的 Python 依赖：

```bash
python3 -m pip install -r requirements.txt
```

在项目根目录执行：

```bash
go run ./cmd/ingest \
  --file ./testdata/sample.pdf \
  --product-id demo \
  --document-name "自建解析测试资料" \
  --output ./var/imports
```

`sample.pdf` 是项目自行生成的三页中英文测试资料，不是真实保险产品，不包含可用于保险解释的真实条款。来源 URL 是示例标记，不会被下载；导入始终读取 `--file` 指定的本地文件。

查看终端输出的 JSON 路径，即可检查元数据、物理页码和正文。`var/` 中的生成结果已加入 Git 忽略规则。

将上一步输出路径传给 M2 切分命令：

```bash
go run ./cmd/chunk \
  --input ./var/imports/<导入命令输出的文件>.json \
  --output ./var/chunks
```

切分命令读取已经通过 M1 质量门槛的 JSON，不会再次解析 PDF。当前刻意保留两条独立命令，便于分别观察解析和切分结果；后续里程碑的一键导入会直接在 Go 服务层传递 `Document`，而不是通过执行另一个 CLI 或重新解析 PDF 来串联。

```bash
go test ./...
go vet ./...
go build -o ./bin/ingest ./cmd/ingest
go build -o ./bin/chunk ./cmd/chunk
go build -o ./bin/migrate ./cmd/migrate
go build -o ./bin/store ./cmd/store
go build -o ./bin/index ./cmd/index
./bin/ingest --help
./bin/chunk --help
./bin/migrate --help
./bin/store --help
./bin/index --help
```

## 参数

| 参数 | 含义 | 要求 |
| --- | --- | --- |
| `--file` | 本地 PDF 路径 | 必填，普通文件且具有 PDF 文件头 |
| `--product-id` | 产品标识 | 必填 |
| `--document-name` | 文档名称 | 可选；未传时使用 PDF 文件名并去掉 `.pdf` 后缀 |
| `--source-url` | 文档来源地址 | 可选；未传时记录为 `local://文件名`，填写时需为 HTTP/HTTPS URL；只记录，不请求 |
| `--output` | 输出目录 | 默认 `./var/imports` |

可以用实际保险 PDF 替换文件路径；有公开来源时再填写真实 URL。项目附带两份已下载的平安公开条款及来源说明，见 [`testdata/real/SOURCES.md`](testdata/real/SOURCES.md)。完成后请对照原文抽查至少三页。

成功返回退出码 0；参数语法错误返回 2；输入、解析或输出失败返回 1。`go run` 本身会包装程序退出码；需要直接检查原始退出码时，请使用构建后的 `./bin/ingest`。

## 条款切分

`chunk` 命令参数：

| 参数 | 含义 | 要求 |
| --- | --- | --- |
| `--input` | M1 导入生成的 JSON | 必填，普通 `.json` 文件且内容通过 M1 质量校验 |
| `--output` | Chunk JSON 输出目录 | 默认 `./var/chunks` |

M2 使用固定的 `clause-aware-v1` 策略：识别 `1.1 合同构成` 和 `第一条 ...` 两类条款编号，并从条款前的短标题推断所属章节。目录当页的后续内容按普通文本保存，不作为正文条款；未识别为条款的阅读指引、前言和表格文本也不会丢弃。

常规条款尽量保持完整。单个页面内的条款文本超过 800 个 Unicode 字符时，依次优先在空行、换行和句末标点处分割，必要时才按字符边界分割；相邻子块最多重叠 100 字符。重叠不会跨越条款或物理页面。跨页续文继承条款与章节元数据，但每个 Chunk 始终只对应一个物理 PDF 页。

Chunk JSON 顶层保留源文档元数据、SHA-256、解析器、页数、切分策略和切分时间。每个 Chunk 包含：

- 从 1 开始的 `chunk_index`。
- 产品、文档名称和版本。
- `page_number`、`section_title` 和 `clause_id`。
- 同一条款的 `part_index`、`part_count`。
- `character_count` 和原文 `text`。

M2 本身不访问数据库，因此 Chunk JSON 不包含 `document_id`。M3 入库成功后由 PostgreSQL 生成文档 ID。

## PostgreSQL 持久化

使用 `pgvector/pgvector:0.8.7-pg17-bookworm` Docker 镜像。PostgreSQL 主版本为 17，`pgvector` 为 0.8.7；M3 登记文档与 Chunk，M4 通过新的迁移增加向量列和 HNSW 索引。

启动数据库并执行迁移：

```bash
docker compose up -d postgres
export DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent?sslmode=disable'
go run ./cmd/migrate --action status
go run ./cmd/migrate --action up
```

将 M2 生成的 Chunk JSON 写入 PostgreSQL：

```bash
go run ./cmd/store --input ./var/chunks/<切分命令输出的文件>.json
```

成功后输出 `document_id`、`inserted/skipped/replaced`、处理状态和 Chunk 数量。幂等键是 `product_id + version + source_sha256`：默认重复入库返回原文档 ID 且不改动数据；如需原子替换同一文档的 Chunk，显式执行：

```bash
go run ./cmd/store \
  --input ./var/chunks/<切分命令输出的文件>.json \
  --replace
```

可直接检查已入库数据：

```bash
docker compose exec postgres \
  psql -U insurance -d insurance_agent \
  -c 'SELECT id, product_id, version, processing_status, total_chunks FROM documents;'
```

迁移是显式操作，入库命令不会自动修改 schema。`down` 只用于本地学习回退，必须同时传入 `--action down --confirm-down`。`db/migrations/` 中的 SQL 是追加式历史；后续里程碑只能新增文件，不修改、重命名、删除或压缩已有迁移。

## Embedding 与向量索引

已有数据库需要先运行 `migrate --action up`。`00003` 会清除旧模型生成的 1536 维向量并将向量列升级为 1024 维；M3 文档、Chunk 正文和元数据保持不变。

先在本机启动提供 OpenAI 兼容 `/v1/embeddings` 接口的 Qwen 服务。例如 vLLM 使用 pooling runner：

```bash
vllm serve Qwen/Qwen3-Embedding-0.6B \
  --runner pooling \
  --host 127.0.0.1 \
  --port 8001
```

macOS 上也可以使用 Ollama。Ollama 的模型别名与 vLLM 默认模型名不同，因此索引时必须明确传入对应配置：

```bash
ollama pull qwen3-embedding:0.6b
export EMBEDDING_BASE_URL='http://127.0.0.1:11434/v1'
export EMBEDDING_MODEL='qwen3-embedding:0.6b'
```

vLLM 是独立的模型运行环境，不加入本项目的 PDF 解析依赖。服务就绪后，将 `store` 输出的文档 UUID 传给索引命令：

```bash
export EMBEDDING_BASE_URL='http://127.0.0.1:8001/v1'
export EMBEDDING_MODEL='Qwen/Qwen3-Embedding-0.6B'
go run ./cmd/index --document-id '<store 输出的 UUID>'
```

这两个环境变量可以省略，默认值如上。回环地址未设置 `EMBEDDING_API_KEY` 时，适配器仅在请求内使用 `local` 占位值；远程地址必须显式配置密钥。地址填写 API 基础路径（例如以 `/v1` 结尾），不含 `/embeddings`、凭据或查询参数。兼容服务必须支持数组输入、`dimensions: 1024`、浮点向量和每项 `index` 字段。`.env.example` 仅为配置示例，Go CLI 不会自动加载 `.env`，运行前需将所需变量导出到 shell。

| 参数 | 含义 | 默认值 |
| --- | --- | --- |
| `--document-id` | 已入库文档的 UUID | 必填 |
| `--batch-size` | 每批 Chunk 数，范围 1–2048 | 16 |
| `--reindex` | 清空旧向量并整份重建 | false |

输出包含文档 ID、`indexed/skipped`、状态、模型、总 Chunk 数、本次处理数和复用数。Embedding 只使用原始 Chunk 正文；Qwen 模型输出 1024 维向量，按余弦距离建立 HNSW 索引。Eino `v0.9.22`、OpenAI Embedding 适配器和 Sonic 的具体版本固定在 `go.mod/go.sum`，M4 尚不提供检索入口。

成功批次单独提交；某批失败时保留已有向量，记录 `failed`、失败阶段和安全错误类别，再次执行相同命令即可续跑。进程异常退出可能留下 `embedding` 状态，重跑也会继续处理缺失向量。仅全部 Chunk 保存成功才标记 `ready`；已完成文档再次运行返回 `skipped`，不请求模型。

单次请求超时 60 秒，临时网络错误及 HTTP 408/429/5xx 最多重试 3 次（共 4 次尝试）；退避可取消，`Retry-After` 最长等待 60 秒。认证、参数、无效响应不自动重试。按 Ctrl+C 会取消请求并尝试保存失败状态；若数据库断连，命令会明确报告状态保存失败，稍后重跑恢复。

同一文档同时索引或在索引期间执行 `store --replace` 会返回“处理中”。替换成功会删除旧向量、清空模型配置并回到 `chunked`。更换服务地址或模型需要明确重建：

```bash
go run ./cmd/index --document-id '<UUID>' --reindex
```

每份文档仅保留一套向量，重建期间不处于 `ready`，重建失败也不会恢复旧向量。修改维度需要后续新迁移。批大小按 Chunk 个数计算，不按 Token 自动拆分；服务输入或 Token 限制错误时，可减小批大小，单个 Chunk 超出模型限制时需要回到切分阶段调整，程序不会截断条款。

查询索引状态与完成数量：

```bash
docker compose exec postgres psql -U insurance -d insurance_agent -c \
  'SELECT d.id, d.processing_status, d.embedding_model, d.failed_stage, d.last_error, d.indexed_at, d.total_chunks, count(c.embedding) AS embedded_chunks FROM documents d LEFT JOIN document_chunks c ON c.document_id = d.id GROUP BY d.id;'
```

执行一次 `migrate --action down --confirm-down` 只回退最新迁移。第一次会清除 Qwen 向量并恢复 00002 的 1536 维空向量列；第二次删除 M4 向量列和索引元数据，同时保留 M3 文档与 Chunk 正文；第三次才会删除 M3 表。

## 测试

常规测试用本地模拟 HTTP 服务验证 Eino 请求、响应校验与重试，不使用真实模型密钥。PostgreSQL 集成测试会清空测试数据并回放迁移，只能使用独立测试库：

```bash
docker compose exec postgres createdb -U insurance insurance_agent_m4_test
export TEST_DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_m4_test?sslmode=disable'
go test ./...
go test -race ./...
go vet ./...
```

测试库名称必须包含 `test`；未设置 `TEST_DATABASE_URL` 时数据库测试会跳过。本地 Qwen 联调需单独启动模型、执行 `index` 并核对数据库向量数量，不能把模拟 HTTP 测试当成真实服务已验证。

## 输出与重复运行

每次成功导入产生一个独立 JSON 文件，包含：

- `metadata`：产品、名称、来源；`version` 字段当前留空，后续文档版本管理阶段再启用。
- `file_path`、`sha256`：绝对文件路径、文件内容的校验值。
- `total_pages`、`character_count`：总页数、提取并去掉页面首尾空白后的 Unicode 码点数。
- `pages`：`page_number` 从 1 开始，包括空白页；`text` 为页面文本。
- `quality`：质量门槛结果、替换字符数、清理的私用区字符数、文本页和空白页数量。
- `imported_at`、`parser`：UTC 导入时间、解析器及实际版本。

先写临时文件、关闭成功后才改名为 `.json`，避免将不完整输出作为成功结果。重复导入保留原输出，同一文件的校验值一致，输出文件名和导入时间可以不同。M3 在独立入库阶段生成 `document_id`并保证幂等；M1/M2 命令语义不变。

## 当前限制

正文由 `pdfplumber` 按 PDF 内容流恢复，从而避免双栏文字被逐行交错；复杂表格、旋转文字或本身缺少正确内容顺序的 PDF 仍需人工核验。页码是 PDF 的物理页面序号，可能不同于正文印刷的页码。

Go 负责校验、SHA-256、质量门槛和 JSON 落盘，内嵌 Python 脚本只调用 `pdfplumber` 逐页提取。出现 `U+FFFD` 替换字符或全文为空会拒绝导入，不会回退到旧解析器；空白页与非空页面混合时保留空白页。M1 不执行 OCR。`quality.status: passed` 表示自动门槛通过，不等同于对条款内容完成人工审校。

M2 的章节识别是确定性启发式，不会使用 LLM 猜测标题或修复条款文字。PDF 若缺少明确条款编号、标题排版异常或表格阅读顺序复杂，可能只得到无条款编号的普通文本 Chunk；这类结果仍需对照原 PDF 人工抽查。固定的 800/100 参数用于建立首个可比较基线，后续质量优化阶段再比较其他策略。

代码阅读、原理和动手实验见 [M1 学习说明](docs/learning/m1-pdf-ingest.md)、[M2 学习说明](docs/learning/m2-clause-chunking.md)、[M3 学习说明](docs/learning/m3-postgresql-persistence.md) 和 [M4 学习说明](docs/learning/m4-embedding-indexing.md)。
