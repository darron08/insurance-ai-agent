-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE insurance_products (
    id text PRIMARY KEY,
    name text,
    insurer text,
    product_type text,
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'inactive')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE documents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id text NOT NULL REFERENCES insurance_products(id),
    name text NOT NULL,
    version text NOT NULL DEFAULT '',
    source_url text NOT NULL,
    source_file_path text NOT NULL,
    source_sha256 character(64) NOT NULL
        CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    source_imported_at timestamptz NOT NULL,
    source_parser text NOT NULL,
    total_pages integer NOT NULL CHECK (total_pages > 0),
    chunking_strategy text NOT NULL,
    max_characters integer NOT NULL CHECK (max_characters > 0),
    overlap_characters integer NOT NULL CHECK (
        overlap_characters >= 0 AND overlap_characters < max_characters
    ),
    total_chunks integer NOT NULL CHECK (total_chunks > 0),
    processing_status text NOT NULL DEFAULT 'chunked'
        CHECK (processing_status IN ('chunked', 'embedding', 'ready', 'failed')),
    failed_stage text,
    last_error text,
    chunked_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (product_id, version, source_sha256)
);

CREATE INDEX documents_product_version_idx
    ON documents (product_id, version);
CREATE INDEX documents_processing_status_idx
    ON documents (processing_status);

CREATE TABLE document_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index integer NOT NULL CHECK (chunk_index > 0),
    page_number integer NOT NULL CHECK (page_number > 0),
    section_title text NOT NULL DEFAULT '',
    clause_id text NOT NULL DEFAULT '',
    part_index integer NOT NULL CHECK (part_index > 0),
    part_count integer NOT NULL CHECK (part_count > 0 AND part_index <= part_count),
    character_count integer NOT NULL CHECK (character_count > 0),
    text text NOT NULL CHECK (length(btrim(text)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (document_id, chunk_index)
);

CREATE INDEX document_chunks_document_page_idx
    ON document_chunks (document_id, page_number, chunk_index);
CREATE INDEX document_chunks_document_clause_idx
    ON document_chunks (document_id, clause_id)
    WHERE clause_id <> '';

-- +goose Down
DROP TABLE IF EXISTS document_chunks;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS insurance_products;
DROP EXTENSION IF EXISTS vector;
