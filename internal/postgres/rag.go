package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"insurance_agent_demo/internal/rag"
)

// RunRepository persists self-contained evidence, not foreign keys to mutable
// Chunk rows. Each operation is short; no transaction spans a model request.
type RunRepository struct{ DB *sql.DB }

func (r RunRepository) Create(ctx context.Context, run rag.Run) error {
	if r.DB == nil {
		return errors.New("数据库连接为空")
	}
	if run.Status != "running" {
		return errors.New("新运行状态必须为 running")
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = r.DB.ExecContext(ctx, `INSERT INTO rag_runs (id,status,started_at,finished_at,snapshot) VALUES ($1,$2,$3,$4,$5)`, run.ID, run.Status, run.StartedAt, run.FinishedAt, raw)
	return err
}

func (r RunRepository) Save(ctx context.Context, run rag.Run) error {
	if r.DB == nil {
		return errors.New("数据库连接为空")
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := r.DB.ExecContext(ctx, `UPDATE rag_runs SET status=$2,finished_at=$3,snapshot=$4 WHERE id=$1 AND status='running'`, run.ID, run.Status, run.FinishedAt, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("运行记录不存在或已经结束")
	}
	return nil
}

func (r RunRepository) Get(ctx context.Context, id string) (rag.Run, error) {
	if r.DB == nil {
		return rag.Run{}, errors.New("数据库连接为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var raw []byte
	err := r.DB.QueryRowContext(ctx, `SELECT snapshot FROM rag_runs WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return rag.Run{}, rag.ErrRunNotFound
	}
	if err != nil {
		return rag.Run{}, err
	}
	var run rag.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return rag.Run{}, err
	}
	return run, nil
}
