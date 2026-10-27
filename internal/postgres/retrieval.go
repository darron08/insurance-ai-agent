package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"insurance_agent_demo/internal/indexing"
	"insurance_agent_demo/internal/retrieval"
)

type SearchRepository struct{ DB *sql.DB }

// Search keeps version resolution, profile checks, text and vector reads in one
// MVCC snapshot. Model calls have already completed before this transaction starts.
func (r SearchRepository) Search(ctx context.Context, params retrieval.SearchParams) (retrieval.Result, error) {
	if r.DB == nil {
		return retrieval.Result{}, errors.New("数据库连接为空")
	}
	req, err := retrieval.NormalizeRequest(params.Request)
	if err != nil {
		return retrieval.Result{}, err
	}
	p, err := indexing.NewProfile(params.Profile.BaseURL, params.Profile.Model)
	if err != nil || p != params.Profile {
		return retrieval.Result{}, errors.New("检索模型配置无效")
	}
	if err := indexing.ValidateVectors([][]float64{params.Vector}, 1); err != nil {
		return retrieval.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return retrieval.Result{}, fmt.Errorf("开始检索事务: %w", err)
	}
	defer tx.Rollback()
	result, err := searchSnapshot(ctx, tx, retrieval.SearchParams{Request: req, Profile: p, Vector: params.Vector})
	if err != nil {
		return retrieval.Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return retrieval.Result{}, fmt.Errorf("结束检索事务: %w", err)
	}
	return result, nil
}

// searchSnapshot also lets integration tests hold a real snapshot across a
// concurrent replacement, without adding hooks to the application service.
func searchSnapshot(ctx context.Context, tx *sql.Tx, params retrieval.SearchParams) (retrieval.Result, error) {
	req, p := params.Request, params.Profile
	var version string
	if req.Version == nil {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT version FROM documents WHERE product_id=$1 AND processing_status='ready' ORDER BY version`, req.ProductID)
		if err != nil {
			return retrieval.Result{}, err
		}
		versions := []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return retrieval.Result{}, err
			}
			versions = append(versions, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return retrieval.Result{}, err
		}
		if len(versions) == 0 {
			return retrieval.Result{}, retrieval.ErrNoReadyDocuments
		}
		if len(versions) > 1 {
			return retrieval.Result{}, &retrieval.VersionRequiredError{Versions: versions}
		}
		version = versions[0]
	} else {
		version = *req.Version
	}
	var total, mismatched int
	err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE
		embedding_profile IS DISTINCT FROM $3 OR embedding_base_url IS DISTINCT FROM $4
		OR embedding_model IS DISTINCT FROM $5 OR embedding_dimensions IS DISTINCT FROM $6)
		FROM documents WHERE product_id=$1 AND version=$2 AND processing_status='ready'`,
		req.ProductID, version, p.Key(), p.BaseURL, p.Model, p.Dimensions).Scan(&total, &mismatched)
	if err != nil {
		return retrieval.Result{}, err
	}
	if total == 0 {
		return retrieval.Result{}, retrieval.ErrNoReadyDocuments
	}
	if mismatched != 0 {
		return retrieval.Result{}, retrieval.ErrProfileMismatch
	}

	if req.Mode == retrieval.ModeHNSW {
		for _, setting := range []string{
			`SET LOCAL hnsw.iterative_scan = strict_order`,
			`SET LOCAL hnsw.ef_search = 100`,
			`SET LOCAL hnsw.max_scan_tuples = 20000`,
		} {
			if _, err := tx.ExecContext(ctx, setting); err != nil {
				return retrieval.Result{}, err
			}
		}
	}
	query := searchHNSWSQL
	if req.Mode == retrieval.ModeExact {
		query = searchExactSQL
	}
	rows, err := tx.QueryContext(ctx, query, req.ProductID, version, p.Key(), p.Model, p.Dimensions, vectorLiteral(params.Vector), req.TopK)
	if err != nil {
		return retrieval.Result{}, err
	}
	defer rows.Close()
	result := retrieval.Result{Version: version, Hits: []retrieval.Hit{}}
	for rows.Next() {
		var h retrieval.Hit
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &h.ProductID, &h.DocumentName,
			&h.DocumentVersion, &h.SourceURL, &h.SourceSHA256, &h.PageNumber, &h.ChunkIndex,
			&h.SectionTitle, &h.ClauseID, &h.PartIndex, &h.PartCount, &h.Text, &h.Distance); err != nil {
			return retrieval.Result{}, err
		}
		h.Score = 1 - h.Distance
		result.Hits = append(result.Hits, h)
	}
	return result, rows.Err()
}

// A single projection/predicate prevents the two modes from drifting. SQL values
// are parameters; the only query selection is between these two fixed constants.
const searchFields = `c.id AS chunk_id, c.document_id, d.product_id, d.name AS document_name,
	d.version AS document_version, d.source_url, d.source_sha256,
	c.page_number, c.chunk_index, c.section_title, c.clause_id, c.part_index, c.part_count, c.text`

const searchFrom = ` FROM document_chunks c JOIN documents d ON d.id=c.document_id
	WHERE d.product_id=$1 AND d.version=$2 AND d.processing_status='ready'
	AND d.embedding_profile=$3 AND d.embedding_model=$4 AND d.embedding_dimensions=$5
	AND c.embedding IS NOT NULL AND vector_dims(c.embedding)=$5
	AND c.embedding_model=$4 AND c.embedded_at IS NOT NULL`

const searchOutput = `chunk_id, document_id, product_id, document_name, document_version,
	source_url, source_sha256, page_number, chunk_index, section_title, clause_id,
	part_index, part_count, text, distance`

// Only distance participates in the inner ordering so the vector index remains
// usable. Stable tie ordering applies to the returned candidate set, not its boundary.
const searchHNSWSQL = `WITH nearest AS MATERIALIZED (
	SELECT ` + searchFields + `, c.embedding <=> $6::vector AS distance` + searchFrom + `
	ORDER BY c.embedding <=> $6::vector LIMIT $7
) SELECT ` + searchOutput + ` FROM nearest ORDER BY distance, document_id, chunk_index`

// Materializing all eligible vectors before ordering prevents approximate index
// truncation while still allowing ordinary indexes to accelerate the filters.
const searchExactSQL = `WITH candidates AS MATERIALIZED (
	SELECT ` + searchFields + `, c.embedding` + searchFrom + `
) SELECT ` + searchOutput + ` FROM (
	SELECT candidates.*, embedding <=> $6::vector AS distance FROM candidates
) scored ORDER BY distance, document_id, chunk_index LIMIT $7`
