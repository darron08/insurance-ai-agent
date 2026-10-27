// Package httpapi maps HTTP input/output to application services.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"insurance_agent_demo/internal/rag"
	"insurance_agent_demo/internal/retrieval"
)

type api struct {
	rag      rag.Service
	search   rag.Searcher
	products ProductLister
}

// Options keeps existing API-only callers source-compatible.
type Options struct {
	Products ProductLister
	Web      http.Handler
}

func New(service rag.Service, searcher rag.Searcher, options ...Options) http.Handler {
	router := gin.New()
	a := api{rag: service, search: searcher}
	var web http.Handler
	if len(options) > 0 {
		a.products, web = options[0].Products, options[0].Web
	}
	router.Use(func(c *gin.Context) {
		started := time.Now()
		requestID := uuid.NewString()
		c.Header("X-Request-ID", requestID)
		ctx, cancel := context.WithTimeout(c.Request.Context(), rag.RequestTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		defer func() {
			if recover() != nil {
				c.Abort()
				writeError(c, &rag.Error{Code: "internal_error", Message: "服务内部错误"})
			}
			// Never log raw paths, queries, request bodies, headers or SQL errors.
			slog.Info("http request", "request_id", requestID, "run_id", c.GetString("run_id"), "route", c.FullPath(), "status", c.Writer.Status(), "total_ms", time.Since(started).Milliseconds())
		}()
		c.Next()
	})
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.POST("/api/rag/ask", a.ask)
	router.POST("/api/rag/search", a.searchOnly)
	router.GET("/api/rag/runs/:run_id", a.getRun)
	router.GET("/api/rag/products", a.listProducts)
	router.NoRoute(func(c *gin.Context) {
		if web != nil && c.Request.Method == http.MethodGet && c.Request.URL.Path != "/api" && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			web.ServeHTTP(c.Writer, c.Request)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": rag.Error{Code: "not_found", Message: "接口不存在"}})
	})
	return router
}

type askBody struct {
	ProductID string  `json:"product_id"`
	Question  string  `json:"question"`
	Version   *string `json:"version"`
	TopK      *int    `json:"top_k"`
	Mode      string  `json:"mode"`
}
type searchBody struct {
	ProductID string  `json:"product_id"`
	Query     string  `json:"query"`
	Version   *string `json:"version"`
	TopK      *int    `json:"top_k"`
	Mode      string  `json:"mode"`
}

func decode(c *gin.Context, target any) bool {
	if strings.Split(c.GetHeader("Content-Type"), ";")[0] != "application/json" {
		writeError(c, &rag.Error{Code: "invalid_request", Message: "Content-Type 必须为 application/json"})
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(c, &rag.Error{Code: "invalid_request", Message: "请求必须是单个合法 JSON 对象，不能包含未知字段，且不得超过 32 KiB"})
		return false
	}
	return true
}

func (a api) ask(c *gin.Context) {
	var body askBody
	if !decode(c, &body) {
		return
	}
	req := rag.Request{ProductID: body.ProductID, Question: body.Question, Version: body.Version, Mode: body.Mode}
	if body.TopK != nil {
		if *body.TopK < 1 {
			writeError(c, &rag.Error{Code: "invalid_request", Message: "top_k 必须在 1 到 20 之间"})
			return
		}
		req.TopK = *body.TopK
	}
	result, err := a.rag.Ask(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Set("run_id", result.RunID)
	c.JSON(http.StatusOK, result)
}

func (a api) searchOnly(c *gin.Context) {
	var body searchBody
	if !decode(c, &body) {
		return
	}
	req := retrieval.Request{ProductID: body.ProductID, Query: body.Query, Version: body.Version, Mode: body.Mode}
	if body.TopK != nil {
		if *body.TopK < 1 {
			writeError(c, &rag.Error{Code: "invalid_request", Message: "top_k 必须在 1 到 100 之间"})
			return
		}
		req.TopK = *body.TopK
	}
	if utf8.RuneCountInString(body.Query) > 2000 {
		writeError(c, &rag.Error{Code: "invalid_request", Message: "query 最长 2000 个字符"})
		return
	}
	var err error
	req, err = retrieval.NormalizeRequest(req)
	if err != nil {
		writeError(c, &rag.Error{Code: "invalid_request", Message: err.Error()})
		return
	}
	result, err := a.search.Search(c.Request.Context(), req)
	if err != nil {
		writeError(c, rag.ClassifyError(err, "retrieval", ""))
		return
	}
	c.JSON(http.StatusOK, result)
}

func (a api) getRun(c *gin.Context) {
	run, err := a.rag.GetRun(c.Request.Context(), c.Param("run_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Set("run_id", run.ID)
	c.JSON(http.StatusOK, run)
}

func writeError(c *gin.Context, err error) {
	var public *rag.Error
	if !errors.As(err, &public) {
		public = &rag.Error{Code: "internal_error", Message: "服务内部错误"}
	}
	if public.RunID != "" {
		c.Set("run_id", public.RunID)
	}
	status := http.StatusInternalServerError
	switch public.Code {
	case "invalid_request":
		status = http.StatusBadRequest
	case "version_required", "profile_mismatch":
		status = http.StatusConflict
	case "no_ready_documents", "run_not_found":
		status = http.StatusNotFound
	case "generation_failed", "invalid_generation", "embedding_failed":
		status = http.StatusBadGateway
	case "storage_unavailable":
		status = http.StatusServiceUnavailable
	case "timeout":
		status = http.StatusGatewayTimeout
	case "canceled":
		status = http.StatusRequestTimeout
	}
	c.JSON(status, gin.H{"error": public})
}
