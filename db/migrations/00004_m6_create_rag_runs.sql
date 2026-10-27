-- +goose Up
CREATE TABLE rag_runs (
    id uuid PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('running', 'answered', 'abstained', 'failed')),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    CHECK ((status = 'running' AND finished_at IS NULL)
        OR (status <> 'running' AND finished_at IS NOT NULL)),
    CHECK (snapshot->>'run_id' = id::text AND snapshot->>'status' = status)
);

CREATE INDEX rag_runs_started_at_idx ON rag_runs (started_at);

-- +goose Down
DROP TABLE IF EXISTS rag_runs;
