# M5：产品内向量检索

M5 的输入是问题、一个产品和可选版本，输出是相关 Chunk 原文、相似度与来源。它复用 M4 向量，不调用生成模型。先阅读服务接口，再看数据库查询，最后用 CLI 对照两种检索模式。

## 调用链与边界

```mermaid
graph TD
    CLI["search CLI：参数与环境配置"] --> Service["retrieval.Service.Search"]
    Service --> Adapter["embedding.EmbedQuery：Qwen 问题指令"]
    Adapter --> Eino["Eino OpenAI Embedding"]
    Eino --> Validate["1024 维、非零、有限数值校验"]
    Validate --> Repository["PostgreSQL SearchRepository"]
    Repository --> Snapshot["10 秒只读可重复读事务"]
    Snapshot --> Scope["解析 ready 版本并核对完整模型配置"]
    Scope --> HNSW["HNSW 候选 Top K"]
    Scope --> Exact["物化符合条件的全部候选，再精确 Top K"]
    HNSW --> Result["原文、来源、物理页码与分数"]
    Exact --> Result
```

阅读顺序：

1. `internal/retrieval/retrieval.go`：`Request`、`QueryEmbedder`、`Repository` 和 `Service.Search`。应用服务依赖项目接口与 `context.Context`，保留后续 HTTP 复用入口。
2. `internal/embedding/eino.go`：`EmbedQuery` 给问题加指令后调用已有 `Embed`。SDK、响应校验、重试与脱敏都在适配器内。
3. `internal/postgres/retrieval.go`：版本解析、配置检查、快照边界及两种 SQL。所有用户输入绑定为 SQL 参数。
4. `cmd/search/main.go` 与对应测试：参数解析、显式空版本、JSON 输出、信号取消和依赖组装。

暂时复用 `indexing.Profile`、`indexing.ValidateVectors` 和数据库的 `vectorLiteral`，避免为了 M5 重构 M4。没有新增依赖或数据库迁移；现有产品/版本索引和 HNSW 索引已够用。

## 问题向量与模型空间

Qwen 的查询输入固定为：

```text
Instruct: Retrieve insurance policy clauses relevant to the user's question.
Query:{去掉首尾空白的问题}
```

查询模板标识为 `qwen3-insurance-v1`，写入输出；以后修改指令应同步更新标识并重新对比检索样本。文档侧仍只嵌入原始条款，不加问题指令，所以添加 M5 不会使已有文档向量失效。[Qwen 官方模型说明](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) 推荐查询指令与无指令的文档编码。

查询和文档必须在同一模型空间。项目用规范化基础地址、模型名、维度生成的配置标识判断兼容，密钥不参与标识。仓库还核对文档保存的配置字段，任何一个 ready 文档不一致都会拒绝所选版本的整次检索。相同的 1024 维并不证明模型兼容；Ollama 别名和 vLLM 默认名也不自动互认。

本阶段的查询格式按 Qwen 设计，使用其他模型前需要另行核对格式。模型调用复用 M4 的单次 60 秒超时、临时错误最多重试三次、可取消退避与错误脱敏；无效向量或取消不会进入检索 SQL。

## 产品、版本和可见数据

产品必填、精确匹配，不默认跨产品搜索。版本以该产品 `ready` 文档中的不同版本为准：

| 输入 | 行为 |
| --- | --- |
| 省略 `--version`，只有一个 ready 版本 | 自动选中该版本 |
| 省略 `--version`，有多个 ready 版本 | 返回候选版本，要求明确选择 |
| 显式 `--version=''` | 只选旧数据的空字符串版本 |
| 显式 `--version v1` | 只选 v1，不回退其他版本 |
| 产品或版本没有 ready 文档 | 返回错误，提示先完成索引 |
| 所选版本存在配置不兼容的 ready 文档 | 返回错误，提示使用索引配置或重建 |

`ingest --version v1` 将标签写入已有 metadata，`chunk` 原样传给每个 Chunk，`store` 沿用 `(product_id, version, source_sha256)` 幂等键。因此，同一 PDF 的不同版本标签可拥有不同文档记录；重复同一标签仍跳过。标签不自动排序，不意味着产品版本的业务比较；旧文件格式和空版本继续有效。导入拒绝版本首尾空白。

仓库在生成问题向量后才开启最长 10 秒的只读 `REPEATABLE READ` 事务。版本、状态、配置、原文、来源和向量都在同一快照中读取；不占用文档写锁，也不会把模型等待放进事务。并发替换或重建时，一个已建立快照的查询可以返回完整旧数据；新查询会看到新的状态。这个语义保证本次结果自洽，不保证返回后该版本仍处于 ready。

