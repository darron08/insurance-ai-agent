package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"insurance_agent_demo/internal/embedding"
	"insurance_agent_demo/internal/generation"
	"insurance_agent_demo/internal/httpapi"
	"insurance_agent_demo/internal/indexing"
	"insurance_agent_demo/internal/postgres"
	"insurance_agent_demo/internal/rag"
	"insurance_agent_demo/internal/retrieval"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("api", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP 监听地址（本地演示）")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "不接受位置参数")
		return 2
	}
	cfg, err := rag.NewConfig(getenv("LLM_BASE_URL"), getenv("LLM_MODEL"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	generator, err := generation.New(cfg, getenv("LLM_API_KEY"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	profile, err := indexing.NewProfile(getenv("EMBEDDING_BASE_URL"), getenv("EMBEDDING_MODEL"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	embedder, err := embedding.New(profile, getenv("EMBEDDING_API_KEY"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connect, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := postgres.Open(connect, getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "连接 PostgreSQL 失败，请检查 DATABASE_URL")
		return 1
	}
	defer db.Close()
	searcher := retrieval.Service{Repository: postgres.SearchRepository{DB: db}, Embedder: embedder, Profile: profile}
	service := rag.Service{Searcher: searcher, Generator: generator, Runs: postgres.RunRepository{DB: db}, Config: cfg}
	fmt.Fprintln(stdout, "M6 API 监听配置:", *listen)
	if err := serve(ctx, *listen, httpapi.New(service, searcher)); err != nil {
		fmt.Fprintln(stderr, "HTTP 服务启动或关闭失败")
		return 1
	}
	return 0
}

func serve(ctx context.Context, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
