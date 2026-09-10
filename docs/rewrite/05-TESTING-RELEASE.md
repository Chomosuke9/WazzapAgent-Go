# Pengujian dan Release

## Istilah

- **Local verification:** test/build pada checkout lokal; tidak membuktikan WhatsApp atau deployment nyata.
- **Real-device verification:** probe dengan dedicated WhatsApp test account.
- **Production canary:** isolated process pada production host dengan account/path/port/allowlist terpisah.
- **Stable release:** baru dapat diklaim setelah Part 9 gates lulus.

Part 1 menargetkan production canary hari ini, bukan stable release.

## Quality gates setiap Part

```text
gofmt -l .  # output wajib kosong
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
go mod verify
govulncheck ./...
```

CI harus memakai tracked `go.sum`, pinned/resolvable dependencies, dan toolchain yang memenuhi minimum seluruh module graph. Release build memakai `CGO_ENABLED=0` selama modernc/Hypermeow native gates tetap lulus.

## Test pyramid

### Unit

- semantic ID and `SenderRef` validation;
- account and action state transitions;
- prompt command parsing/authorization/size limits;
- Agent key/context-bounded constructor/config invariants;
- AgentRegistry coalesced construction, independent waiter cancellation, pinning, bound, stale notification, and idle eviction;
- Agent Config refresh/defensive snapshot/CAS/best-effort post-commit notification semantics;
- Invocation digest, TurnStore lease, plan commit, and replay state transitions;
- trigger rules and message bounds;
- LLM request construction and output validation;
- retry/error classification;
- config validation and redaction;
- per-Agent invocation serialization and cancellation.

Use fake clock, deterministic random source, fake LLM, fake sender, and temporary data roots.

### Store integration

- fresh embedded migration and checksum;
- foreign keys, WAL, busy timeout, rollback;
- `ClaimAndResolveSender` atomicity;
- sender-ref collision retry and uniqueness;
- prompt-override set/view/clear and isolation;
- inbound dedup;
- full allowlisted transcript intake, passive group retention, monotonic sequence/quote metadata, and through-invocation context boundary;
- InvocationID/digest conflict and generation lease expiry;
- atomic response/action plan transaction;
- receipt conflict and unknown-outcome transitions;
- reopen after forced process exit;
- coordinated checkpoint/backup/restore;
- two-tenant path/row isolation.

### Fake end-to-end

```text
fake source
 -> real inbound claim/trigger/policy
 -> real AgentRegistry
 -> real Agent.Invoke with fake ModelInvoker
 -> real TurnStore response/action commit
 -> real durable ResponseDispatcher/outbox/worker
 -> fake sender
```

Required Part 1 scenarios:

- eligible DM sends exactly one response;
- group mention sends one response;
- passive group text/sticker sends none immediately but appears in the next eligible model context;
- non-mention group/self/status/duplicate sends none;
- owner `/prompt` operations bypass LLM and persist;
- non-owner prompt mutation is denied;
- unauthorized mutation never calls Agent Config methods;
- same key returns one live Agent even during concurrent first access;
- idle Agent recreation restores durable Config;
- cancellation of one AgentFor waiter does not cancel another waiter's construction;
- config update during Invoke does not alter the captured Config version;
- failed Config CAS does not mutate memory or publish `ConfigChanged`;
- Config conflict forces policy reload and reauthorization instead of blind retry;
- dropped/reordered Config notification does not fail mutation or leave the next authorization stale;
- same InvocationID/digest after planning skips the model and reuses one action;
- same InvocationID with changed input digest fails closed;
- handled model failure releases/terminates the generation lease; simulated crash requires lease expiry before retry;
- provider error/timeout/cancellation does not create unsafe send;
- duplicate action wake-up does not send twice;
- same chat remains ordered while different chats progress concurrently;
- graceful shutdown releases/cancels work predictably.

### Real-device

Real-device tests use a dedicated account and recipient/chat allowlist. Tests must never target arbitrary contacts/groups.

Part 1 matrix:

