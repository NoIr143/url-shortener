# url-shortener

A URL shortener backend written in Go. **This is a local-only prototype / proof-of-concept, not a deployed or production-ready service** — see [Status](#status) before assuming otherwise.

It validates a specific architecture end to end: a leased-numeric-range key allocator, a transactional exact-repeat-deduplicating mapping store, a status-aware redirect cache with safe failure fallback, input/attack-corpus validation, and a low-fidelity server-rendered creation UI — all runnable and tested locally against Docker-based substitutes for DynamoDB and Valkey, no AWS account required.

## Packages

| Package | What it does |
|---|---|
| `internal/base62` | Case-sensitive Base62 encode/decode for short keys |
| `internal/keyalloc` | Leased numeric ID ranges with a fencing token, backed by a DynamoDB counter item — collision-free under concurrency, safe at exhaustion |
| `internal/mapping` | Transactional short-key ↔ destination store with exact-repeat deduplication (byte equality, not just a digest match), plus backup/restore/reconciliation |
| `internal/cache` | Cache-aside redirect layer (Valkey) that degrades safely to the authoritative store on any cache failure, with explicit invalidation for fast propagation |
| `internal/api` | HTTP handlers: `POST /api/v1/urls` (create), `GET /{shortKey}` (resolve), destination/short-key validation, per-IP rate limiting |
| `internal/webui` | Server-rendered creation page (`GET /`, `POST /`) — no-JavaScript functional baseline, minimal progressive enhancement for clipboard copy |
| `cmd/server` | Runs the whole thing with an **in-memory** store, for demoing the UI/API without Docker |

## Running it

**UI/API demo (in-memory store, no Docker needed):**

```bash
go run ./cmd/server
# open http://localhost:8080/
```

**Unit tests** (no infrastructure required):

```bash
go test ./...
```

**Integration tests** (exercise the real key allocator, transactional mapping store, and cache against local DynamoDB/Valkey):

```bash
docker compose up -d
go test -tags=integration ./...
```

## API

| Route | Purpose |
|---|---|
| `POST /api/v1/urls` | Create a short URL. Body: `{"destination": "https://example.com/..."}`. Returns `201` (new) or `200` (exact byte-identical repeat, same mapping). |
| `GET /{shortKey}` | Resolve and `302` redirect. `404` if unknown, `400` if the key syntax is invalid. |

Errors are `application/problem+json` ([RFC 9457](https://www.rfc-editor.org/rfc/rfc9457)) with a stable `code` field and a `retryable` boolean:

```bash
curl -s -X POST localhost:8080/api/v1/urls \
  -H 'Content-Type: application/json' \
  -d '{"destination":"https://example.com/very/long/path"}'
# {"shortKey":"k1","shortUrl":"https://short.example/k1"}

curl -s -i localhost:8080/k1
# HTTP/1.1 302 Found
# Location: https://example.com/very/long/path
```

Short keys are **case-sensitive** — `Kx7fQ2b` and `kx7fq2b` are different keys. Rate limits are per-IP (10 creates/min, 100 resolves/min by default) and return `429` with `retryable: true`.

## Status

This is a working proof-of-concept, not a finished product:

- **No privileged operations** — suspend/reinstate/abuse-reporting endpoints don't exist yet.
- **`cmd/server` uses an in-memory store** — nothing persists across a restart. The real DynamoDB-backed store (`internal/mapping`) exists and is tested, but isn't wired into `cmd/server`.
- **No TLS, no deployment, no observability** — this runs on `localhost` only.
- **Rate limiting is in-process** — fine for one instance, not for a distributed deployment.

## License

No license file yet — treat as all-rights-reserved until one is added.
