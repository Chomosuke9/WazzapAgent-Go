# Pengujian, Cutover, dan Rollback

## Quality gates

Setiap PR Go harus menjalankan format, unit tests, vet, dan race tests yang relevan. CI release menambah integration, compatibility, security, dan cross-platform build.

Baseline commands target:

```text
gofmt check
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
```

Tambahkan vulnerability scan (`govulncheck`) setelah toolchain dipin. Karena `go-sqlite3` memakai CGO, build production wajib diuji di image/host target.

## Test pyramid

### Unit

- config defaults/validation/redaction;
- path/tenant normalization;
- protocol validation and error mapping;
- request correlation and late ACK;
- message unwrap/normalization/mentions/quoted/location;
- permissions and activation;
- history formatting and hydration;
- debounce/burst/stale/prefix interrupt;
- LLM tool schemas and output parsing;
- action intent validation;
- scheduler next-fire and cancellation;
- receipt fingerprint/state machine;
- sub-agent state transitions and path validation.

Gunakan fake clock, fake LLM provider, fake gateway, dan temporary tenant roots.

### Golden/contract

Canonical fixture per frame:

- happy path, omitted optional fields, explicit nulls;
- invalid discriminator/required field/type/enum;
- every action result and error code;
- all control events with top-level fields;
- lazy attachment shape;
- protocol version mismatch.

Fixture yang sama dipakai TS, Python, dan Go. Test harus membandingkan semantic object dan canonical encoded representation jika ordering relevan.

### Database

- fresh schema;
- each historical schema fixture;
- legacy `bot.db` import;
- split DB/subagent migration;
- NULL/default/time conversion;
- concurrent readers and busy timeout;
- crash/WAL recovery;
- migration lock and checksum mismatch;
- JSON receipt/tracker importer;
- two-tenant isolation;
- backup/restore.

Jangan merusak fixture utama; copy ke temporary directory per test.

### Agent replay

Bangun corpus event teranonimisasi/sintetis:

- DM dan group modes;
- mention/reply/name/prefix/tag-all triggers;
- multi-message bursts dan stale payloads;
- media and quoted media;
- owner/admin/user permissions;
- activation expired/unactivated;
- muted sender;
- LLM primary failure/fallback/tool malformed;
- sub-agent active/steering/completion;
- scheduled/direct invoke.

Jalankan Python reference dan Go shadow dengan fake deterministic LLM. Bandingkan:

- history;
- LLM request messages/tools;
- LLM1 decision;
- final action intents dan ordering;
- state writes;
- metrics/error category.

### HTTP API

Black-box harness menjalankan old dan Go servers terhadap isolated tenant fixture. Bandingkan route, status, headers penting, dan normalized JSON untuk:

- auth fail-closed dan bad token rate limit;
- overview/accounts;
- settings/memory/models/activation/bot config/stickers;
- env masking/restart fields;
- update/restart adapter;
- sub-agent outbox;
- audit log;
- invalid IDs, traversal, null bytes, oversized body, duplicate requests.

### Adapter integration

Sidecar path:

- handshake/reconnect/heartbeat;
- reliable queue flush/order/overflow;
- every action and ACK/error;
- Node restart, Go restart, network partition;
- WhatsApp not-open gate;
- durable duplicate request replay.

Native path:

- real WhatsApp test accounts;
- Android/iOS/Web sender matrix yang realistis;
- all message/media/interactive capabilities;
- reconnect and device logout;
- LID/PN and group roles;
- multi-account isolation.

### Fault injection

Matikan process pada titik:

- setelah action claim, sebelum send;
- setelah send, sebelum receipt complete;
- setelah webhook persisted, sebelum response;
- saat output partially written;
- saat scheduler claim;
- saat WAL active;
- saat account catalog/env atomic rename;
- saat reconnect queue flush.

Expected outcome harus ditentukan: retry, no-retry, reconciliation, atau dead-letter.

### Load and soak

Uji berdasarkan traffic target, bukan angka arbitrer. Minimal scenarios:

- concurrent tenants dan chats;
- burst messages pada chat yang sama;
- slow LLM and media downloads;
- reconnect storm;
- large sub-agent output;
- control panel mutations saat agent aktif;
- 24+ hour soak untuk timer/cache/goroutine/WAL growth.

Track goroutines, heap, queue depth, DB busy, dropped events, latencies, and file growth.