| Capability | Part 1 gate |
|---|---|
| Fresh QR or pairing number | At least one flow passes |
| Persistent session after restart | Required |
| Incoming DM text | Required |
| Outgoing DM text | Required |
| Group mention detection/response | Required |
| Duplicate replay probe | Required |
| Network loss recovery | Smoke only; full matrix Part 2 |
| Logout/device removal | Deferred to control operations |
| Reaction/delete/read/presence | Deferred Part 3 |
| Image/media | Deferred Part 4 |

Record library commit, Go version, OS/arch, client type, test account, timestamp, result, and observed limitation. Never store phone/JID or secret in committed reports.

## Part 1 test inventory

### SenderRef

- random generation produces exactly six lowercase base36 characters;
- non-canonical `u_...` references are rejected; fresh data must use six-character sender refs;
- collision is retried under unique constraint;
- same participant/chat returns existing ref;
- same provider participant in another chat has independent ref;
- tenant A cannot resolve tenant B mapping;
- reopen/restore keeps ref;
- raw address does not appear in LLM request or normal logs;
- ref cannot be accepted as role/owner evidence.

### Prompt

- exact `/prompt`, `/prompt view`, `/prompt set`, `/prompt clear` grammar;
- malformed command deterministic error;
- owner identity resolved by trusted mapping;
- non-owner denial in the external application/policy handler;
- Agent Config mutation methods accept no actor and perform no authorization;
- an authorization decision is bound to the exact Config snapshot version passed to mutation;
- `/prompt set|clear` mutates only per-chat `PromptOverride`;
- base and non-overridable safety prompt remain unchanged;
- UTF-8 and length boundaries;
- versioned transactional CAS set/clear;
- per-chat and per-tenant isolation;
- prompt survives restart;
- prompt content redacted from normal logs;
- command never reaches LLM.

### Agent contract

- one `Agent` key contains tenant/account/chat and no raw provider identifier;
- concurrent `AgentFor` calls construct one live instance;
- registry hard limit and backpressure are deterministic;
- in-flight Agent cannot be evicted;
- eviction never deletes durable Config/action state;
- Agent recreation reloads the latest Config version;
- constructor I/O respects context timeout and retains no caller context;
- same-Agent Invoke calls serialize;
- different Agents progress concurrently within semaphore limits;
- stale externally decided PolicyVersion fails before TurnStore claim/model invocation;
- Config direct field mutation is impossible;
- snapshots defensively copy pointer/slice/map members;
- `ConfigChanged` is post-commit, non-sensitive, best effort, and not required for correctness;
- notification reordering/loss is repaired by durable `Refresh()`;
- `NotifyConfigChanged` never creates or evicts an Agent;
- stored response plan replay skips ModelInvoker and keeps response/action IDs;
- model cannot change Agent key/target;
- current external policy is rechecked immediately before every send;
- `Agent.Invoke()` returns succeeded/pending/unknown delivery accurately.

### Inbound and action durability

- duplicate provider key returns duplicate claim;
- crash after inbound claim can resume safely;
- crash during LLM call may repeat model computation but not WhatsApp effect;
- plan response and mark inbound planned are atomic;
- same InvocationID/digest after plan returns/dispatches the stored plan without another LLM call;
- action key/digest replay returns existing receipt;
- conflicting digest fails closed;
- crash before native send permits safe retry;
- timeout/disconnect after send begins becomes `unknown_outcome` when provider evidence is insufficient;
- unknown action is not auto-replayed.

### Concurrency and bounds

- bounded inbound queue backpressure;
- same-chat requests serialize;
- cross-chat requests can execute concurrently;
- Agent registry entries are safely idle-evicted after work/cancellation;
- global LLM limit enforced;
- DB/LLM/connect/send deadlines enforced;
- oversized text/prompt/response rejected;
- all goroutines stop under root cancellation;
- race detector reports no shared-state race.

### Security and privacy

- LLM API key never appears in errors/logs;
- message body and prompts are not logged by default;
- raw JID/phone/provider payload does not cross adapter boundary;
- canary allowlist is fail-closed when missing/invalid;
- model response cannot choose tenant/chat/action ID/idempotency key;
- no model tool calls or generic commands accepted;
- HTTP health endpoint binds to configured safe address and exposes no secrets.

