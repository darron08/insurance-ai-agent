// Package indexing 实现 M4 的向量索引应用流程。
//
// 这个包只定义业务流程和项目自己的接口，不直接依赖 Eino、PostgreSQL、
// CLI 或未来的 Gin Handler。不同入口只需组装对应的 Embedder 和 Repository，
// 就可以复用同一套批处理、失败续跑、幂等和状态转换规则。
package indexing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	// Dimensions 是 M4 固定使用的向量维度，对应数据库中的 vector(1024)。
	// 如果以后改变维度，需要新增数据库迁移，不能只修改此常量。
	Dimensions = 1024

	// DefaultBaseURL 指向本机 vLLM 的 OpenAI 兼容接口。
	DefaultBaseURL = "http://127.0.0.1:8001/v1"
	// DefaultModel 是本地服务加载并对外暴露的 Hugging Face 模型 ID。
	DefaultModel = "Qwen/Qwen3-Embedding-0.6B"

	// DefaultBatchSize 是一次发送给 Embedding 服务的默认 Chunk 数量。
	DefaultBatchSize = 16
)

var (
	// ErrBusy 表示同一份文档已被另一个索引任务占用。
	ErrBusy = errors.New("文档正在处理中，请稍后重试")
	// ErrProfileMismatch 表示已有向量与本次模型配置不同，不能混合使用。
	ErrProfileMismatch = errors.New("Embedding 配置不同，请使用 --reindex 重建")
)

// Profile 描述一套可识别的 Embedding 配置。
//
// 它会存入 documents 表，并通过 Key 生成稳定标识。密钥不属于 Profile，
// 因而不会被写入数据库；更换 API Key 也不会被误判成更换向量模型。
type Profile struct {
	// BaseURL 是规范化后的 OpenAI 兼容 API 基础地址，例如 http://127.0.0.1:8001/v1。
	BaseURL string `json:"base_url"`
	// Model 是请求 Embedding API 时使用的模型名称。
	Model string `json:"model"`
	// Dimensions 是模型必须返回的向量维度，Qwen3-Embedding-0.6B 固定为 1024。
	Dimensions int `json:"dimensions"`
}

// NewProfile 补全默认配置，并把服务地址规范化。
// 禁止在 URL 中携带凭据、查询参数或片段，避免敏感信息进入配置标识和数据库。
func NewProfile(baseURL, model string) (Profile, error) {
	baseURL, model = strings.TrimSpace(baseURL), strings.TrimSpace(model)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Profile{}, errors.New("EMBEDDING_BASE_URL 必须是无凭据、查询参数和片段的 HTTP/HTTPS 地址")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return Profile{BaseURL: u.String(), Model: model, Dimensions: Dimensions}, nil
}

// Key 返回当前配置的稳定 SHA-256 标识。
// 索引续跑时通过它判断已有向量是否由同一服务地址、模型和维度生成。
func (p Profile) Key() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidateDocumentID 在访问数据库前检查 CLI 传入的文档 UUID 格式。
func ValidateDocumentID(id string) error {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return errors.New("--document-id 必须是有效 UUID")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(decoded) != 16 {
		return errors.New("--document-id 必须是有效 UUID")
	}
	return nil
}

// ValidateVectors 检查模型响应能否安全写入 PostgreSQL 的 vector(1024)。
//
// 除了数量和维度，它还拒绝 NaN、无穷大、float32 溢出以及零向量。
// pgvector 使用 float32 存储元素；零向量也不适合建立余弦距离索引。
func ValidateVectors(vectors [][]float64, count int) error {
	if len(vectors) != count {
		return errors.New("Embedding 返回数量与输入不一致")
	}
	for _, v := range vectors {
		if len(v) != Dimensions {
			return fmt.Errorf("Embedding 向量维度必须为 %d", Dimensions)
		}
		nonzero := false
		for _, n := range v {
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > math.MaxFloat32 {
				return errors.New("Embedding 含有无效数值")
			}
			if float32(n) != 0 {
				nonzero = true
			}
		}
		if !nonzero {
			return errors.New("Embedding 不允许零向量")
		}
	}
	return nil
}

// Chunk 是索引流程需要的最小 Chunk 视图。
// ID 用于把向量写回原数据库行，Text 是发送给 Embedding 服务的原始正文。
// 页码、条款号等元数据不参与 M4 的 Embedding 请求，因此不放进该结构体。
type Chunk struct {
	ID   string
	Text string
}

// Progress 表示索引开始或恢复时，数据库中已有的处理进度。
type Progress struct {
	// Total 是这份文档应有的 Chunk 总数，Completed 是已保存向量的数量。
	Total, Completed int
	// Status 是文档当前处理状态，例如 embedding 或 ready。
	Status string
}

// Request 是应用服务的输入，可由 CLI、未来的 Gin Handler 或后台 Worker 构造。
type Request struct {
	// DocumentID 是 M3 入库后 documents.id 的 UUID。
	DocumentID string
	// BatchSize 控制每次模型调用包含多少个 Chunk；0 表示使用默认值。
	BatchSize int
	// Reindex 为 true 时先清空已有向量，再从第一批开始重建。
	Reindex bool
}

// Result 汇总一次索引调用的结果，供 CLI 或未来的 HTTP 接口展示。
type Result struct {
	// Outcome 当前可能是 indexed 或 skipped；Status 是数据库最终状态。
	DocumentID, Outcome, Status, Model string
	// Processed 是本次新处理数，Reused 是续跑时直接复用的已有向量数。
	Total, Processed, Reused int
}

// Embedder 隔离具体模型 SDK。
// Eino 适配器实现该接口，测试则可以使用不访问网络的假实现。
type Embedder interface {
	// Embed 必须按输入文本的顺序返回一一对应的向量。
	Embed(context.Context, []string) ([][]float64, error)
}

