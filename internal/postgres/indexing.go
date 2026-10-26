package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/indexing"
)

// The same key is used by the session lock and Store's transaction lock.
// Hash collisions only serialize otherwise unrelated documents.
const documentLockKey = `hashtextextended('insurance:index:' || $1::uuid::text, 0)`

type IndexRepository struct{ DB *sql.DB }
type indexSession struct {
	conn       *sql.Conn
	documentID string
	profile    indexing.Profile
}

func (r IndexRepository) Lock(ctx context.Context, documentID string) (indexing.Session, error) {
	if r.DB == nil {
		return nil, errors.New("数据库连接为空")
	}
	if err := indexing.ValidateDocumentID(documentID); err != nil {
		return nil, err
	}
	conn, err := r.DB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取索引连接: %w", err)
	}
	s := &indexSession{conn: conn, documentID: documentID}
	var locked bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(`+documentLockKey+`)`, documentID).Scan(&locked); err != nil {
		// The server may have acquired a session lock before the reply failed.
		s.discard()
		return nil, fmt.Errorf("获取文档锁: %w", err)
	}
	if !locked {
		conn.Close()
		return nil, indexing.ErrBusy
	}
	return s, nil
}

func (s *indexSession) discard() {
	// Do not return a possibly locked connection to the pool.
	_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = s.conn.Close()
}

func (s *indexSession) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := s.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(`+documentLockKey+`)`, s.documentID).Scan(&unlocked)
	if err != nil || !unlocked {
		s.discard()
		return errors.New("释放文档锁失败，连接已丢弃")
	}
	return s.conn.Close()
}

func (s *indexSession) Prepare(ctx context.Context, p indexing.Profile, reindex bool) (indexing.Progress, error) {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return indexing.Progress{}, err
	}
	defer tx.Rollback()
	var result indexing.Progress
	var oldProfile sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT total_chunks, processing_status, embedding_profile FROM documents WHERE id = $1 FOR UPDATE`, s.documentID).Scan(&result.Total, &result.Status, &oldProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return result, catalog.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if oldProfile.Valid && oldProfile.String != p.Key() && !reindex {
		return result, indexing.ErrProfileMismatch
	}
	if reindex {
		if _, err = tx.ExecContext(ctx, `UPDATE document_chunks SET embedding = NULL, embedding_model = NULL, embedded_at = NULL WHERE document_id = $1`, s.documentID); err != nil {
			return result, err
		}
	}
	var actual, invalid int
	err = tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE embedding IS NOT NULL AND embedding_model = $2), count(*) FILTER (WHERE embedding IS NOT NULL AND embedding_model <> $2) FROM document_chunks WHERE document_id = $1`, s.documentID, p.Model).Scan(&actual, &result.Completed, &invalid)
	if err != nil {
		return result, err
	}
	if actual != result.Total || actual == 0 || invalid != 0 {
		return result, errors.New("文档 Chunk 数量或模型不一致，请检查数据或 --reindex")
	}
	s.profile = p
	if !reindex && oldProfile.Valid && result.Status == catalog.StatusReady && result.Completed == result.Total {
		return result, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE documents SET processing_status = 'embedding', embedding_profile = $2, embedding_base_url = $3, embedding_model = $4, embedding_dimensions = $5, indexed_at = NULL, failed_stage = NULL, last_error = NULL, updated_at = now() WHERE id = $1`, s.documentID, p.Key(), p.BaseURL, p.Model, p.Dimensions)
	if err != nil {
		return result, err
	}
	result.Status = catalog.StatusEmbedding
	return result, tx.Commit()
}

func (s *indexSession) Pending(ctx context.Context, limit int) ([]indexing.Chunk, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT id, text FROM document_chunks WHERE document_id = $1 AND embedding IS NULL ORDER BY chunk_index LIMIT $2`, s.documentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []indexing.Chunk
	for rows.Next() {
		var c indexing.Chunk
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func (s *indexSession) SaveBatch(ctx context.Context, p indexing.Profile, chunks []indexing.Chunk, vectors [][]float64) error {
	if p != s.profile {
		return indexing.ErrProfileMismatch
	}
	if err := indexing.ValidateVectors(vectors, len(chunks)); err != nil {
		return err
	}
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, c := range chunks {
		result, err := tx.ExecContext(ctx, `UPDATE document_chunks SET embedding = $4::vector, embedding_model = $5, embedded_at = now() WHERE id = $1 AND document_id = $2 AND text = $3 AND embedding IS NULL AND EXISTS (SELECT 1 FROM documents WHERE id = $2 AND processing_status = 'embedding' AND embedding_profile = $6)`, c.ID, s.documentID, c.Text, vectorLiteral(vectors[i]), p.Model, p.Key())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("Chunk 已变更，取消本批次写入")
		}
	}
	return tx.Commit()
}

func vectorLiteral(v []float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, n := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(float32(n)), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func (s *indexSession) Complete(ctx context.Context) error {
	result, err := s.conn.ExecContext(ctx, `UPDATE documents d SET processing_status = 'ready', indexed_at = now(), failed_stage = NULL, last_error = NULL, updated_at = now() WHERE d.id = $1 AND d.processing_status = 'embedding' AND d.embedding_profile = $2 AND d.total_chunks = (SELECT count(*) FROM document_chunks c WHERE c.document_id = d.id) AND d.total_chunks = (SELECT count(*) FROM document_chunks c WHERE c.document_id = d.id AND c.embedding IS NOT NULL AND c.embedding_model = d.embedding_model)`, s.documentID, s.profile.Key())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("Chunk 未全部完成，不能标记 ready")
	}
	return nil
}

func (s *indexSession) Fail(ctx context.Context, stage, message string) error {
	result, err := s.conn.ExecContext(ctx, `UPDATE documents SET processing_status = 'failed', failed_stage = $2, last_error = $3, indexed_at = NULL, updated_at = now() WHERE id = $1 AND processing_status = 'embedding' AND embedding_profile = $4`, s.documentID, stage, message, s.profile.Key())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("失败状态未保存：文档已变更或不存在")
	}
	return nil
}
