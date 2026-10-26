# AGENTS.md

## Project intent

This repository is a learning-oriented insurance AI agent built in small, reviewable milestones.
Keep each change scoped to the active milestone and preserve code readability for study.
The current milestone is M4: index persisted M3 chunks with Eino Embedding and pgvector, including batching, resumability, document locks, and idempotency.

## Architecture boundaries

- Go owns the CLI, validation, metadata, SHA-256, quality gates, deterministic chunking, JSON persistence, and PostgreSQL persistence.
- Python is limited to PDF text extraction through `pdfplumber`.
- Keep third-party parser details behind `internal/parser.Parser`.
- Keep PostgreSQL details behind the catalog and indexing repository boundaries so later Gin handlers and workers can reuse application services.
- Keep Eino types inside the Embedding adapter; application services use project-owned interfaces and context.Context.
- M4 uses Eino only for Embedding. Do not add retrieval, answer generation, Gin, Graph, Agent, Redis, Elasticsearch, or React work to M4.
- M4 defaults to locally served `Qwen/Qwen3-Embedding-0.6B` with 1024-dimensional vectors through an OpenAI-compatible endpoint.
- Indexing uses one locked database connection per document. Keep model requests outside transactions, preserve committed batches on failure, and coordinate replacement with the same document lock.
- Use the pinned Go 1.27.3 toolchain and dependency versions in go.mod/go.sum.
- Keep every milestone's DDL as a new versioned SQL file under `db/migrations/`; never edit, rename, delete, or squash an applied migration.
- OCR is out of scope for M2; reject unsupported scanned or low-quality documents clearly.

## PDF correctness rules

- Preserve physical PDF page numbers, including blank pages.
- Keep every chunk on one physical PDF page; carry clause metadata across page boundaries instead of merging pages.
- Never treat a successfully opened PDF as proof that extracted text is trustworthy.
- Reject documents with no extracted text or any Unicode replacement character (`U+FFFD`).
- Do not use an LLM to guess or repair insurance policy wording.
- Keep `testdata/sample.pdf` as the synthetic fixture.
- Keep both files under `testdata/real/` as offline regression fixtures; do not replace them casually.
- When parser behavior changes, verify Chinese order, replacement characters, and multi-column reading order.
- `quality.status: passed` means automated checks passed, not that legal content was manually certified.

## Local setup and validation

Install the pinned Python dependency before running the importer or tests:

```bash
python3 -m pip install -r requirements.txt
```

Run these checks for code changes:

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
```

PostgreSQL integration tests require a Docker-backed test database whose name contains `test` and a `TEST_DATABASE_URL` connection string. They truncate data and replay migrations; never point them at the demo database. Embedding unit tests use local HTTP servers, never live API credentials.

For parser or concurrency changes, also run `go test -race ./...`.
Parser changes must pass the two real-policy regression tests in `internal/parser/pdf_test.go`.
Chunker changes must pass the two real-policy regression tests in `internal/chunker/chunker_test.go`.

## Repository hygiene

- Treat existing uncommitted changes as user work and preserve them.
- Generated imports belong under `var/` and must not be committed.
- Build output belongs under `bin/` and must not be committed.
- Keep documentation consistent with behavior when commands, dependencies, or JSON fields change.
- Do not create a Git commit unless the user explicitly requests one.