## Security tests

- timing-safe auth behavior secara code review dan tests;
- token fail-closed;
- SSRF policy untuk URL download: scheme/host allow policy, DNS resolution, redirect revalidation, private/link-local/loopback blocking;
- archive/path traversal and symlink/reparse escape;
- MIME/content-size mismatch;
- malicious HTML rendering input;
- SQL injection and query parameterization;
- prompt display-name/context injection guard;
- webhook replay/forgery;
- cryptographic activation/token generation;
- secret redaction in logs/API/errors;
- request/body/frame/resource limits.

## Canary strategy

### Stage A — Offline

Go hanya memproses fixtures. Tidak ada network production atau writes.

### Stage B — Shadow live

Go menerima copy event tenant test, menghasilkan decisions, tetapi dispatch dan writes dimatikan. Compare dengan Python.

### Stage C — Test tenant active

Go agent aktif melalui Node sidecar untuk dedicated account. Test semua features dan restart/fault paths.

### Stage D — Production tenant low risk

Satu tenant dengan feature flags dan operator window. Keep Python stopped but ready for rollback; Node/Baileys tetap unchanged.

### Stage E — Expand Go agent

Tambah tenant secara bertahap setelah burn-in dan metric/error budget lulus.

### Stage F — Native adapter canary

Hanya setelah spike ADR. Mulai dedicated test account, lalu selected tenant dengan explicit re-pair/import plan.

## Pre-cutover checklist

- [ ] Release artifact, source commit, module sums, dan config checksum tercatat.
- [ ] Baseline, integration, race, migration, and security gates pass.
- [ ] Tenant data and auth backups complete; restore tested.
- [ ] Disk space cukup untuk backup, WAL, migration temp, dan outputs.
- [ ] No pending schema writer dari process lama.
- [ ] Pending jobs/sub-agent completions/action receipts inventoried.
- [ ] Maintenance/draining behavior dikomunikasikan.
- [ ] Dashboard, logs, alerts, and operator access ready.
- [ ] Rollback binary/config/runbook ready.
- [ ] Test message, media, command, job, and sub-agent probes defined.

## Cutover runbook per tenant

1. Disable new cold work dan set draining.
2. Tunggu active chat batches/action dispatches sampai timeout yang disepakati.
3. Stop Python session untuk tenant.
4. Stop old DB writer path; Node tetap sidecar bila dipilih.
5. Checkpoint DB and take final backup/manifest.
6. Run Go migration dry-run; review output.
7. Apply migrations/importers.
8. Run integrity and invariant checks.
9. Start Go tenant runtime in active mode.
10. Verify protocol/sidecar binding dan WhatsApp `open`.
11. Run DM/group/action/media/job probes.
12. Observe errors, queue, DB, and duplicate metrics.
13. Mark cutover complete atau trigger rollback.

## Automatic rollback triggers

Threshold exact ditetapkan dari baseline production. Trigger class:

- cross-tenant state or path leak;
- message/action duplicate yang berbahaya;
- sustained incoming/outgoing loss;
- DB integrity/migration failure;
- uncontrolled reconnect loop;
- sub-agent completion loss;
- auth/session corruption;
- queue growth tanpa recovery;
- required core feature unavailable.

LLM textual variation saja tidak otomatis rollback jika action safety dan acceptance criteria masih terpenuhi; review product owner diperlukan.

## Rollback runbook

1. Stop Go intake dan freeze writes.
2. Capture logs, metrics, receipts, outbox, and current DB copy.
3. Classify unknown side-effect requests for reconciliation.
4. Restore pre-cutover data jika old runtime tidak forward-compatible.
5. Start pinned Node/Python release and old auth path.
6. Verify DB integrity, bridge handshake, WhatsApp open, and representative probes.
7. Re-arm jobs/completions only after dedup reconciliation.
8. Record incident and block next canary until corrective tests exist.

## Production acceptance

- Zero tenant isolation violation.
- No unreconciled duplicate destructive actions.
- Incoming/outgoing delivery and error rates within agreed baseline.
- p95/p99 agent latency no worse than agreed budget.
- DB busy/recovery and WAL growth stable.
- Scheduler lateness within tolerance.
- Every durable sub-agent completion delivered or visible in retry/dead-letter state.
- Restart and host reboot recover without manual DB repair.
- Operators can backup, restore, inspect readiness, and rollback.
