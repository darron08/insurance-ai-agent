# M7：从 HTTP API 到本地问答页面

M7 的目标是把 M6 已有的问答服务交给浏览器使用。页面负责产品选择、问题输入和结果展示；Go 仍负责检索、生成、引用校验和持久化。数据库继续使用本地 PostgreSQL。

## 数据如何流动

```mermaid
flowchart LR
    UI[React 页面] -->|GET 产品与版本| Directory[catalog 目录服务]
    Directory --> PG[(本地 PostgreSQL)]
    UI -->|POST 单次完整问题| RAG[M6 Ask 服务]
    RAG --> Retrieval[M5 检索与版本校验]
    Retrieval --> PG
    RAG --> Model[DeepSeek]
    RAG -->|运行与证据快照| PG
    RAG -->|回答或拒答与引用| UI
```

目录接口只读取数据库。它不发 Embedding 请求，也不通过文档分页拼出一个不完整的产品列表。所有 ready 文档在一次 SQL 的同一快照内按产品/版本聚合；该版本有任何 ready 文档的 profile、地址、模型或维度不匹配，就标记为不兼容。目录加载后数据仍可能改变，Ask 会再次校验；页面遇到 404/409 提示重新选择，不能自动切换版本。

返回示例：

```json
{
  "products": [{
    "product_id": "medical",
    "name": null,
    "versions": [{"version": "", "document_count": 1, "compatible": true}]
  }]
}
```

`name=null` 时显示产品 ID。目录为空返回 `products: []`。错误继续使用 M6 的 `error.code/message` 格式，数据库故障 503、超时 504。现有 Ask、Search、运行详情及健康检查契约保留。

## 推荐阅读顺序

1. `internal/catalog/products.go` 与 `internal/postgres/products.go`：项目接口、目录类型与 SQL 边界。
2. `internal/httpapi/products.go`、`web.go` 和 `cmd/api/main.go`：共享连接池，添加目录接口及可选静态托管。
3. `web/src/api.ts` 与 `types.ts`：浏览器请求、公开错误和问答结果契约。
4. `web/src/App.tsx`：目录、选择、草稿、消息和进行中请求的状态。
5. `web/src/Answer.tsx`：仅将已存在的引用编号转换成按钮，展开来源快照。
6. 页面测试与 `web/e2e/chat.spec.ts`：从交互结果检查规则，而不是只看页面能否渲染。

页面可以显示多条消息，但每次 Ask 只发送当前问题，历史消息不进入 Prompt。产品与版本记录在每条问答上；切换选择不会给旧回答换标签。版本状态使用 `undefined` 表示未选择，空字符串仍是一个真实版本，通过选择框序列化区分两者。

## 取消、错误和引用

`AbortController` 取消当前 fetch，活动请求 ID 防止旧响应覆盖新提问。请求进行中锁定产品、版本与输入，防止双击重复提交；停止等待后保留草稿。客户端等待 125 秒，给后端 120 秒上限留出网络余量。超时和网络错误都不会自动重复生成；请求可能已产生运行记录。

清空对话和刷新只移除 React 内存中的消息，不写浏览器历史存储，也不删除数据库。产品目录请求 15 秒超时，允许手动重新加载。

原文和答案作为 React 文本节点输出，不解析 Markdown/HTML，不使用 `dangerouslySetInnerHTML`。只有当前回答引用列表中的 `[数字]` 才成为可点击按钮；来源 URL 仅 HTTP/HTTPS 可点击。摘录保留原始换行，完整 Chunk 放在折叠区，文档页码均为物理 PDF 页码。相似度不显示为可信度。

引用存在性和摘录匹配由 M6 校验。页面不会进行额外语义裁判，M6 的语义和合同适用范围限制继续有效；部分支持问题仍必须整题拒答。

## 本地运行与验证