// Repository 是索引服务使用的持久化入口。
// PostgreSQL 的 SQL、事务和 advisory lock 都隐藏在其实现中。
type Repository interface {
	// Lock 为一份文档取得独占会话；调用方结束时必须关闭返回的 Session。
	Lock(context.Context, string) (Session, error)
}

// Session 表示一份文档从准备到完成期间持有的数据库会话。
//
// PostgreSQL 实现会让 Session 独占一条连接，并在 Close 前一直持有文档锁，
// 防止同一文档被并发索引或被 store --replace 替换。模型 API 调用可以发生在
// Session 生命周期内，但不能放进数据库事务，否则网络等待会形成长事务。
type Session interface {
	// Prepare 校验文档和模型配置，处理 reindex，并将文档置为 embedding。
	Prepare(context.Context, Profile, bool) (Progress, error)
	// Pending 按顺序读取下一批尚未生成向量的 Chunk。
	Pending(context.Context, int) ([]Chunk, error)
	// SaveBatch 用短事务原子保存一整个批次；任一 Chunk 失败则整批回滚。
	SaveBatch(context.Context, Profile, []Chunk, [][]float64) error
	// Complete 仅在所有 Chunk 都有正确向量后把文档标记为 ready。
	Complete(context.Context) error
	// Fail 记录失败阶段和经过脱敏的错误摘要，同时保留已经成功的批次。
	Fail(context.Context, string, string) error
	// Close 释放文档锁和专用数据库连接。
	Close() error
}

// Service 编排完整的 M4 索引流程。
// Repository 和 Embedder 都是接口，因此该服务可以被多种入口直接复用。
type Service struct {
	// Repository 负责读取 Chunk、保存进度和维护文档状态。
	Repository Repository
	// Embedder 负责把一批原始 Chunk 正文转换成向量。
	Embedder Embedder
	// Profile 是本次索引使用的规范化模型配置。
	Profile Profile
}

// Index 为指定文档生成或恢复向量索引。
//
// 主流程是：校验输入 → 获取文档锁 → 准备或恢复进度 → 循环读取批次 →
// 调用模型 → 校验并保存向量 → 全部完成后标记 ready。
func (s Service) Index(ctx context.Context, req Request) (result Result, err error) {
	// 尽早拒绝无效参数，避免为明显错误占用数据库连接。
	if err := ValidateDocumentID(req.DocumentID); err != nil {
		return result, err
	}
	if req.BatchSize == 0 {
		req.BatchSize = DefaultBatchSize
	}
	if req.BatchSize < 1 || req.BatchSize > 2048 {
		return result, errors.New("batch-size 必须在 1 到 2048 之间")
	}
	p, e := NewProfile(s.Profile.BaseURL, s.Profile.Model)
	if e != nil {
		return result, e
	}
	if s.Profile != p {
		return result, errors.New("索引服务必须使用规范化的 1024 维模型配置")
	}
	if s.Repository == nil || s.Embedder == nil {
		return result, errors.New("未配置索引仓库或 Embedding 服务")
	}

	// Session 在整个索引过程持有文档锁；无论成功失败都必须释放。
	session, err := s.Repository.Lock(ctx, strings.ToLower(req.DocumentID))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, session.Close()) }()
	progress, err := session.Prepare(ctx, p, req.Reindex)
	if err != nil {
		return result, fmt.Errorf("准备索引: %w", err)
	}
	result = Result{DocumentID: req.DocumentID, Model: p.Model, Total: progress.Total, Reused: progress.Completed, Status: progress.Status, Outcome: "skipped"}
	if progress.Status == "ready" {
		// 配置一致且向量完整时保持幂等，不再调用模型。
		return result, nil
	}
	result.Outcome, result.Status = "indexed", "embedding"
	stage := "embedding"

	// 统一处理 Prepare 之后的失败。即使原请求被取消，也额外给数据库最多
	// 5 秒记录失败状态；这不会删除此前已经成功提交的批次。
	defer func() {
		if err == nil {
			return
		}
		result.Status = "failed"
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// 只保存有限的阶段描述，不把上游响应正文或敏感配置写入数据库。
		if failure := session.Fail(cleanup, stage, "索引失败（"+stage+"），详情见命令错误输出"); failure != nil {
			err = errors.Join(err, fmt.Errorf("无法保存失败状态: %w", failure))
		}
	}()
	for {
		stage = "load_chunks"
		chunks, e := session.Pending(ctx, req.BatchSize)
		if e != nil {
			return result, fmt.Errorf("读取待索引 Chunk: %w", e)
		}
		if len(chunks) == 0 {
			// 没有待处理 Chunk，说明首次索引已完成，或中断后已补齐全部批次。
			break
		}

		// M4 只把原始正文发送给模型，不拼接页码、章节或条款元数据。
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		stage = "embedding"
		vectors, e := s.Embedder.Embed(ctx, texts)
		if e != nil {
			return result, fmt.Errorf("生成 Embedding: %w", e)
		}
		stage = "validate_embedding"
		if e = ValidateVectors(vectors, len(chunks)); e != nil {
			return result, e
		}
		stage = "save_embeddings"
		if e = session.SaveBatch(ctx, p, chunks, vectors); e != nil {
			return result, fmt.Errorf("保存 Embedding: %w", e)
		}
		// SaveBatch 成功提交后才计入本次处理数；后续批次失败时可以复用它。
		result.Processed += len(chunks)
	}

	// Complete 会再次在数据库中验证所有 Chunk 均已生成正确模型的向量。
	stage = "complete_index"
	if err = session.Complete(ctx); err != nil {
		return result, fmt.Errorf("完成索引: %w", err)
	}
	result.Status = "ready"
	return result, nil
}
