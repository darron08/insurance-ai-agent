# M6：基于证据的保险问答

M6 接收一个产品中的独立问题，返回带原文引用的条款解释或明确拒答。它复用 M5 检索，不理解上一轮对话。默认生成服务为 DeepSeek Flash；Embedding 继续使用建立文档向量时的 Qwen 配置，两者不能混淆。

## 数据流与阅读顺序

```mermaid
flowchart TD
    HTTP["Gin：参数校验 / 120 秒取消上下文"] --> Run["rag.Service：创建 running 记录"]
    Run --> Search["M5：Query Embedding → 只读检索快照"]
    Search --> Context["完整 Chunk / E1… / 12000 字符预算"]
    Context --> Save["提交召回、上下文与实际消息快照"]
    Save --> Empty{"是否有上下文？"}
    Empty -->|否| Refuse["固定证据不足模板"]
    Empty -->|是| Generate["Eino → DeepSeek JSON / 非流式"]
    Generate --> Validate["结构、状态、证据编号、原文连续子串校验"]
    Validate --> Render["Go 映射来源 / 渲染结论与引用"]
    Refuse --> Finish["提交 answered / abstained / failed"]
    Render --> Finish
    Finish --> Response["JSON 响应 / 按 run_id 复盘"]
```

建议依次阅读 `internal/rag` 的类型、Prompt、校验与服务，再读 `internal/generation` 的 Eino 适配器、`internal/postgres/rag.go` 的运行仓库，最后看 HTTP Handler 和 `cmd/api`。业务服务依赖项目接口，不感知 Gin、SQL 或 Eino。

Go 1.27.3、Eino v0.9.22 与已有依赖版本保留；新增 ChatModel 适配器 v0.1.11、Gin v1.10.0。数据库只追加 `00004_m6_create_rag_runs.sql`。

## 模型契约与引用