先按[本地运行与开发指南](../getting-started.md#本地浏览器问答m7)配置 `DATABASE_URL`、`EMBEDDING_*` 和 `LLM_API_KEY`。模型密钥只在 Go 进程环境中，页面没有模型配置或密钥输入。环境文件不自动加载。

```bash
# 项目根目录；Node 24
npm --prefix web ci
npm --prefix web run build
go run ./cmd/migrate --action up
go run ./cmd/api --web-dir ./bin/web
```

访问 `http://127.0.0.1:8080`。Go 使用限定在构建目录内的文件系统，拒绝逃逸目录的符号链接及路径，不提供目录列表或未知路由的首页回退。未知 `/api` 路径继续返回 JSON 404。没有前端参数时，Go 保持纯 API；目录缺少 `index.html` 时启动明确报错。

开发时分别运行 `go run ./cmd/api` 和 `npm --prefix web run dev`，访问 5173；Vite 把 `/api` 代理给 8080，代理等待时间覆盖后端问答上限。

没有 ready 文档时，先运行既有导入流程。例如：

```bash
go run ./cmd/ingest --file ./testdata/real/pingan-ankang-te-xu-ban-2020-medical.pdf --product-id medical --version 2020 --document-name '平安安康医疗保险条款' --output ./var/imports
go run ./cmd/chunk --input ./var/imports/<上步输出>.json --output ./var/chunks
go run ./cmd/store --input ./var/chunks/<上步输出>.json
go run ./cmd/index --document-id <入库输出的UUID>
```

同一套 Embedding 配置必须同时用于 index 和 api。默认是本地 Qwen 的 8001 地址及 Hugging Face 模型名称；本机已入库的 Ollama 索引使用 `http://127.0.0.1:11434/v1` 和 `qwen3-embedding:0.6b`，启动 API 时沿用这两个值。不要为了让目录可选而跳过兼容检查。

```bash
npm --prefix web run typecheck
npm --prefix web test
cd web
npx playwright install chromium
npm run test:e2e
```

前端单元与浏览器自动化全部模拟模型相关响应；Go 模型适配器测试继续使用本地 HTTP 模拟服务器。数据库测试只使用名称含 `test` 的独立数据库。构建输出在 `bin/web`，浏览器截图在 `var/m7-smoke`，不加入 Git。

手动真实验收显式运行，使用 M6 已导入两份真实保单的本地独立验收数据库：

```bash
# 独立终端，配置好本地密钥后启动：
export DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_m6_smoke_test?sslmode=disable'
export EMBEDDING_BASE_URL='http://127.0.0.1:11434/v1'
export EMBEDDING_MODEL='qwen3-embedding:0.6b'
go run ./cmd/api --listen 127.0.0.1:8081 --web-dir ./bin/web
# 另一终端：
M7_SMOKE_URL='http://127.0.0.1:8081' npm --prefix web run test:live
```

这个命令会产生真实模型调用和新的后台问答记录；它不导入、替换或清空文档。缺少 `m6-ankang` 或 `m6-travel` 时，应先按照 M6 学习说明准备独立验收数据。日常使用只需一个本地数据库，不需要长期维护验收库。

## 验收记录与学习实验

2026-10-28 完成 Go 全量测试（包括 Docker 独立测试库）、竞态检查、vet、七个 CLI/API 构建，以及前端类型检查、13 项单元交互测试、构建和 Playwright 端到端测试。桌面与 390 像素窄屏截图已检查，无水平溢出；问答记录刷新后清空。

真实浏览器使用本地 Qwen/Ollama 和 DeepSeek Flash，完成以下三项检查，并核对引用所属产品、版本、原文连续子串与后台最终运行快照：

| 场景 | 结果 | 运行 ID | 后端耗时 |
| --- | --- | --- | --- |
| 医疗险等待期及等待期内患病处理 | 回答，引用物理第 3 页 | `c71c3de2-e154-408a-bea2-5bd9343455a0` | 3863 ms |
| 等待期 + 缺少平台银行卡资料 | 整题证据不足拒答 | `a1e985a4-c7a0-4b0c-9594-91f9c47e2d5d` | 1541 ms |
| 旅行险意外身故 180 天规定 | 回答，引用物理第 1 页 | `07a35824-2e32-4779-87c1-3c742e3e4280` | 2358 ms |

完整结果、运行快照和截图保存于 `var/m7-smoke/`。这些样本验证 M7 页面接入，没有替代 M6 验收和后续 M8 系统评测。

动手实验：在同一产品下准备两个版本，观察选择框必须显式选择；再选空版本并检查网络请求仍携带 `"version":""`。提问后点击引用，核对 PDF 页码；刷新页面后通过已记录的 `run_id` 查询后台，理解页面状态和持久化记录的区别。

已知限制：本地单用户使用，没有登录、文档上传、多轮记忆、流式回答或历史列表。取消请求不能回滚已完成的模型调用或运行记录；模型用量只显示后端提供的生成用量，缺失时不估算。进程退出后按上述命令重新启动，Go 不会自动启动 PostgreSQL 或本地 Embedding 服务。