## Part 1 canary stages

### Stage A — Local offline

1. Run all quality gates.
2. Run store/fake end-to-end/fault tests.
3. Inspect config redaction and logs.
4. Build the exact artifact intended for the host.
5. Record source commit or working-tree digest, module sums, Go version, OS/arch, and artifact checksum.

### Stage B — Isolated native smoke

1. Provision fresh dedicated data directory.
2. Use dedicated WhatsApp test account.
3. Keep response agent disabled.
4. Pair account.
5. Verify native connect and account readiness.
6. Restart process and verify session reopen.
7. Create first coordinated database backup; outbound send begins only in bounded Stage C.

### Stage C — Allowlisted agent probe

1. Configure at least one DM and one group allowlist target.
2. Verify missing/empty allowlist fails closed.
3. Enable agent.
4. Run one DM response.
5. Run non-mentioned and mentioned group probes.
6. Run `/prompt set`, `view`, and `clear` as owner.
7. Verify non-owner denial.
8. Restart and verify sender ref/prompt persistence.
9. Replay a sanitized duplicate inbound event and inspect receipt count.

### Stage D — Production-host canary

Deploy the same tested artifact using:

- separate service/process name;
- separate port;
- separate data root;
- separate log destination;
- dedicated WhatsApp account;
- mandatory allowlist;
- agent feature flag and one-step kill switch.

The old service remains running and untouched. Observe a bounded canary window and record:

- account state/reconnects;
- inbound accepted/duplicate/rejected counts;
- queue depth/oldest age;
- LLM latency/error/timeout;
- action succeeded/failed/unknown count;
- DB busy/WAL size;
- goroutine/heap trend.

Part 1 is complete only after Stage D probes and rollback test pass.

### Current local evidence — 2026-09-08

- `gofmt -l .`, `go mod tidy -diff`, `go mod verify`, `go vet ./...`, `go test -count=1 ./...`, dan `go test -race -count=1 ./...`: pass pada Go 1.27.0 Windows amd64; test/vet juga pass pada minimum Go 1.26.5.
- `CGO_ENABLED=0` build: pass untuk Windows amd64, Linux amd64, Linux arm64, dan Android arm64.
- `govulncheck@v1.7.0`: zero reachable symbol/package vulnerabilities. Satu module-level advisory untuk package `openpgp` yang tidak diimpor tidak berada pada call graph binary; dua advisory lain ditutup dengan `golang.org/x/crypto` v0.56.0.
- Real Windows process dengan kedua feature flag disabled: `/health/live`, `/health/ready`, dan `/metrics` pass.
- Tidak ada real-device pairing/message atau production-host deployment pada evidence ini.

### Current Part 2 local evidence — 2026-09-09

- Pada Go 1.27.0 Windows amd64, `gofmt -l .`, `go mod tidy -diff`, `go mod verify`, `go vet ./...`, `go test -count=1 ./...`, `go test -race -count=1 ./...`, dan `go build ./cmd/...` lulus.
- Stress regression batching/order/retry dijalankan 50 kali per package dan lulus. Store tests juga mencakup migration 1 ke 2, durable history reopen, corrupted-history rejection, reset race, active-generation recovery, exact plan/history timestamp replay, action unknown outcome, serta backup/verify/restore ke directory baru.
- `CGO_ENABLED=0` build lulus untuk Windows amd64, Linux amd64, Linux arm64, dan Android arm64. Artifact tersebut hanya quality-gate sementara, bukan artifact canary yang ditandatangani atau dideploy.
- `govulncheck@v1.8.0` dengan database 2026-09-02 melaporkan zero reachable vulnerabilities; satu advisory module-only tidak berada pada package/call graph yang dipakai.
- Belum ada real-device Part 2 matrix, forced OS process-kill, observasi network-loss nyata, restore drill dengan Hypermeow session, production-host canary, atau soak. Karena itu evidence ini hanya membuktikan local implementation/fake integration.

