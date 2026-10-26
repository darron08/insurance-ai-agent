-- +goose Up
-- Existing 1536-dimensional vectors belong to a different model space and
-- cannot be reused or resized. Clear only index-derived data; Chunk text and
-- metadata remain unchanged.
UPDATE document_chunks
SET embedding = NULL, embedding_model = NULL, embedded_at = NULL
WHERE embedding IS NOT NULL OR embedding_model IS NOT NULL OR embedded_at IS NOT NULL;

UPDATE documents
SET processing_status = 'chunked',
    embedding_profile = NULL,
    embedding_base_url = NULL,
    embedding_model = NULL,
    embedding_dimensions = NULL,
    indexed_at = NULL,
    failed_stage = NULL,
    last_error = NULL,
    updated_at = now()
WHERE embedding_profile IS NOT NULL;

DROP INDEX document_chunks_embedding_hnsw_idx;

ALTER TABLE documents
    DROP CONSTRAINT documents_embedding_dimensions_check,
    ADD CONSTRAINT documents_embedding_dimensions_check CHECK (embedding_dimensions = 1024);

ALTER TABLE document_chunks
    ALTER COLUMN embedding TYPE vector(1024) USING NULL::vector(1024);

CREATE INDEX document_chunks_embedding_hnsw_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
-- A dimension rollback also invalidates all Qwen vectors. Restore the exact
-- schema expected by 00002 while preserving M3 documents and Chunk text.
UPDATE document_chunks
SET embedding = NULL, embedding_model = NULL, embedded_at = NULL
WHERE embedding IS NOT NULL OR embedding_model IS NOT NULL OR embedded_at IS NOT NULL;

UPDATE documents
SET processing_status = 'chunked',
    embedding_profile = NULL,
    embedding_base_url = NULL,
    embedding_model = NULL,
    embedding_dimensions = NULL,
    indexed_at = NULL,
    failed_stage = NULL,
    last_error = NULL,
    updated_at = now()
WHERE embedding_profile IS NOT NULL;

DROP INDEX document_chunks_embedding_hnsw_idx;

ALTER TABLE documents
    DROP CONSTRAINT documents_embedding_dimensions_check,
    ADD CONSTRAINT documents_embedding_dimensions_check CHECK (embedding_dimensions = 1536);

ALTER TABLE document_chunks
    ALTER COLUMN embedding TYPE vector(1536) USING NULL::vector(1536);

CREATE INDEX document_chunks_embedding_hnsw_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);
