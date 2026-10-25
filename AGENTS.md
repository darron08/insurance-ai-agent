# AGENTS.md

## Project intent

This repository is a learning-oriented insurance AI agent built in small, reviewable milestones.
Keep each change scoped to the active milestone and preserve code readability for study.
The current milestone is M2: split an imported page-level JSON document into inspectable clause-aware chunks.

## Architecture boundaries

- Go owns the CLI, validation, metadata, SHA-256, quality gates, deterministic chunking, and JSON persistence.
- Python is limited to PDF text extraction through `pdfplumber`.
- Keep third-party parser details behind `internal/parser.Parser`.
- Do not add database, embedding, retrieval, LLM, Gin, or React work to M2 changes.
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

For parser or concurrency changes, also run `go test -race ./...`.
Parser changes must pass the two real-policy regression tests in `internal/parser/pdf_test.go`.
Chunker changes must pass the two real-policy regression tests in `internal/chunker/chunker_test.go`.

## Repository hygiene

- Treat existing uncommitted changes as user work and preserve them.
- Generated imports belong under `var/` and must not be committed.
- Build output belongs under `bin/` and must not be committed.
- Keep documentation consistent with behavior when commands, dependencies, or JSON fields change.
- Do not create a Git commit unless the user explicitly requests one.
