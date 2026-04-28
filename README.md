# ortholog-artifact-store

Content-addressed blob store for the [Ortholog](https://github.com/clearcompass-ai/ortholog-sdk) decentralized credentialing protocol.

A small HTTP service that stores ciphertext blobs keyed by CID against an **object store**: GCS, RustFS, or any future provider that satisfies the `BackendProvider` interface. Optional mirroring composes any pair of object stores. It never computes CIDs, never encrypts, never holds keys — that's the SDK's job. It stores bytes and gives them back.

---

## Quick start

```bash
# In-memory backend, no dependencies
ARTIFACT_BACKEND=memory go run ./cmd/artifact-store

# GCS-backed
ARTIFACT_BACKEND=gcs ARTIFACT_BUCKET=my-bucket go run ./cmd/artifact-store

# RustFS-backed (S3 wire protocol)
ARTIFACT_BACKEND=rustfs \
  ARTIFACT_ENDPOINT=http://rustfs.internal:9000 \
  ARTIFACT_BUCKET=artifacts \
  ARTIFACT_PATH_STYLE=true \
  go run ./cmd/artifact-store
```

The service listens on `:8082` by default. Override with `ARTIFACT_LISTEN_ADDR`.

---

## HTTP API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/artifacts` | Push bytes (header `X-Artifact-CID: <cid>`) |
| `GET` | `/v1/artifacts/{cid}` | Fetch raw bytes |
| `HEAD` | `/v1/artifacts/{cid}` | Existence check |
| `DELETE` | `/v1/artifacts/{cid}` | Delete |
| `POST` | `/v1/artifacts/{cid}/pin` | Pin against GC |
| `GET` | `/v1/artifacts/{cid}/resolve` | Retrieve a `RetrievalCredential` (signed URL or direct in-memory) |
| `GET` | `/healthz` | Backend reachability |

`?expiry=<seconds>` on `/resolve` overrides the default signed-URL lifetime.

### Resolve methods

Every supported object-store backend returns a `signed_url` retrieval
credential with a bounded TTL. The in-memory reference returns `direct`
(no expiry — the URL is the CID itself; intended for tests only).

```json
// GCS / RustFS production response
{"method": "signed_url", "url": "https://...", "expiry": "2026-04-16T17:00:00Z"}

// In-memory test response
{"method": "direct", "url": "sha256:abc…", "expiry": null}
```

Signed URLs can be scoped in time and revoked by rotating the cloud
signing credentials. The artifact store mints them; the cloud edge
verifies them.

---

## Configuration

All settings come from environment variables. Only `ARTIFACT_BACKEND` has a meaningful default (`memory`).

| Variable | Default | Notes |
|---|---|---|
| `ARTIFACT_BACKEND` | `memory` | `gcs`, `rustfs`, `memory` |
| `ARTIFACT_BUCKET` | `ortholog-artifacts` | GCS/RustFS |
| `ARTIFACT_ENDPOINT` | — | RustFS endpoint URL |
| `ARTIFACT_REGION` | `us-east-1` | RustFS only (SigV4 region label) |
| `ARTIFACT_PATH_STYLE` | `false` | RustFS path-style addressing |
| `ARTIFACT_PREFIX` | — | Object key prefix |
| `ARTIFACT_MIRROR_BACKEND` | — | Secondary object store (`gcs`/`rustfs`) |
| `ARTIFACT_MIRROR_MODE` | `sync` | Synchronous double-write (only mode) |
| `ARTIFACT_VERIFY_ON_PUSH` | `true` | **Do not disable in production.** Validates the body hashes (under the CID's algorithm) to the CID digest server-side. |
| `ARTIFACT_RESOLVE_EXPIRY` | `3600` | Default signed-URL lifetime (seconds) |
| `ARTIFACT_LISTEN_ADDR` | `:8082` | |
| `ARTIFACT_MAX_BODY_SIZE` | `67108864` | 64 MB. Push requests over this return 413. |

### Mirroring

`ARTIFACT_MIRROR_BACKEND=gcs` (or `rustfs`) layers a second object store behind the primary. The only supported mode is `sync`: every Push writes both backends. Primary failure is fatal; mirror failure is logged but non-fatal. Fetches fall back to the mirror on primary error. The composition is symmetric — any pair of object stores composes (GCS+RustFS for cross-provider redundancy, GCS+GCS for cross-region, etc.).

---

## Security

### VerifyOnPush (required in production)

**`ARTIFACT_VERIFY_ON_PUSH=true` is not optional in production.** When the
server computes SHA-256 of the received body and compares it to the claimed
CID, it catches truncated uploads, bit flips in transit, and malicious clients
sending mismatched bytes. Disabling verification allows any client to store
arbitrary bytes under any CID — silent data corruption that propagates across
the protocol with no recovery path.

Enforcement behavior:

- `VerifyOnPush=false`: a WARN log fires at startup with
  `event=artifact.config.verify_on_push_disabled`.
- `VerifyOnPush=false` AND `ORTHOLOG_ENV=production`: the warning re-emits
  every 60 seconds for the lifetime of the process. The misconfiguration
  is impossible to miss in a log pipeline.
- `VerifyOnPush=false` AND `ORTHOLOG_ENV != production` (dev/staging):
  one warning at startup, no repeats. Verification is frequently disabled
  in dev to exercise the corrupt-bytes path; repeating the warning would
  train operators to ignore it.

**Deployment templates must set `ARTIFACT_VERIFY_ON_PUSH=true` in production.
Pull requests that disable it in production configs should be treated as
security violations and blocked.**

### Upload token authentication (optional)

The store supports an optional `X-Upload-Token` header for authenticated
uploads. Policy is controlled by `ARTIFACT_REQUIRE_UPLOAD_TOKEN`:

| Policy | Behavior | Use when |
|---|---|---|
| `off` (default) | No token check. Store accepts any push. | Single-cluster deployments where network segmentation prevents external access. |
| `optional` | If `X-Upload-Token` is present, it must verify; if absent, accept. | Rollout / migration: issue tokens to some clients, observe, then escalate. |
| `required` | Every push must carry a valid token or returns 401. | Multi-tenant, shared-network, or untrusted-client deployments. |

Token format: `base64url(payload_json).base64url(ed25519_signature)`.
Payload fields: `cid`, `size`, `exp` (required), `iat`, `kid` (REQUIRED
when the store is loaded with multiple operator pubkeys; falls back to
the empty-kid slot for single-key deployments).

Operator pubkeys are kid-keyed at the store. The verifier dispatches
on the token's `kid` claim before checking the signature, which makes
operator key rotation a configuration swap rather than a code change.
Two equivalent loaders:

| Env var | Format | Use when |
|---|---|---|
| `ARTIFACT_OPERATOR_PUBKEYS` | `kid1:<encoded>,kid2:<encoded>` (PEM/base64/hex auto-detected) | Inline config, small deployments, dev |
| `ARTIFACT_OPERATOR_PUBKEYS_DIR` | Directory of `<kid>.pem` files | Secret-mount workflows where each key rotates as its own file |

Single-key deployments register a key under the empty kid (`:<encoded>`
inline, or `.pem` named `''`-ish in a dir of one) and mint tokens
without a `kid` claim. Rotation: load both old and new keys for the
window, retire the old kid after in-flight tokens expire. The Ed25519
signature itself is verified through the SDK's audited
`crypto/signatures.VerifyEd25519` primitive — the same one every
log-side signature check uses.

Every rejected push emits a structured audit log with a stable
`event`/`reason` pair (see "Audit logging" below).

### Audit logging

Every push rejection emits a WARN-level `slog` record with:

```
event  = "artifact.push.rejected"
reason = one of: size_exceeded | cid_mismatch |
                 token_required_missing | token_invalid |
                 token_unknown_kid |
                 token_expired | token_cid_mismatch |
                 token_size_mismatch | token_malformed |
                 missing_cid_header | invalid_cid_header |
                 read_body_error | backend_error
```

Plus context attributes: `claimed_cid`, `remote_addr`, `received_size`,
`max_body_size`, `computed_digest` (on cid_mismatch), `claimed_digest`,
`operator_token_kid` (when a token was provided). Send these to your SIEM
and alert on `reason ∈ {cid_mismatch, size_exceeded, token_invalid,
token_expired}` — under normal operation these never fire because the
upstream operator's quota and signing pipeline catches them first.

### Notes

- The service does not hold encryption keys. All ciphertext is opaque to the store.
- Every supported backend is an object store with bounded-TTL signed URLs. Access is controlled by URL scope and rotated by rotating the cloud signing credentials.

---

## Development

```bash
make test                    # Wave 1: unit + HTTP-mocked tests with -race
make audit-v775-consumer     # v7.75 SDK alignment gate (build + vet + Wave 1)
make coverage                # HTML coverage report
make lint                    # vet + staticcheck
make flake                   # run the suite 50× to detect flakes
make test-all                # everything per-PR CI runs
```

`make test` completes in about 5 seconds.

### Architecture

- `api/` — HTTP handlers, one per route, plus `server.go` that wires them into a `ServeMux`.
- `backends/` — `BackendProvider` implementations: `gcs.go`, `rustfs.go`, plus the `MirroredStore` decorator and the `InMemoryBackend` reference impl.
- `config/` — env-var loading and validation.
- `cmd/artifact-store/` — the `main` that ties it together.
- `internal/testutil/` — httptest fakes (`GCSFake`, `S3Fake`), slog capture, goleak shim, known CID vectors.
- `tests/conformance/` — the backend contract suite, run against every `BackendProvider`.

### Testing

See [**docs/TESTING.md**](docs/TESTING.md) for the full strategy:
the five-layer pyramid, what each layer catches, what gates exist,
how Wave 2 (testcontainers) and Wave 3 (staging) plug in.

See [**docs/ADDING_A_BACKEND.md**](docs/ADDING_A_BACKEND.md) for the
step-by-step checklist when adding a new backend implementation.

---

## License

TBD.
