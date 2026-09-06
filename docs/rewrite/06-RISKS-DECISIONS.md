# Risiko dan Keputusan

## Decision log

### D1 — Native Go sejak awal

**Status:** accepted.

Production runtime memakai `hypermeow` langsung. Node/Baileys dan Python tidak menjadi sidecar. Source lama hanya referensi fitur dan test scenarios.

Konsekuensi: fitur WhatsApp kompleks harus melewati capability gate; fitur yang tidak didukung ditunda atau mendapat fallback.

### D2 — Kontrak baru

**Status:** accepted.

Canonical Go domain types menjadi kontrak internal. HTTP API memakai version baru. Tidak ada kewajiban mempertahankan protocol Node/Python atau raw Baileys result.

### D3 — Stable TenantID

**Status:** accepted.

Internal identity tidak memakai absolute path atau JID. Tenant ID immutable dan semua dependency tenant-scoped.

### D4 — Greenfield persistence

**Status:** accepted.

Schema dirancang baru dan versioned sejak v1. Tidak ada importer SQLite/JSON/auth/config lama.

### D5 — Durable delivery di SQLite

**Status:** accepted.

Action receipts, inbox/outbox, scheduler leases, dan sub-agent delivery state memakai transactional tables dengan bounded retention.

### D6 — Fresh pairing

**Status:** accepted.

Setiap account melakukan pairing baru ke native device store. Tidak ada konversi Baileys auth.

### D7 — Control panel di binary utama

**Status:** proposed.

Default satu lifecycle dan embedded UI. Pisahkan hanya jika security boundary atau scaling membutuhkannya.

### D8 — Artifact deployment

**Status:** proposed.

Default release berupa reproducible artifact/container. Self-update via writable Git checkout bukan core feature v1.

## Risk register

| ID | Risiko | Severity | Mitigasi / Gate |
|---|---|---:|---|
| R1 | Hypermeow tidak mendukung fitur WhatsApp penting | Critical | Native capability matrix sebelum fitur masuk scope v1 |
| R2 | Session/pairing tidak stabil | Critical | Dedicated account soak, reconnect/logout tests, isolated device store |
| R3 | Duplicate side effect setelah crash | Critical | Transactional receipt/outbox, unknown-outcome reconciliation, fault injection |
| R4 | Cross-tenant data/path leak | Critical | Stable TenantID, scoped dependencies, containment and isolation tests |
| R5 | Message normalization salah untuk LID/wrappers | High | Sanitized native event fixtures and real-device matrix |
| R6 | LLM action melampaui permission | Critical | Typed schema and permission recheck immediately before side effect |
| R7 | LLM prompt/tool behavior buruk | High | Golden tests, deterministic fake provider, staged feature enablement |
| R8 | Sub-agent completion hilang/duplikat | High | Durable ownership, delivery checkpoints, restart/fault tests |
| R9 | Scheduler duplicate/late | High | Transactional leases, fake-clock/timezone tests, readiness gate |
| R10 | SSRF melalui media/download | High | DNS/redirect validation, IP policy, egress controls, size/time limits |
| R11 | Secrets plaintext/logged | High | Redaction, masked API, restricted files, secret provider, rotation |
| R12 | SQLite lock/WAL/corruption | High | Driver benchmark, pragmas, busy metrics, backup/restore/fault tests |
| R13 | Unbounded queue/cache/file growth | High | Hard bounds, retention, backpressure, quotas, soak tests |
| R14 | Interactive messages tidak portable/stabil | High | Capability flags, safe fallback, real-device tests |
| R15 | CGO deployment gagal | Medium | Build in target image; pure-Go SQLite ADR |
| R16 | Toolchain/dependency tidak reproducible | Medium | Track `go.sum`, pin versions/commits, artifact checksums |
| R17 | Windows path/reparse behavior | Medium | Canonical path containment and platform tests |
| R18 | Control panel attack surface | High | Fail-closed auth, rate limits, CSP, request limits, audit |
| R19 | Insufficient observability | Medium | Metrics, correlation IDs, readiness split, alerts |
| R20 | Source reference membatasi desain baru | Medium | Treat source as feature inventory, require Go ADR for architecture |

