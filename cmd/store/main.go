package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"insurance_agent_demo/internal/catalog"
	store "insurance_agent_demo/internal/postgres"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("store", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "M2 Chunk JSON 路径（必填）")
	replace := flags.Bool("replace", false, "原子替换同一产品、版本和校验值的已有 Chunk")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --input 指定 M2 JSON")
		return 2
	}
	chunked, err := catalog.LoadChunkedDocument(*input)
	if err != nil {
		fmt.Fprintln(stderr, "入库失败:", err)
		return 1
	}
	if err := catalog.ValidateChunkedDocument(chunked); err != nil {
		fmt.Fprintln(stderr, "入库失败:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(stderr, "入库失败:", err)
		return 1
	}
	defer db.Close()
	service := catalog.Service{Repository: store.Repository{DB: db}}
	result, err := service.Store(ctx, chunked, *replace)
	if err != nil {
		fmt.Fprintln(stderr, "入库失败:", err)
		return 1
	}
	fmt.Fprintf(stdout, "入库成功\n文档 ID: %s\n结果: %s\n状态: %s\nChunk 数量: %d\n", result.DocumentID, result.Outcome, result.Status, result.ChunkCount)
	return 0
}
