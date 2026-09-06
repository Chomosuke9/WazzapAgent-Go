# Risiko dan Keputusan

## Decision log

### D1 — Sidecar dahulu, native WhatsApp terakhir

**Status:** accepted untuk rencana awal.

Go mengganti bridge/control/data behavior lebih dulu, sementara Node/Baileys tetap menjadi WhatsApp adapter. Native `hypermeow` hanya dipakai production setelah capability spike dan canary.

Alasan: Baileys behavior mencakup auth, LID/JID, message wrappers, native flows, raw relay, group metadata, media crypto, pairing, dan reconnect. API similarity tidak membuktikan wire/behavior parity.

### D2 — Protocol dibuat canonical dan generated

**Status:** accepted.

Buat schema machine-readable baru dari runtime inventory, lalu generate/validate Go, TS, Python, dan fixtures. `CONTRACT.md`, TS union, atau Python dataclasses tidak lagi berdiri sendiri sebagai source of truth.

### D3 — Stable TenantID

**Status:** accepted.

Internal identity tidak memakai absolute filesystem path. `folderPath` tetap dipertahankan di protocol v2 adapter selama migration.

### D4 — Satu migration owner

**Status:** accepted.

Go mengambil schema ownership hanya ketika writers lama dihentikan. Repositories tidak menjalankan DDL.

### D5 — Pertahankan split DB saat behavior cutover

**Status:** accepted.

Konsolidasi database ditunda agar rollback sederhana dan scope migration tidak bercampur dengan rewrite behavior.

### D6 — SQLite untuk durable delivery

**Status:** accepted.

Action receipts, inbox/outbox, scheduler claims, dan sub-agent delivery state dipindahkan dari JSON ke transactional tables dengan retention. Legacy JSON importer tetap tersedia selama rollback window.

### D7 — Control panel di binary utama

**Status:** proposed.

Alasan: satu lifecycle, typed services, dan embed assets. Pisahkan hanya jika security/deployment boundary membutuhkan process terpisah.

### D8 — Self-update bukan core

**Status:** proposed.

Git fast-forward updater dipertahankan sebagai optional deployment adapter. Reproducible artifact/container deployment menjadi default target.

## Risk register

| ID | Risiko | Severity | Mitigasi / Gate |
|---|---|---:|---|
| R1 | Native Go tidak parity dengan Baileys | Critical | Sidecar default; capability matrix, real-device canary, adapter rollback |
| R2 | Auth state tidak dapat dimigrasi | Critical | Jangan overwrite auth; re-pair plan; backup; importer hanya setelah proof |
| R3 | Duplicate side effects setelah crash/retry | Critical | Transactional claim/receipt, fingerprint, reconciliation state, fault tests |
| R4 | Node/Python/Go concurrent schema writes | Critical | Explicit ownership cutover, migration lock, read-only Go first |
| R5 | Protocol drift menghasilkan silent field loss | Critical | Generated schema, shared fixtures, unknown-field telemetry |
| R6 | Cross-tenant data/path leak | Critical | Stable TenantID, scoped dependencies, containment validation, isolation tests |
| R7 | LLM behavior/prompt drift | High | Golden prompt/tool/action replay, shadow mode, intentional-delta ADR |
| R8 | Sub-agent completion hilang/duplikat | High | Durable ownership, delivery checkpoints, restart/fault tests |
| R9 | Scheduler semantics berubah | High | Separate one-shot/daily policies, leases, fake-clock tests |
| R10 | Control panel API/UI break | High | Black-box differential tests, compatibility response shapes |
| R11 | SSRF melalui URL media/download | High | URL policy, DNS/redirect validation, network egress controls |
| R12 | Secrets plaintext/logged | High | Redaction, masked API, permissions, secret provider, rotation |
| R13 | SQLite lock/WAL/corruption behavior berbeda | High | Same pragmas, busy metrics, copied fixture tests, backup/restore |
| R14 | In-memory IDs hilang setelah restart | High | Preserve semantics or persist via explicit compatibility decision |
| R15 | Unbounded cache/queue/file growth | High | Bounds, retention, backpressure, metrics, soak tests |
| R16 | Interactive messages berubah/tidak didukung | High | Capability tests, canonical fallback, sidecar retention |
| R17 | CGO deployment gagal | Medium | Build in target image; consider pure-Go SQLite ADR |
| R18 | Go/dependency versions tidak reproducible | Medium | Valid toolchain, track `go.sum`, pin actual commits/releases |
| R19 | Windows path/case/reparse behavior | Medium | Canonical boundary handling and platform tests |
| R20 | Git self-update incompatible dengan binary deployment | Medium | Optional adapter; signed/pinned release artifacts |
| R21 | Activation/memory IDs memakai weak RNG | Medium | `crypto/rand`, collision/retry tests |
| R22 | Insufficient observability blocks diagnosis | Medium | Metrics, request correlation, readiness split, audit |