## Security requirements

### URL/media

- Allow intended schemes only.
- Resolve host and block loopback/private/link-local/metadata ranges.
- Revalidate every redirect and resolved address.
- Stream with strict timeout and byte limits.
- Detect MIME from content.
- Store using generated names under tenant root.

### Tokens and activation

- Generate with `crypto/rand`.
- Constant-time comparison.
- Rate-limit auth, pairing, reconnect, and resource-heavy endpoints.
- Do not accept secrets in query params.
- Support rotation without logging old/new values.

### Filesystem

- Reject traversal, unintended absolute paths, null bytes, and symlink/reparse escape.
- Validate source and destination after canonicalization.
- Atomic writes for config/catalog/state that is not transactional.
- Quotas for media, output files, audit, and temp data.

### LLM/tool safety

- Validate every model action against typed schema.
- Re-evaluate permission and tenant scope immediately before execution.
- Explicit allow policies for commands, HTML, URLs, and sub-agent files.
- Redact prompt/media logs by default.
- Bound action count, attachment count/size, and recursion.

## Open decisions

| Decision | Wajib ditutup pada |
|---|---|
| O1 — SQLite driver | Milestone 0 |
| O2 — single or split tenant DB | Milestone 0 |
| O3 — single binary or service split | Milestone 0 |
| O4 — config hot reload | Milestone 0 |
| O5 — context ID format/retention | Milestone 0 sebelum message model |
| O6 — unsupported interactive fallback | Milestone 3 capability review |
| O7 — control panel auth setup | Milestone 0 |
| O8 — artifact/container/Pterodactyl target | Milestone 0, finalized Milestone 9 |

### O1 — SQLite driver

Bandingkan `mattn/go-sqlite3` dan pure-Go alternative untuk:

- target build support;
- WAL and backup behavior;
- concurrency/performance;
- binary reproducibility;
- compatibility dengan hypermeow device store.

### O2 — Database topology

Pilih satu DB per tenant untuk atomic transactions atau split stores untuk failure/operational isolation. Keputusan dibuat sebelum schema v1.

### O3 — Process topology

Default single binary. Service split memerlukan authenticated transport, durable cross-process queue, dan independent lifecycle; jangan dipilih tanpa operational need.

### O4 — Config reload

Klasifikasikan startup-only versus reloadable fields. Reload memakai immutable validated snapshot; failure mempertahankan last-known-good config.

### O5 — Context IDs

Gunakan opaque stable IDs atau bounded short IDs. Tentukan persistence, collision, expiry, and quoted-message lookup semantics sebelum API/model dibekukan.

### O6 — Interactive fallback

Untuk fitur native yang unsupported, pilih text fallback, document/image rendering, atau deferred release. Jangan mengirim malformed raw protobuf.

### O7 — Control panel auth

Pilih initial bootstrap flow, token hashing/storage, session versus bearer auth, CSRF strategy, dan recovery procedure.

### O8 — Deployment target

Pilih target primary agar CGO, static assets, signals, filesystem permissions, health checks, dan backup path diuji pada environment nyata.

## ADR template

```text
Context
Options
Decision
Consequences
Security impact
Operational impact
Evidence/tests
```

## Stop conditions

Hentikan phase/release bila:

- required capability hanya lulus happy path;
- native session tidak pulih setelah restart/network loss;
- durable state machine belum lulus crash injection;
- side-effect outcome tidak dapat direconcile;
- tenant identity/path containment belum terjamin;
- secret/auth setup tidak fail-closed;
- resource limits tidak diterapkan;
- build tidak reproducible pada deployment target.
