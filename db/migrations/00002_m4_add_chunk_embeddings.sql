-- +goose Up
ALTER TABLE documents
    ADD COLUMN embedding_profile text,
    ADD COLUMN embedding_base_url text,
    ADD COLUMN embedding_model text,
    ADD COLUMN embedding_dimensions integer CHECK (embedding_dimensions = 1536),
    ADD COLUMN indexed_at timestamptz,
    ADD CONSTRAINT documents_embedding_profile_check CHECK (
        (embedding_profile IS NULL AND embedding_base_url IS NULL AND embedding_model IS NULL AND embedding_dimensions IS NULL AND indexed_at IS NULL)
        OR (embedding_profile IS NOT NULL AND embedding_base_url IS NOT NULL AND embedding_model IS NOT NULL AND embedding_dimensions IS NOT NULL)
    );

ALTER TABLE document_chunks
    ADD COLUMN embedding vector(1536),
    ADD COLUMN embedding_model text,
    ADD COLUMN embedded_at timestamptz,
    ADD CONSTRAINT document_chunks_embedding_check CHECK (
        (embedding IS NULL AND embedding_model IS NULL AND embedded_at IS NULL)
        OR (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedded_at IS NOT NULL AND vector_norm(embedding) > 0)
    );

CREATE INDEX document_chunks_embedding_hnsw_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP INDEX document_chunks_embedding_hnsw_idx;
ALTER TABLE document_chunks
    DROP CONSTRAINT document_chunks_embedding_check,
    DROP COLUMN embedded_at,
    DROP COLUMN embedding_model,
    DROP COLUMN embedding;
-- A rolled-back M4 schema must not advertise an index that no longer exists.
UPDATE documents SET processing_status = 'chunked', failed_stage = NULL, last_error = NULL, updated_at = now()
    WHERE embedding_profile IS NOT NULL;
ALTER TABLE documents
    DROP CONSTRAINT documents_embedding_profile_check,
    DROP COLUMN indexed_at,
    DROP COLUMN embedding_dimensions,
    DROP COLUMN embedding_model,
    DROP COLUMN embedding_base_url,
    DROP COLUMN embedding_profile;
