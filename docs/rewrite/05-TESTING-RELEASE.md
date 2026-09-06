# Pengujian dan Release

## Quality gates

Setiap perubahan Go menjalankan:

```text
gofmt check
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
```

Release CI menambah `govulncheck`, integration tests, real-device smoke, load/soak gates, dan build di target environment. `go-sqlite3` membutuhkan validasi CGO toolchain bila tetap dipilih.

## Test pyramid

### Unit

- config defaults, validation, reload, redaction;
- tenant/path isolation;
- canonical message/action/error validation;
- message unwrap, mentions, quoted, location;
- permissions, activation, moderation;
- history formatting and hydration;
- debounce, burst, stale, cancellation;
- LLM schemas and output parsing;
- action receipt state machine;
- scheduler calculation and leases;
- sub-agent transitions and file validation.

Gunakan fake clock, fake LLM provider, fake gateway, dan temporary data roots.

### Database

- fresh schema v1;
- each migration created after v1;
- checksum mismatch and interrupted migration;
- NULL/default/time conversion;
- concurrent readers/writers and busy timeout;
- WAL checkpoint and crash recovery;
- transaction rollback;
- two-tenant isolation;
- receipt/outbox/job/sub-agent durability;
- backup/restore.

### Message golden tests

Simpan sanitized native event fixtures untuk:

- DM dan group text;
- quoted/reply and mentions;
- LID/phone JID forms;
- image/video/audio/document/sticker;
- view-once, ephemeral, edited wrappers;
- reactions and group events;
- malformed and unsupported payloads.

Golden output menggunakan canonical Go message model baru, bukan legacy protocol frames.

### Agent replay

Corpus sintetis mencakup:

- group trigger modes;
- bursts and stale payloads;
- media and quoted media;
- owner/admin/user permissions;
- activation and mute states;
- LLM primary failure/fallback/malformed tool call;
- sub-agent active/steering/completion;
- scheduled and direct invoke.

Bandingkan deterministic expected:

- history;
- LLM requests/tools;
- LLM1 decision;
- action intents/order;
- state writes;
- stable errors and metrics.

### API/UI

- initial token setup and fail-closed state;
- login/auth rate limiting;
- account pair/reconnect/logout/delete-data;
- settings/memory/models/activation/moderation/stickers;
- jobs and sub-agent admin;
- secret masking;
- audit, health, metrics, backup/restore;
- invalid IDs, traversal, null bytes, oversized body, duplicate requests;
- browser CSP and security headers.

### Native WhatsApp integration

Gunakan dedicated test accounts:

- QR dan pairing number;
- session reconnect setelah process/host restart;
- DM/group send and receive;
- reply/quote/mention/reaction/delete/read/presence;
- media and stickers;
- participant role and kick;
- interactive capabilities;
- logout/device removal;
- network partition, stream replacement, rate limit;
- multiple active accounts.

Tests yang dapat mengirim pesan harus memakai explicit test allowlist agar tidak menyentuh chat nyata.

### Fault injection

Matikan process pada titik:

- setelah inbound dedup claim;
- setelah action claim sebelum send;
- setelah send sebelum receipt completion;
- setelah webhook persisted sebelum response;
- saat output partially written;
- setelah job lease;
- saat WAL active;
- saat account catalog/config atomic write;
- saat reconnect and outbox drain.

Expected state ditetapkan sebagai retry, no-retry, reconciliation, atau dead-letter.

### Load dan soak

- concurrent tenants and chats;
- burst pada chat sama;
- slow/failing LLM;
- large and slow media;
- reconnect storm;
- large sub-agent outputs;
- control-panel mutations saat agent aktif;
- 24+ hour cache/goroutine/WAL/file growth test.

Track goroutines, heap, queue depth/age, DB busy, dropped events, action/LLM latency, reconnect count, dan disk growth.

## Security tests

- auth fail-closed dan constant-time token comparison;
- cryptographic token/activation generation;
- SSRF blocking untuk loopback/private/link-local/metadata destinations;
- DNS rebinding dan redirect revalidation;
- path traversal, symlink/reparse escape;
- MIME/content-size mismatch and decompression limits;
- malicious HTML rendering input;
- SQL parameterization;
- prompt/context injection guard;
- webhook replay/forgery;
- secret redaction in logs/API/errors;
- request/body/frame/file/concurrency limits;
- permission recheck immediately before model-generated side effect.

## Release stages

### Stage A — Local offline

Fake gateway dan fake LLM. Seluruh unit/database/golden tests lulus.

### Stage B — Dedicated WhatsApp account

Fresh pair satu account test. Jalankan native capability matrix dan fault tests.

### Stage C — Multi-account test

Fresh pair minimal dua accounts. Validasi isolation, concurrent traffic, and independent reconnect.

### Stage D — Release candidate soak

Jalankan full stack termasuk control panel, jobs, sub-agent, LLM sandbox, backup/restore, dan host restart.

### Stage E — New production deployment

Install ke data directory kosong, buat secrets baru, pair accounts baru, dan aktifkan feature flags bertahap.

## Pre-release checklist

- [ ] Scope v1 frozen; deferred features terdokumentasi.
- [ ] Source commit, module sums, toolchain, build image, dan checksums tercatat.
- [ ] Unit, race, DB, native integration, security, and soak gates pass.
- [ ] Dedicated test accounts lulus capability matrix.
- [ ] Fresh data directory startup teruji.
- [ ] Initial secret setup dan rotation workflow teruji.
- [ ] Backup/restore and schema upgrade drill berhasil.
- [ ] Resource limits and retention configured.
- [ ] Logs, metrics, alerts, and operator access ready.
- [ ] Test-recipient allowlist enabled selama verification.
- [ ] Node/Python tidak diperlukan di release environment.

## Deployment runbook

1. Provision fresh data directory dan least-privilege service account.
2. Install verified binary/artifact.
3. Generate new control, webhook, direct-invoke, dan provider secrets.
4. Start service dan verify liveness/readiness.
5. Create tenant/account dari control panel/API.
6. Pair WhatsApp account baru.
7. Verify account status `open`.
8. Run allowlisted DM/group/action/media probes.
9. Enable agent, jobs, dan sub-agent features bertahap.
10. Observe error, queue, DB, reconnect, and duplicate metrics.
11. Take first consistent backup and test restore separately.

## Release stop triggers

Stop rollout dan disable affected feature/account bila:

- cross-tenant state/path leak;
- duplicate destructive action;
- sustained incoming/outgoing loss;
- database integrity failure;
- uncontrolled reconnect/pair loop;
- sub-agent completion loss;
- auth/session corruption;
- unbounded queue/disk/goroutine growth;
- required security control unavailable.

Karena project baru, recovery dilakukan dengan memperbaiki atau menonaktifkan release baru dan memulihkan backup aplikasi baru. Tidak ada rollback ke database/auth/runtime project lama.

## Production acceptance

- Zero tenant isolation violation.
- No unreconciled duplicate destructive action.
- Incoming/outgoing reliability memenuhi SLO v1.
- p95/p99 action dan agent latency memenuhi budget.
- DB busy/WAL/disk growth stabil.
- Scheduler lateness dalam tolerance.
- Semua durable sub-agent completions delivered atau visible di retry/dead-letter.
- Process dan host restart pulih tanpa manual DB repair.
- Operators dapat backup, restore, inspect readiness, rotate secrets, dan disable fitur bermasalah.