`chunked`、`embedding`、`failed` 文档一律不参与查询。Chunk 还需满足向量非空、1024 维、模型一致、已完成嵌入。若 ready 文档的数据被外部改坏而没有任何合格向量，返回 `hits: []`；检索不会修复数据或改变状态，可回到 M4 检查并显式重建。

## HNSW 与 exact 如何比较

余弦距离由 pgvector 的 `<=>` 计算，越小越接近；输出 `score = 1 - distance`，越大越接近。分数通常在 -1 到 1 之间，浮点计算可能产生极小误差；它不是概率或答案置信度。M5 不设相似度阈值，无关问题仍可能返回 Top K。证据是否足以回答由 M6 结合样本另行设计。

`hnsw` 的内层 SQL 将产品、版本、ready 状态与模型条件放在候选查询里，以原始距离表达式升序排序并取 K；外层对已选出的候选按距离、文档 ID、Chunk 序号排序。不要改成“全库先取 K，Go 再按产品过滤”，也不要用相似度表达式降序代替内层距离排序，否则可能失去可用的索引路径。

事务内使用 `SET LOCAL` 设置 `hnsw.iterative_scan = strict_order`、`hnsw.ef_search = 100` 和 `hnsw.max_scan_tuples = 20000`。事务结束自动恢复设置，避免污染连接池。迭代扫描改善过滤后的召回，但仍有访问数量与内存限制；不能把不足 K 条理解成没有更多匹配候选。并列距离的边界候选可能变化；外层稳定排序只保证返回集合内的顺序。[pgvector 官方说明](https://github.com/pgvector/pgvector#iterative-index-scans) 解释了过滤与迭代扫描的关系。

`exact` 先物化符合相同条件的所有向量，再计算距离、排序和 LIMIT，因此不使用 HNSW 做候选截断。并列距离按文档 ID、Chunk 序号排序，适合建立可重复对照基线。它不等于“语义答案一定正确”，只表示给定向量和候选范围内的数学 Top K。

生产查询允许优化器根据数据量和过滤选择精确扫描；输出 `mode: hnsw` 表示允许近似索引的请求策略，不保证一定走 HNSW。集成测试单独通过 `EXPLAIN` 和仅限测试事务的规划设置证明 HNSW 路径可用，并实际执行强过滤查询；exact 的执行计划不得使用 HNSW。没有自动降级策略，方便清楚比较两种模式。

## 运行和复盘

沿用索引时的环境配置，例如 macOS 的 Ollama：

```bash
export EMBEDDING_BASE_URL='http://127.0.0.1:11434/v1'
export EMBEDDING_MODEL='qwen3-embedding:0.6b'
export DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent?sslmode=disable'

go run ./cmd/search --product-id demo --query '保障范围包括什么？' --mode hnsw
go run ./cmd/search --product-id demo --query '保障范围包括什么？' --mode exact
```

stdout 是完整 JSON；错误和版本候选在 stderr。`hits` 保留完整原文、出处 URL、源文件 SHA-256、物理页码、文档版本、条款号与分片信息。不能将物理页码替换成印刷页码，不能把多个命中拼接成未经核对的条款。原文、字段和元数据仍继承 M1/M2 的解析与切分限制。

建议实验：

1. 分别导入空版本和 v1，比较省略 `--version` 与 `--version=''` 的结果；观察版本歧义而非自动“最新”。
2. 相同问题、相同配置下对比两种模式的 Chunk ID、页码、Top K 与距离，增加其他产品的更近向量来测试过滤。
3. 改模型名或地址，观察配置不匹配；保持原配置并更换 API Key，配置标识应保持一致。
4. 搜索一个条款没有涉及的问题，观察仍可能返回高分近邻；解释为什么相似度不能直接用于保险结论。

## 自动化验收

测试包括应用服务校验、查询模板、取消与非法向量、CLI JSON/退出码/空版本、版本录入和幂等、多产品多版本过滤、完整来源、已知距离与并列排序、错误状态与模型不兼容、真实快照、HNSW 计划、强过滤、连接设置恢复。

```bash
docker compose exec postgres createdb -U insurance insurance_agent_m5_test
export TEST_DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_m5_test?sslmode=disable'
go test ./...
go test -race ./...
go vet ./...
go build -o ./bin/ ./cmd/...
```

测试库名称必须含 `test`。集成测试会清空数据、回放原有迁移，不能指向演示库，也不要并发运行多组使用同一测试库的测试进程。模型单元测试只用本地模拟 HTTP，真实 Qwen 验收另行记录。

## 本次真实联调记录（2026-10-27）

使用本机 Ollama 的 `qwen3-embedding:0.6b`（模型 ID 前缀 `ac6da0dfba84`），OpenAI 兼容地址 `http://127.0.0.1:11434/v1`，固定 1024 维。独立库为 `insurance_agent_m5_smoke_test`，没有操作演示库。完整模型配置标识为 `6dacd43ad6c39043d5c8daa1aea3257361aff1cd59bec629c2455b39d9f572d0`。

两份离线保单分别经真实 `ingest → chunk → store → index` 服务链处理，安康医疗险生成 85 个 Chunk，旅行险历史手册生成 67 个，均为 ready。再次运行索引分别跳过并复用 85/67 个向量。旅行手册包含多份历史条款，本次作为一个资料产品演示检索；这些命中不能替代手册内部各合同的适用范围辨认。

预期证据在请求模型前从现有原文确定；脚本还会核对对应页码、条款号和原文片段确实存在。六个完整问题为：

1. 安康医疗保险的等待期是多久，等待期内确诊疾病导致的医疗费用是否赔付？
2. 收到安康医疗保险合同后有多少天犹豫期，期间解除合同能退还保费吗？
3. 安康医疗保险的保险期间多长，是否保证续保？
4. 旅行意外伤害保险中，意外事故后多少天内因事故身故可以获得身故保险金？
5. 旅行意外伤害保险对醉酒或者酒后驾驶期间遭受伤害如何规定？
6. 旅行期间行李物品或者旅行证件被盗后，需要向警方报案并提交什么证明？

下表的 Top 5 使用文档内的 `chunk_index`，便于对照原始 Chunk JSON；本次两种模式的五条结果及顺序完全相同。预期证据排名按页码、条款号和原文片段共同核对，不只比较条款号。

| 问题 | 预期证据：物理页 / 条款 / Chunk | 两种模式共同 Top 5 | 预期证据排名 / score | HNSW / exact 总耗时 ms |
| --- | --- | --- | --- | --- |
| 安康等待期 | 3 / 2.2 / 15 | 85, 15, 4, 1, 35 | 2 / 0.5697 | 1415 / 309 |
| 安康犹豫期 | 2 / 1.6 / 10 | 10, 1, 11, 84, 15 | 1 / 0.6627 | 288 / 518 |
| 安康续保 | 2 / 1.7 / 12 | 12, 4, 85, 1, 37 | 1 / 0.6239 | 185 / 138 |
| 旅行意外身故 | 1 / 第五条 / 6 | 6, 7, 26, 11, 53 | 1 / 0.7698 | 461 / 358 |
| 旅行酒后驾驶免责 | 2 / 第七条 / 11 | 11, 6, 1, 32, 7 | 1 / 0.7183 | 1268 / 390 |
| 旅行行李被盗 | 8 / 第五条 / 64 | 64, 65, 57, 61, 48 | 1 / 0.7846 | 1139 / 413 |

六个问题在两种模式下均命中预期 Top 5，Top 1 各为 5/6。等待期问题的首位实际是第 16 页的保险计划表，score 约 0.6000，比正确等待期条款的 0.5697 更高；说明相似度排序不能替代证据核查。计划表的 `clause_id` 仍继承 M2 的 `8.36` 元数据，这是既有切分局限，本阶段没有修改原文或切分逻辑。

耗时包括 Query Embedding、模型加载/缓存、数据库往返和读取，且两种模式依次执行；不能据此判断 HNSW 与 exact 的数据库性能。小数据集还可能由优化器选择精确扫描，HNSW 索引路径的可用性由另行强制路径的集成测试验证。六条样本只是联调冒烟结果，不构成通用召回率或回答正确率评测。

本机原始记录在忽略目录 `var/m5-smoke/`：`fixtures.json` 记录文档 ID 与 Chunk 路径，`cases.json` 记录问题与预期证据，`results.json` 保存两种模式的完整 Top 5、原文、分数、来源和耗时。`_runner/main.go` 是本次本地验收辅助程序，不进入版本控制，也不会被 `go test ./...` 发现。克隆仓库后可按[本地运行与开发指南](../getting-started.md)中的六个独立 CLI 复现同一数据链，无需这些本地产物。

Go 1.27.3 下的全量普通测试、竞态测试、vet 和六个 CLI 构建均通过；普通与竞态测试均包含真实独立数据库的 M3–M5 集成测试及两份真实 PDF 的解析、切分回归。另用构建后的 `bin/search` 执行真实 exact 查询，核对了最终 JSON、版本、模板/配置标识、原文及完整来源。
