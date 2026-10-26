package main

import (
	"context"
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
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("index", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("document-id", "", "M3 入库的文档 UUID（必填）")
	reindex := flags.Bool("reindex", false, "清空旧向量并整份重建")
	batchSize := flags.Int("batch-size", indexing.DefaultBatchSize, "每批 Chunk 数量（1–2048）")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --document-id")
		return 2
	}
	if err := indexing.ValidateDocumentID(*id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *batchSize < 1 || *batchSize > 2048 {
		fmt.Fprintln(stderr, "batch-size 必须在 1 到 2048 之间")
		return 1
	}
	profile, err := indexing.NewProfile(getenv("EMBEDDING_BASE_URL"), getenv("EMBEDDING_MODEL"))
	if err != nil {
		fmt.Fprintln(stderr, "配置失败:", err)
		return 1
	}
	model, err := embedding.New(profile, getenv("EMBEDDING_API_KEY"))
	if err != nil {
		fmt.Fprintln(stderr, "配置失败:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connect, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := postgres.Open(connect, getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "索引失败:", err)
		return 1
	}
	defer db.Close()
	service := indexing.Service{Repository: postgres.IndexRepository{DB: db}, Embedder: model, Profile: profile}
	result, err := service.Index(ctx, indexing.Request{DocumentID: *id, BatchSize: *batchSize, Reindex: *reindex})
	if result.DocumentID != "" {
		fmt.Fprintf(stdout, "文档 ID: %s\n结果: %s\n状态: %s\nEmbedding 模型: %s\n总 Chunk 数: %d\n本次处理: %d\n复用数量: %d\n", result.DocumentID, result.Outcome, result.Status, result.Model, result.Total, result.Processed, result.Reused)
	}
	if err != nil {
		fmt.Fprintln(stderr, "索引失败:", err)
		return 1
	}
	return 0
}