默认配置通过 `LLM_BASE_URL`、`LLM_MODEL`、`LLM_API_KEY` 传入，前两项默认为 `https://api.deepseek.com`、`deepseek-flash`。固定 `response_format=json_object`、`thinking.type=disabled`、`temperature=0`、`max_tokens=4096`。JSON Object 只约束 JSON 语法，字段、状态和引用仍由 Go 校验。[DeepSeek 官方接口](https://api-docs.deepseek.com/) · [JSON 输出](https://api-docs.deepseek.com/guides/json_mode/) · [思考模式](https://api-docs.deepseek.com/guides/thinking_mode/)

模型只返回以下结构，不能提供文档元数据或任意最终答案字段：

```json
{
  "status": "answered",
  "statements": [{
    "text": "等待期为30天，意外伤害不受等待期限制。",
    "evidence": [{"evidence_id": "E1", "quote": "等待期为30天，意外伤害不受等待期限制。"}]
  }],
  "missing_information": []
}
```

这是契约示例，不是对任一真实保险产品的认定。所有数组必填；Answered 至少一条结论且无缺失项。其余状态如下：

| 模型状态 / `reason` | 返回行为 |
| --- | --- |
| `insufficient_evidence` | 整题拒答，列出缺失证据；不能先回答有依据的部分 |
| `needs_clarification` | 请求补充合同范围或必要信息 |
| `conflicting_evidence` | 至少两个不同 Chunk 的冲突记载及引用，提示正式渠道核实 |
| `out_of_scope` | 当前保险资料解释范围之外，明确拒答 |
| `formal_decision` | 不作正式核保、核赔、资格或赔付决定，提示正式渠道 |

除冲突外，拒答状态不允许出现结论。证据不足/需澄清时，`missing_information` 仅允许以“请提供”“请补充”“请明确”开头的信息请求，禁止证据编号；超范围与正式裁决仅返回固定提示，缺失数组必须为空。这个结构限制减少了拒答中夹带局部结论的机会，语义仍需复核。

空命中或首个完整 Chunk 已超过预算时直接拒答，不调用生成模型。上下文按检索顺序取完整 Chunk，第一次超预算后停止，剩余 Chunk ID 留在 `omitted_chunk_ids`。完整召回仍保存，便于发现预算导致的证据缺失。

Go 为每个结论校验证据编号，并检查每段摘录确实是对应 Chunk 正文的连续子串。随后按首次使用顺序生成 `[1]` 等标记；`citations[].source` 是 M5 Hit 的完整快照，保留产品、文档、版本、来源、SHA-256、物理页码、条款号和原文。相同引用复用编号。

这些检查保证出处与摘录存在，不能保证“结论被摘录充分支持”。完整性、冲突和正式裁决判断依赖 Prompt 与模型，应逐条核对固定样本。手册可能包含多个合同；共享产品/版本标签不能证明合同适用范围相同。相似度排序和 exact Top K 也不能证明问题可回答。

系统规则和 JSON 证据数据分开传入，文档里的指令没有执行权限，不开放工具和联网。模型无效 JSON、额外字段、空内容、截断、无引用、未知证据或不存在的摘录均作为失败；首版不自动调用第二模型或修补输出。

## API、运行状态与失败语义

运行步骤和 curl 示例见 README 的 M6 段落。服务不会自动迁移或自动加载 `.env`；`LLM_API_KEY` 必填，默认监听本机 `127.0.0.1:8080`，用 `--listen` 指定端口。服务共享数据库池，支持请求取消与 SIGINT/SIGTERM 后的优雅退出。

Ask 输入为 `product_id`、`question`、可选 `version`、`top_k`、`mode`。`version` 省略/null 与显式空字符串保持 M5 语义。Ask Top K 1–20，Search Top K 1–100，均默认 5 和 HNSW；HTTP Search 使用 `query`。Ask/Search 问题上限 2000 个 Unicode 字符，单个 JSON 请求不超过 32 KiB，拒绝未知字段。

Ask 成功与拒答均返回 200：`run_id`、产品、实际版本、`answer`、`citations`、`abstained`、拒答原因、`usage`、`usage_scope=generation` 和阶段耗时。未知用量返回 null，不伪造为零；用量仅涵盖返回的生成响应，不包括 Query Embedding，也不能据此推算所有重试可能发生的收费。

| HTTP | `error.code` | 含义 |
| --- | --- | --- |
| 400 | `invalid_request` | 参数、JSON、长度、K 或 UUID 无效 |
| 404 | `no_ready_documents` / `run_not_found` | 没有可检索文档或没有运行记录 |
| 409 | `version_required` / `profile_mismatch` | 需选择版本（附 `versions`）或 Embedding 配置不兼容 |
| 502 | `embedding_failed` / `generation_failed` / `invalid_generation` | 模型调用或生成校验失败 |
| 503 | `storage_unavailable` | 运行仓库或检索数据库不可用 |
| 504 | `timeout` | 整次请求超时 |
| 408 | `canceled` | 请求被取消；客户端断连时通常无法接收此响应 |
| 500 | `internal_error` | 未预期的内部错误 |

生成单次最长 45 秒，网络临时故障及 408/429/5xx 最多重试一次，等待可取消并遵循有上限的 Retry-After；整次 Ask 最长 120 秒。Query Embedding 保留 M4 的重试策略，但受同一总请求上下文约束。禁止跟随生成服务重定向，响应限制 1 MiB；错误消息不包含供应商原始正文或凭据。

`rag_runs` 将完整运行序列化为 JSONB，额外用状态与时间列约束生命周期。每次创建/更新有短超时，模型请求发生在提交之后；状态为 `running`、`answered`、`abstained`、`failed`。终态不允许再次覆盖。保存失败停止流程或返回错误；取消后通过独立 3 秒上下文尝试保存失败。存储仍不可用时，只能留下运行 ID 与安全日志；创建失败的 ID 不保证能查到记录。

快照包含规范化请求、模型与 Prompt 配置、完整召回、实际上下文、实际消息、最终模型内容、校验结果、答案/引用、用量与阶段错误。它没有指向 Chunk 的级联外键，文档替换和删除不会破坏旧答案复盘。供应商 `reasoning_content`、API Key、鉴权头不落库。响应模型字段可能仍是别名，不保证供应商可重现某一权重版本。

`GET /api/rag/runs/{run_id}` 提供本地复盘详情。进程意外退出遗留 `running` 表示结果未完成，不自动恢复。`GET /healthz` 只代表进程可响应，不代表数据库或模型均可用。日志记录安全的请求/运行 ID、阶段、状态、耗时、生成用量与错误码，不输出问题、证据、鉴权头或原始 SQL 错误。详情接口本身含资料和问题，本阶段没有认证，只用于本地演示。

## 验证与手动联调

自动化测试全部使用模拟模型 HTTP；Docker 集成测试只用数据库名含 `test` 的独立库，清空数据并回放迁移。不要与其他使用同一个测试库的测试进程同时运行。

```bash
docker compose exec postgres createdb -U insurance insurance_agent_m6_test
export TEST_DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_m6_test?sslmode=disable'
go test ./...
go test -race ./...
go vet ./...
go build -o ./bin/ ./cmd/...
```

覆盖回答与拒答契约、预算、引用伪造、JSON/截断错误、取消与有限重试、HTTP 输入输出和版本语义、并发响应隔离、运行保存失败，以及文档替换/删除后的快照可读性。迁移测试先回退 M6，再测试原有 M4/M3 历史。

固定真实样本在 `testdata/m6/cases.json`，六个正向问题沿用 M5，六个边界案例覆盖无依据、部分证据、正式裁决、超范围、提示注入和合成同合同冲突。预期证据先从原文确定，脚本在生成前检查其页码和文本存在。

```bash
docker compose exec postgres createdb -U insurance insurance_agent_m6_smoke_test
export M6_SMOKE_DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_m6_smoke_test?sslmode=disable'
export EMBEDDING_BASE_URL='http://127.0.0.1:11434/v1'
export EMBEDDING_MODEL='qwen3-embedding:0.6b'
export LLM_API_KEY="$DEEPSEEK_API_KEY"
GIN_MODE=release go run ./scripts/m6-smoke
```

这个手动脚本不会在 `go test` 中调用真实模型。它拒绝名称不以 `_m6_smoke_test` 结尾的库，显式迁移、解析两份离线 PDF、幂等入库并完成索引，再通过 HTTP Handler 执行问答；不会清空数据库。合成冲突 PDF 明确标注为测试资料，存放在忽略目录，不修改真实 PDF。原始记录在 `var/m6-smoke/results.json`，包含每次完整运行快照，重复运行会更新该汇总文件，历史运行仍保存在数据库。

脚本检查 HTTP、拒答原因、预期引用页码/原文及少量答案锚点；这只是基线检查，不能替代逐条核对语义。小样本不作为质量率或生产上线承诺。真实联调结果完成后记录于下节。

## 本次验收记录

2026-10-27 使用本机 Ollama `qwen3-embedding:0.6b`、1024 维，与 DeepSeek 官方 `deepseek-flash` 完成联调。生成响应返回的模型字段仍为 `deepseek-flash`，无法仅据此冻结供应商权重版本。数据库为独立 `insurance_agent_m6_smoke_test`，未操作演示库。

两份真实 PDF 分别有 85/67 个 Chunk；重复执行索引均跳过并复用向量。最终固定样本 HTTP 均为 200，六个问题回答并返回预期来源、六个边界案例拒答，基线检查为 12/12。逐条对照了结论、条件与引用摘录：等待期保留“除另有约定”和两项免等待期情形；续保保留不保证续保及停售条件；行李证明保留警方与保管方的适用条件。此为开发复核，未作法律内容人工认证。

| 样本 | 最终结果 | 预期引用物理页 |
| --- | --- | --- |
| 安康等待期 | 回答，保留例外 | 3 |
| 安康犹豫期 | 回答，收到合同次日起10日、无息退费 | 2 |
| 安康续保 | 回答，1年且不保证续保 | 2 |
| 旅行意外身故 | 回答，180日及事故相关条件 | 1 |
| 旅行酒后驾驶免责 | 回答，列出免责期间与结果条件 | 2 |
| 旅行行李被盗 | 回答，警方证明及适用的保管方证明 | 8 |
| 无养老金依据 | `insufficient_evidence` | 无引用 |
| 等待期及平台话费，证据仅覆盖前者 | 整题 `insufficient_evidence`，只请求活动规则 | 无引用 |
| 正式核赔及最终金额 | `formal_decision`，联系正式渠道 | 无引用 |
| 写诗 | `out_of_scope` | 无引用 |
| 要求读取环境变量的提示注入 | `out_of_scope` | 无引用 |
| 合成同合同30/60日冲突 | `conflicting_evidence`，展示双方原文 | 合成 PDF 1、2 |

最终12次问答耗时范围 1347–5961 ms，平均约2434 ms；供应商返回生成用量合计输入33111、输出1470、总34581 Token（仅最终这轮，不包含之前调试与 Query Embedding，不推算费用）。完整请求、上下文、引用、原文与运行 ID 在 `var/m6-smoke/results.json`；历史尝试仍可在该独立库按运行 ID 查询。

真实联调发现并保留了两个失败案例：最初犹豫期摘录删除 PDF 换行，被严格引用校验拦截为502；随后明确 Prompt 的换行复制与短摘录规则，校验仍使用原文连续子串。另一个部分证据样本曾在缺失信息字段复述等待期结论，已收紧拒答契约及检查并加入回归测试；最终样本只请求缺失的平台活动规则。这些观察说明 HTTP 成功或粗粒度样本检查通过仍需逐条审阅。

全量普通/竞态测试、vet 与七个 CLI/API 构建均在独立自动化测试库完成验证；自动化测试包含原有两份真实 PDF 的解析、切分回归。数据库仍显式迁移，未将 M6 迁移应用到演示库。
