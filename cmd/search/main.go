package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"insurance_agent_demo/internal/embedding"
	"insurance_agent_demo/internal/indexing"
	"insurance_agent_demo/internal/postgres"
	"insurance_agent_demo/internal/retrieval"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return runWithSearch(args, stdout, stderr, getenv, search)
}

// The injected boundary lets CLI tests verify successful JSON and explicit-empty
// flags without starting a database or loading real API credentials.
type searchFunc func(context.Context, retrieval.Request, func(string) string) (retrieval.Result, error)

func runWithSearch(args []string, stdout, stderr io.Writer, getenv func(string) string, execute searchFunc) int {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var req retrieval.Request
	var version string
	flags.StringVar(&req.ProductID, "product-id", "", "产品标识（必填）")
	flags.StringVar(&req.Query, "query", "", "检索问题（必填）")
	flags.StringVar(&version, "version", "", "文档版本；省略时选择唯一 ready 版本，显式空值选择空版本")
	flags.IntVar(&req.TopK, "top-k", retrieval.DefaultTopK, "返回数量（1–100）")
	flags.StringVar(&req.Mode, "mode", retrieval.ModeHNSW, "检索模式：hnsw 或 exact")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --query")
		return 2
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "version" {
			req.Version = &version
		}
	})
	if req.TopK < 1 || req.TopK > 100 {
		fmt.Fprintln(stderr, "top-k 必须在 1 到 100 之间")
		return 1
	}
	if req.Mode != retrieval.ModeHNSW && req.Mode != retrieval.ModeExact {
		fmt.Fprintln(stderr, "mode 必须为 hnsw 或 exact")
		return 1
	}
	var err error
	req, err = retrieval.NormalizeRequest(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := execute(ctx, req, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "检索失败:", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "输出检索结果失败")
		return 1
	}
	return 0
}

func search(ctx context.Context, req retrieval.Request, getenv func(string) string) (retrieval.Result, error) {
	profile, err := indexing.NewProfile(getenv("EMBEDDING_BASE_URL"), getenv("EMBEDDING_MODEL"))
	if err != nil {
		return retrieval.Result{}, err
	}
	model, err := embedding.New(profile, getenv("EMBEDDING_API_KEY"))
	if err != nil {
		return retrieval.Result{}, err
	}
	connect, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := postgres.Open(connect, getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		return retrieval.Result{}, err
	}
	defer db.Close()
	service := retrieval.Service{Repository: postgres.SearchRepository{DB: db}, Embedder: model, Profile: profile}
	return service.Search(ctx, req)
}
