package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/pressly/goose/v3"

	"insurance_agent_demo/db/migrations"
)

type MigrationAction string

const (
	MigrationUp     MigrationAction = "up"
	MigrationStatus MigrationAction = "status"
	MigrationDown   MigrationAction = "down"
)

var migrationMu sync.Mutex

func RunMigrations(ctx context.Context, db *sql.DB, action MigrationAction) error {
	if db == nil {
		return fmt.Errorf("数据库连接为空")
	}
	migrationMu.Lock()
	defer migrationMu.Unlock()

	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("配置迁移方言: %w", err)
	}
	switch action {
	case MigrationUp:
		if err := goose.UpContext(ctx, db, "."); err != nil {
			return fmt.Errorf("执行数据库迁移: %w", err)
		}
	case MigrationStatus:
		if err := goose.StatusContext(ctx, db, "."); err != nil {
			return fmt.Errorf("读取数据库迁移状态: %w", err)
		}
	case MigrationDown:
		if err := goose.DownContext(ctx, db, "."); err != nil {
			return fmt.Errorf("回退数据库迁移: %w", err)
		}
	default:
		return fmt.Errorf("不支持的迁移操作 %q", action)
	}
	return nil
}
