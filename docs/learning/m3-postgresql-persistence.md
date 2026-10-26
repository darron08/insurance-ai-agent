# M3：PostgreSQL 持久化

## 本阶段只解决什么

M3 把已经通过 M2 校验的 Chunk JSON 原子、幂等地写入 PostgreSQL。它不生成 Embedding，不执行检索，也不接入 Gin 或 Eino。

```text
M2 Chunk JSON
  → 严格 JSON 读取
  → 业务契约校验
  → Catalog Service
  → Repository 接口
  → PostgreSQL 事务
  → documents + document_chunks
```

M1 和 M2 仍保留独立 JSON，便于先检查 PDF 解析，再检查 Chunk 边界，最后单独定位入库问题。

## 环境

PostgreSQL 通过 Docker Compose 运行：

```bash
docker compose up -d postgres
export DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent?sslmode=disable'
go run ./cmd/migrate --action up
```

镜像锁定为 `pgvector/pgvector:0.8.7-pg17-bookworm`。M3 Migration 启用 `vector` 扩展，但暂不创建向量列和索引。

## 为什么保留每个 DDL

`db/migrations/` 是追加式的 schema 历史。M3 的第一个文件是：

```text
00001_m3_create_document_catalog.sql
```

后续 M4 如果增加 `embedding` 列，必须新建 `00002_m4_....sql`，而不能回头修改 `00001`。这样可以：

- 从空数据库重放项目的完整演进。
- 看到每个里程碑对 schema 做了什么。
- 让已经运行的数据库从当前版本安全升级。

Migration 文件中的 `Up` 是正式前进路径；`Down` 只供本地学习和验证。回退会删除 schema，所以命令要求显式的 `--confirm-down`。

## 数据模型

### insurance_products

产品标识是文档的过滤边界。M3 输入只有 `product_id`，因此产品名、承保公司和类型允许为空，不用技术标识伪造展示名称。

### documents

保存文档来源、SHA-256、解析器、切分配置、Chunk 数量和处理状态。

幂等键是：

```text
(product_id, version, source_sha256)
```

处理状态是 `chunked`、`embedding`、`ready` 或 `failed`。M3 只会把成功入库的文档置为 `chunked`；后续阶段负责其他转换。

### document_chunks

每个 Chunk 只保存一份正文，通过 `document_id` 关联文档。`(document_id, chunk_index)` 唯一，并使用外键确保不会出现孤立 Chunk。

## 幂等与替换

首次入库返回 `inserted`。再次导入同一幂等键时：

- 默认返回 `skipped` 和原文档 ID，不修改任何行。
- 显式传入 `--replace` 时返回 `replaced`，在同一事务中更新文档、删除旧 Chunk 并插入新 Chunk。

任意一个 Chunk 插入失败都会回滚整个事务，不会留下“文档存在但 Chunk 只有一部分”的状态。

## 为什么先定义 Repository

`internal/catalog` 只看到 Repository 接口和业务结构，不知道 SQL、命令行或 HTTP。PostgreSQL 是该接口的一个 Adapter。

后续接入时：

- Gin Handler 解析 HTTP 请求后调用 Catalog Service。
- Eino Retriever 依赖后续增加的 Chunk 检索接口。
- CLI、Gin 和 Eino 都不直接编写 SQL。

这样 M3 可以专注数据正确性，后续增加传输层和 Agent 编排时不需重写入库逻辑。

## 推荐阅读顺序

1. `db/migrations/00001_m3_create_document_catalog.sql`：表、约束和索引如何表达业务规则。
2. `internal/catalog/catalog.go`：应用服务与 Repository 的边界。
3. `internal/catalog/validate.go`：为什么不能相信手工修改过的 M2 JSON。
4. `internal/postgres/repository.go`：幂等写入和原子替换如何使用事务。
5. `cmd/store/main.go`：CLI 如何只做参数、连接和输出。

## 集成测试

普通 `go test ./...` 不强制启动外部数据库。要执行真实 PostgreSQL 迁移、事务和约束测试，使用名称包含 `test` 的独立数据库：

```bash
docker compose exec postgres createdb -U insurance insurance_agent_test
TEST_DATABASE_URL='postgres://insurance:insurance_dev@localhost:5432/insurance_agent_test?sslmode=disable' \
  go test ./internal/postgres -run TestMigrationsAndRepository -v
```

测试会回退并重放 M3 Migration，因此拒绝连接名称不含 `test` 的数据库。

## 动手实验

1. 连续执行两次 `store`，观察 `inserted` 和 `skipped` 共用同一个文档 ID。
2. 复制一份 Chunk JSON，修改正文但不修改 `character_count`，观察校验如何在连接数据库前失败。
3. 正确修改正文和字符数后使用 `--replace`，查询 `document_chunks` 观察原子替换。
4. 在测试数据库上执行 `down` 和 `up`，检查 Migration 历史如何重建 schema。

## 进入 M4 前的复盘问题

1. 为什么默认重复入库不覆盖 Chunk？
2. 唯一约束和 Go 业务校验分别防止什么问题？
3. 为什么文档和所有 Chunk 必须在一个事务中写入？
4. 为什么不能在 M4 直接修改 M3 Migration？
5. Gin 或 Eino 接入时，哪一层可以复用，哪一层需要新增？