## Part 2 pre-canary checklist

- [x] Durable history/context, batching, quote, reply trigger, reset, retention, metrics, dan backup/restore implementation tersedia.
- [x] Format, tidy/verify, vet, full tests, race detector, host build, vulnerability scan, serta empat cross-build target lulus lokal.
- [x] Schema upgrade dan current invocation digest diuji.
- [x] Generation/action lease recovery, replay, unknown-outcome, reset race, dan same-chat ordering memiliki deterministic regression tests.
- [ ] Exact canary artifact/commit dan checksum dicatat setelah konfigurasi operator siap.
- [ ] Dedicated device/account, isolated data root/port/process/log, owner, serta allowlist disiapkan.
- [ ] Real-device matrix, forced kill/network-loss observation, backup/restore drill, dan rollback lulus.

## Part 1 pre-canary checklist

- [x] Scope/non-scope frozen.
- [ ] Exact artifact and checksum recorded.
- [x] Format, vet, tests, race, module verify, vulnerability scan, and build pass.
- [x] Fake end-to-end/replay/crash-boundary tests pass.
- [ ] Dedicated test account and allowlisted recipients/chats prepared.
- [ ] Data root, port, process/service, and logs do not overlap old runtime.
- [ ] LLM and application secrets supplied outside chat/log/CLI history.
- [x] Normal runtime defaults enabled behind a mandatory fail-closed allowlist.
- [ ] Kill switch tested.
- [ ] Backup and rollback commands verified.
- [x] Health and account readiness observable locally; real-device state remains pending.
- [ ] Operator understands `unknown_outcome` and will not blindly replay it.

## Canary stop triggers

Immediately disable the Go agent when:

- any message reaches a non-allowlisted target;
- old and new runtime touch the same WhatsApp account or data path;
- duplicate visible response appears;
- raw JID, phone, secret, prompt, or message body leaks to normal logs;
- account enters uncontrolled reconnect/pair loop;
- database integrity/migration fails;
- queue, goroutine, WAL, disk, or memory growth is unbounded;
- action remains ambiguous and is automatically resent;
- kill switch or readiness monitoring is unavailable.

## Canary rollback

1. Disable agent and stop only the Go canary process.
2. Preserve both canary databases and logs for diagnosis.
3. Record the last inbound ID, action ID, and receipt state without exposing raw address/content.
4. Do not delete or mutate old runtime data.
5. Logout only the dedicated test account if the native session itself must be invalidated.
6. Restore canary database only into a separate verification directory.

## Later-Part gates

### Part 2

- history/window/context golden tests;
- history read/reset rejects a changed externally authorized Config version;
- history append is idempotent by message/invocation identity and detects digest conflict;
- batching/debounce with fake clock;
- network partition, replay, process kill, and recovery;
- backup/restore/retention;
- concurrent-chat load.

### Part 3

- typed tool schema and permission matrix;
- live role refresh;
- destructive-action unknown outcomes;
- prompt-injection attempts cannot gain authority.

### Part 4

- MIME/content mismatch, decompression, size/pixel limits;
- SSRF/DNS/redirect policy;
- path traversal/symlink/reparse escape;
- real-client image/media matrix.

### Part 5

- multiple real account isolation;
- noisy-neighbor budgets;
- control API/browser auth, CSRF, rate limiting, and audit.

### Parts 6–8

- scheduler lease/timezone/restart tests;
- direct invoke auth and live context refresh;
- sub-agent callback/file/delivery state fault injection;
- advanced feature capability and compatibility matrix.

### Part 9 stable release

- full security audit;
- reproducible target artifacts/checksums;
- fresh install, upgrade, backup, restore, and rollback drills;
- 24+ hour soak;
- bounded per-tenant resource growth;
- operator runbook, alerting, and stable release sign-off.

## Reporting rule

Every status report states exactly which level passed:

- local tests/build;
- fake integration;
- real-device smoke;
- production-host canary;
- stable release.

Never infer a higher level from a lower one.