## Security changes required

### URL/media

- Allow only intended schemes.
- Resolve host and block loopback/private/link-local/metadata ranges.
- Revalidate every redirect and resolved address.
- Apply download size/time limits and streaming.
- Detect MIME from content; do not trust extension/header.
- Store under tenant-controlled root with generated filenames.

### Tokens and activation

- Generate account/control/activation identifiers with `crypto/rand`.
- Use constant-time token comparison.
- Rate-limit auth, pairing, reconnect, and resource-heavy endpoints.
- Do not pass secrets in query params for new APIs; retain legacy only behind deprecation.

### Filesystem

- Reject traversal, absolute path where not allowed, null bytes, symlink/reparse escape.
- Validate source and destination after canonicalization.
- Atomic write + fsync strategy for state files where needed.
- Enforce file count/size quotas for media and sub-agent outputs.

### LLM/tool safety

- Validate model-generated action against typed schema and current permissions.
- Do not let prompt content bypass owner/admin/activation checks.
- Require explicit allow policy for commands, HTML, downloads, and sub-agent file access.
- Redact prompt/media logging by default.

## Open decisions

| Decision | Wajib ditutup pada |
|---|---|
| O1 — sidecar atau native | Milestone 8 sebelum Milestone 9 |
| O2 — SQLite driver | Milestone 1 |
| O3 — process topology | Milestone 1 untuk package boundary; konfirmasi deployment di Milestone 10 |
| O4 — DB consolidation | Setelah Milestone 7 stabil |
| O5 — raw action result | Milestone 2 |
| O6 — context ID durability | Milestone 5 sebelum history dibekukan |
| O7 — config hot reload | Milestone 1 |
| O8 — update mechanism | Milestone 10 sebelum deployment release |

### O1 — Permanent sidecar atau full native Go?

Owner memilih setelah Milestone 8 evidence. Kriteria bukan jumlah kode Go, tetapi reliability dan feature parity.

### O2 — SQLite driver

Current `mattn/go-sqlite3` sudah tersedia tetapi membutuhkan CGO. Bandingkan:

- deployment target support;
- WAL/backup behavior;
- performance/concurrency;
- binary reproducibility;
- compatibility dengan hypermeow store.

### O3 — Single binary versus service split

Default single Go service. Pisahkan control plane/worker hanya bila scaling atau security isolation membutuhkannya. Tenant worker multi-process memerlukan durable queue dan scheduler leases lebih awal.

### O4 — Database consolidation

Tentukan setelah Go menjadi sole writer dan backup/restore stabil. Jangan gabungkan hanya demi kerapian.

### O5 — Raw action result compatibility

Legacy `send_buttons`, carousel, dan copy-code dapat mengembalikan raw Baileys message object. Pilih:

- freeze normalized portable result dan bump protocol; atau
- compatibility serializer khusus sidecar v2.

Rekomendasi: normalized result untuk protocol baru, compatibility serializer selama v2.

### O6 — History/context ID durability

Tentukan apakah transient six-digit IDs tetap reset/lost on restart atau dipersist. Persisting memperbaiki UX tetapi merupakan behavior change dan membutuhkan retention/collision policy.

### O7 — Config hot reload

Klasifikasikan tiap field sebagai startup-only atau hot-reload. Implement immutable config snapshot atomik; partial reload failure mempertahankan last-known-good state.

### O8 — Update mechanism

Pilih artifact/container/system package/Pterodactyl flow. Jika Git updater dipertahankan, jangan izinkan core process mengubah source tanpa signature/checksum dan rollback policy.

## ADR template

Setiap open decision ditutup dengan ADR berisi:

```text
Context
Options
Decision
Consequences
Compatibility impact
Migration plan
Rollback plan
Evidence/tests
```

## Stop conditions

Hentikan rollout dan kembali ke phase sebelumnya bila:

- canonical baseline belum stabil;
- behavior hanya diverifikasi lewat happy-path manual test;
- schema memiliki lebih dari satu writer;
- native auth/session tidak memiliki rollback;
- durable state machine belum lulus crash injection;
- side effect outcome tidak dapat direconcile;
- tenant identity/path containment belum terjamin;
- build tidak reproducible di production target.
