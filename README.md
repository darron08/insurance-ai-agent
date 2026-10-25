# 通用保险 AI Agent

当前完成 M2：先通过 Go 命令将本地文本型 PDF 解析为逐页 JSON，再用独立命令按章节和条款切成可检查的 Chunk JSON。

## 环境与运行

需要 Go 1.22 或更高版本、Python 3 和 `pdfplumber 0.11.9`；不需要 API Key。首次运行前安装锁定的 Python 依赖：

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
./bin/ingest --help
./bin/chunk --help
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

M2 还没有数据库，因此不生成 `document_id`。每次成功切分产生独立结果文件；失败不会留下最终 JSON。

## 输出与重复运行

每次成功导入产生一个独立 JSON 文件，包含：

- `metadata`：产品、名称、来源；`version` 字段当前留空，后续文档版本管理阶段再启用。
- `file_path`、`sha256`：绝对文件路径、文件内容的校验值。
- `total_pages`、`character_count`：总页数、提取并去掉页面首尾空白后的 Unicode 码点数。
- `pages`：`page_number` 从 1 开始，包括空白页；`text` 为页面文本。
- `quality`：质量门槛结果、替换字符数、清理的私用区字符数、文本页和空白页数量。
- `imported_at`、`parser`：UTC 导入时间、解析器及实际版本。

先写临时文件、关闭成功后才改名为 `.json`，避免将不完整输出作为成功结果。重复导入保留原输出，同一文件的校验值一致，输出文件名和导入时间可以不同。M3 再加入基于数据库的查重和幂等状态；M1 没有 `document_id`、自动跳过或 `--reindex`。

## 当前限制

正文由 `pdfplumber` 按 PDF 内容流恢复，从而避免双栏文字被逐行交错；复杂表格、旋转文字或本身缺少正确内容顺序的 PDF 仍需人工核验。页码是 PDF 的物理页面序号，可能不同于正文印刷的页码。

Go 负责校验、SHA-256、质量门槛和 JSON 落盘，内嵌 Python 脚本只调用 `pdfplumber` 逐页提取。出现 `U+FFFD` 替换字符或全文为空会拒绝导入，不会回退到旧解析器；空白页与非空页面混合时保留空白页。M1 不执行 OCR。`quality.status: passed` 表示自动门槛通过，不等同于对条款内容完成人工审校。

M2 的章节识别是确定性启发式，不会使用 LLM 猜测标题或修复条款文字。PDF 若缺少明确条款编号、标题排版异常或表格阅读顺序复杂，可能只得到无条款编号的普通文本 Chunk；这类结果仍需对照原 PDF 人工抽查。固定的 800/100 参数用于建立首个可比较基线，后续质量优化阶段再比较其他策略。

代码阅读、原理和动手实验见 [M1 学习说明](docs/learning/m1-pdf-ingest.md) 和 [M2 学习说明](docs/learning/m2-clause-chunking.md)。
