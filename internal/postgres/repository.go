package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/document"
)

type Repository struct {
	DB *sql.DB
}

func (r Repository) Store(ctx context.Context, input document.ChunkedDocument, replace bool) (catalog.StoreResult, error) {
	if r.DB == nil {
		return catalog.StoreResult{}, fmt.Errorf("数据库连接为空")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return catalog.StoreResult{}, fmt.Errorf("开始入库事务: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO insurance_products (id)
		VALUES ($1)
		ON CONFLICT (id) DO NOTHING`, input.Metadata.ProductID); err != nil {
		return catalog.StoreResult{}, fmt.Errorf("保存产品: %w", err)
	}

	var documentID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (
			product_id, name, version, source_url, source_file_path,
			source_sha256, source_imported_at, source_parser, total_pages,
			chunking_strategy, max_characters, overlap_characters,
			total_chunks, processing_status, chunked_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15
		)
		ON CONFLICT (product_id, version, source_sha256) DO NOTHING
		RETURNING id`,
		input.Metadata.ProductID, input.Metadata.Name, input.Metadata.Version,
		input.Metadata.SourceURL, input.SourceFilePath, input.SourceSHA256,
		input.SourceImportedAt, input.SourceParser, input.TotalPages,
		input.Chunking.Strategy, input.Chunking.MaxCharacters,
		input.Chunking.OverlapCharacters, input.TotalChunks,
		catalog.StatusChunked, input.ChunkedAt,
	).Scan(&documentID)

	outcome := catalog.OutcomeInserted
	if errors.Is(err, sql.ErrNoRows) {
		var existingStatus string
		var existingChunks int
		if err := tx.QueryRowContext(ctx, `
			SELECT id, processing_status, total_chunks
			FROM documents
			WHERE product_id = $1 AND version = $2 AND source_sha256 = $3
			FOR UPDATE`, input.Metadata.ProductID, input.Metadata.Version, input.SourceSHA256,
		).Scan(&documentID, &existingStatus, &existingChunks); err != nil {
			return catalog.StoreResult{}, fmt.Errorf("读取已有文档: %w", err)
		}
		if !replace {
			if err := tx.Commit(); err != nil {
				return catalog.StoreResult{}, fmt.Errorf("提交重复入库检查: %w", err)
			}
			return catalog.StoreResult{
				DocumentID: documentID,
				Outcome:    catalog.OutcomeSkipped,
				Status:     existingStatus,
				ChunkCount: existingChunks,
			}, nil
		}
		outcome = catalog.OutcomeReplaced
		if _, err := tx.ExecContext(ctx, `
			UPDATE documents SET
				name = $2,
				source_url = $3,
				source_file_path = $4,
				source_imported_at = $5,
				source_parser = $6,
				total_pages = $7,
				chunking_strategy = $8,
				max_characters = $9,
				overlap_characters = $10,
				total_chunks = $11,
				processing_status = $12,
				failed_stage = NULL,
				last_error = NULL,
				chunked_at = $13,
				updated_at = now()
			WHERE id = $1`,
			documentID, input.Metadata.Name, input.Metadata.SourceURL,
			input.SourceFilePath, input.SourceImportedAt, input.SourceParser,
			input.TotalPages, input.Chunking.Strategy,
			input.Chunking.MaxCharacters, input.Chunking.OverlapCharacters,
			input.TotalChunks, catalog.StatusChunked, input.ChunkedAt,
		); err != nil {
			return catalog.StoreResult{}, fmt.Errorf("更新已有文档: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
			return catalog.StoreResult{}, fmt.Errorf("删除旧 Chunk: %w", err)
		}
	} else if err != nil {
		return catalog.StoreResult{}, fmt.Errorf("保存文档: %w", err)
	}

	if err := insertChunks(ctx, tx, documentID, input.Chunks); err != nil {
		return catalog.StoreResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return catalog.StoreResult{}, fmt.Errorf("提交入库事务: %w", err)
	}
	return catalog.StoreResult{
		DocumentID: documentID,
		Outcome:    outcome,
		Status:     catalog.StatusChunked,
		ChunkCount: input.TotalChunks,
	}, nil
}

func insertChunks(ctx context.Context, tx *sql.Tx, documentID string, chunks []document.Chunk) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO document_chunks (
			document_id, chunk_index, page_number, section_title, clause_id,
			part_index, part_count, character_count, text
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`)
	if err != nil {
		return fmt.Errorf("准备 Chunk 入库: %w", err)
	}
	defer stmt.Close()
	for _, chunk := range chunks {
		if _, err := stmt.ExecContext(ctx,
			documentID, chunk.ChunkIndex, chunk.PageNumber,
			chunk.SectionTitle, chunk.ClauseID, chunk.PartIndex,
			chunk.PartCount, chunk.CharacterCount, chunk.Text,
		); err != nil {
			return fmt.Errorf("保存第 %d 个 Chunk: %w", chunk.ChunkIndex, err)
		}
	}
	return nil
}

func (r Repository) GetDocument(ctx context.Context, id string) (catalog.StoredDocument, error) {
	if r.DB == nil {
		return catalog.StoredDocument{}, fmt.Errorf("数据库连接为空")
	}
	row := r.DB.QueryRowContext(ctx, documentSelect+` WHERE id = $1`, id)
	doc, err := scanDocument(row)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.StoredDocument{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.StoredDocument{}, fmt.Errorf("读取文档: %w", err)
	}
	return doc, nil
}

func (r Repository) ListDocuments(ctx context.Context, filter catalog.ListFilter) ([]catalog.StoredDocument, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("数据库连接为空")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	conditions := []string{"($1 = '' OR product_id = $1)", "($2 = '' OR processing_status = $2)"}
	query := documentSelect + " WHERE " + strings.Join(conditions, " AND ") + " ORDER BY created_at, id LIMIT $3 OFFSET $4"
	rows, err := r.DB.QueryContext(ctx, query, filter.ProductID, filter.Status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("查询文档列表: %w", err)
	}
	defer rows.Close()
	var documents []catalog.StoredDocument
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("读取文档列表: %w", err)
		}
		documents = append(documents, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历文档列表: %w", err)
	}
	return documents, nil
}

func (r Repository) ListChunks(ctx context.Context, documentID string) ([]catalog.StoredChunk, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("数据库连接为空")
	}
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, document_id, chunk_index, page_number, section_title,
			clause_id, part_index, part_count, character_count, text
		FROM document_chunks
		WHERE document_id = $1
		ORDER BY chunk_index`, documentID)
	if err != nil {
		return nil, fmt.Errorf("查询 Chunk: %w", err)
	}
	defer rows.Close()
	var chunks []catalog.StoredChunk
	for rows.Next() {
		var chunk catalog.StoredChunk
		if err := rows.Scan(
			&chunk.ID, &chunk.DocumentID, &chunk.ChunkIndex, &chunk.PageNumber,
			&chunk.SectionTitle, &chunk.ClauseID, &chunk.PartIndex,
			&chunk.PartCount, &chunk.CharacterCount, &chunk.Text,
		); err != nil {
			return nil, fmt.Errorf("读取 Chunk: %w", err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Chunk: %w", err)
	}
	return chunks, nil
}

const documentSelect = `
	SELECT id, product_id, name, version, source_url, source_file_path,
		source_sha256, source_imported_at, source_parser, total_pages,
		chunking_strategy, max_characters, overlap_characters, total_chunks,
		processing_status, failed_stage, last_error, chunked_at, created_at, updated_at
	FROM documents`

type rowScanner interface {
	Scan(...any) error
}

func scanDocument(row rowScanner) (catalog.StoredDocument, error) {
	var doc catalog.StoredDocument
	var failedStage, lastError sql.NullString
	err := row.Scan(
		&doc.ID, &doc.ProductID, &doc.Name, &doc.Version,
		&doc.SourceURL, &doc.SourceFilePath, &doc.SourceSHA256,
		&doc.SourceImportedAt, &doc.SourceParser, &doc.TotalPages,
		&doc.Chunking.Strategy, &doc.Chunking.MaxCharacters,
		&doc.Chunking.OverlapCharacters, &doc.TotalChunks,
		&doc.ProcessingStatus, &failedStage, &lastError,
		&doc.ChunkedAt, &doc.CreatedAt, &doc.UpdatedAt,
	)
	if failedStage.Valid {
		doc.FailedStage = failedStage.String
	}
	if lastError.Valid {
		doc.LastError = lastError.String
	}
	return doc, err
}
