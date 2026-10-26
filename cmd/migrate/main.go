package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	store "insurance_agent_demo/internal/postgres"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	action := flags.String("action", string(store.MigrationUp), "迁移操作：up、status 或 down")
	confirmDown := flags.Bool("confirm-down", false, "确认执行本地回退（action=down 时必填）")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --action")
		return 2
	}
	migrationAction := store.MigrationAction(*action)
	if migrationAction != store.MigrationUp && migrationAction != store.MigrationStatus && migrationAction != store.MigrationDown {
		fmt.Fprintln(stderr, "--action 只支持 up、status 或 down")
		return 2
	}
	if migrationAction == store.MigrationDown && !*confirmDown {
		fmt.Fprintln(stderr, "down 会回退最新迁移，请显式传入 --confirm-down")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(stderr, "迁移失败:", err)
		return 1
	}
	defer db.Close()
	if err := store.RunMigrations(ctx, db, migrationAction); err != nil {
		fmt.Fprintln(stderr, "迁移失败:", err)
		return 1
	}
	fmt.Fprintf(stdout, "迁移操作完成\n操作: %s\n", migrationAction)
	return 0
}
