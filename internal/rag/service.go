package rag

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"insurance_agent_demo/internal/retrieval"
)

type Service struct {
	Searcher  Searcher
	Generator Generator
	Runs      RunRepository
	Config    Config
}

// ClassifyError maps internal causes to stable, safe public error information.
func ClassifyError(err error, stage, runID string) *Error {
	result := &Error{Code: "storage_unavailable", Message: "运行记录或数据库暂时不可用", RunID: runID, Stage: stage, Cause: err}
	var versions *retrieval.VersionRequiredError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		result.Code, result.Message = "timeout", "请求超时，请稍后重试"
	case errors.Is(err, context.Canceled):
		result.Code, result.Message = "canceled", "请求已取消"
	case errors.As(err, &versions):
		result.Code, result.Message, result.Versions = "version_required", "存在多个可检索版本，请明确选择", versions.Versions
	case errors.Is(err, retrieval.ErrNoReadyDocuments):
		result.Code, result.Message = "no_ready_documents", retrieval.ErrNoReadyDocuments.Error()
	case errors.Is(err, retrieval.ErrProfileMismatch):
		result.Code, result.Message = "profile_mismatch", retrieval.ErrProfileMismatch.Error()
	case errors.Is(err, retrieval.ErrEmbeddingUnavailable):
		result.Code, result.Message = "embedding_failed", "问题向量服务暂时不可用"
	case stage == "generation":
		result.Code, result.Message = "generation_failed", "生成模型服务暂时不可用"
	case stage == "validation":
		result.Code, result.Message = "invalid_generation", "生成结果未通过结构或引用校验"
	}
	return result
}

func (s Service) Ask(parent context.Context, input Request) (result Result, err error) {
	req, err := NormalizeRequest(input)
	if err != nil {
		return result, err
	}
	cfg, configErr := NewConfig(s.Config.BaseURL, s.Config.Model)
	if configErr != nil || cfg != s.Config || s.Searcher == nil || s.Generator == nil || s.Runs == nil {
		return result, &Error{Code: "storage_unavailable", Message: "问答服务配置不完整", Stage: "configuration"}
	}
	ctx, cancel := context.WithTimeout(parent, RequestTimeout)
	defer cancel()
	run := Run{ID: uuid.NewString(), Status: "running", StartedAt: time.Now().UTC(), Request: req, Config: cfg, Evidence: []Evidence{}, OmittedChunkIDs: []string{}, Messages: []Message{}, Validation: "not_run"}
	if err = s.Runs.Create(ctx, run); err != nil {
		return result, ClassifyError(err, "create_run", run.ID)
	}
	stage := "retrieval"
	defer func() {
		if err != nil {
			err = ClassifyError(err, stage, run.ID)
			run.Status, run.Error, run.Result = "failed", err.(*Error), nil
			finished := time.Now().UTC()
			run.FinishedAt, run.Timings.TotalMS = &finished, time.Since(run.StartedAt).Milliseconds()
			// Finalization must survive a canceled HTTP request, but is bounded.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
			defer stop()
			if saveErr := s.Runs.Save(cleanup, run); saveErr != nil {
				slog.Error("rag finalization failed", "run_id", run.ID, "stage", stage, "code", "storage_unavailable")
			}
		}
		code := ""
		if run.Error != nil {
			code = run.Error.Code
		}
		attrs := []any{"run_id", run.ID, "stage", stage, "status", run.Status, "total_ms", run.Timings.TotalMS, "code", code}
		if run.Generation != nil && run.Generation.Usage != nil {
			attrs = append(attrs, "generation_tokens", run.Generation.Usage.TotalTokens)
		}
		slog.Info("rag run", attrs...)
	}()
	started := time.Now()
	search, err := s.Searcher.Search(ctx, retrieval.Request{ProductID: req.ProductID, Query: req.Question, Version: req.Version, TopK: req.TopK, Mode: req.Mode})
	run.Timings.SearchMS = time.Since(started).Milliseconds()
	if err != nil {
		return result, err
	}
	run.Retrieval = &search
	run.Evidence, run.OmittedChunkIDs = BuildContext(search.Hits)
	run.Messages = BuildMessages(req, search.Version, run.Evidence)
	stage = "save_context"
	if err = s.Runs.Save(ctx, run); err != nil {
		return result, err
	}
	d := Decision{Status: Insufficient, Statements: []Statement{}, MissingInformation: []string{"请提供可用于完整回答的条款证据"}}
	if len(run.Evidence) > 0 {
		stage = "generation"
		started = time.Now()
		generation, genErr := s.Generator.Generate(ctx, run.Messages)
		run.Timings.GenerationMS = time.Since(started).Milliseconds()
		run.Generation = &generation
		if genErr != nil {
			return result, genErr
		}
		stage = "validation"
		d, err = ValidateDecision(generation, run.Evidence)
		if err != nil {
			run.Validation = "failed"
			return result, err
		}
		run.Validation = "passed"
	} else {
		run.Validation = "not_needed"
	}
	answer, citations := render(d, run.Evidence)
	run.Status = "answered"
	if d.Status != Answered {
		run.Status = "abstained"
	}
	finished := time.Now().UTC()
	run.FinishedAt, run.Timings.TotalMS = &finished, time.Since(run.StartedAt).Milliseconds()
	result = Result{RunID: run.ID, ProductID: req.ProductID, Version: search.Version, Answer: answer, Citations: citations, Abstained: d.Status != Answered, UsageScope: "generation", Timings: run.Timings}
	if result.Abstained {
		result.Reason = d.Status
	}
	if run.Generation != nil {
		result.Usage = run.Generation.Usage
	}
	run.Result = &result
	stage = "save_result"
	if err = s.Runs.Save(ctx, run); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s Service) GetRun(ctx context.Context, id string) (Run, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return Run{}, &Error{Code: "invalid_request", Message: "run_id 必须是有效 UUID"}
	}
	id = parsed.String()
	run, err := s.Runs.Get(ctx, id)
	if errors.Is(err, ErrRunNotFound) {
		return Run{}, &Error{Code: "run_not_found", Message: ErrRunNotFound.Error()}
	}
	if err != nil {
		return Run{}, ClassifyError(err, "get_run", id)
	}
	return run, nil
}
