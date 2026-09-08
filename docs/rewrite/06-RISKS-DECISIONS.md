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

Schema dirancang baru dan versioned sejak Part 1. Tidak ada importer SQLite/JSON/auth/config lama.

### D5 — Durable correctness state di SQLite

**Status:** accepted.

Setiap Part menambah hanya durable state yang dibutuhkannya. Part 1 memiliki inbox claim, outbound text intent, dan action receipt. Part berikutnya menambah history, scheduler leases, dan sub-agent delivery state melalui immutable migrations dengan bounded retention.

### D6 — Fresh pairing

**Status:** accepted.

Setiap account melakukan pairing baru ke native device store. Tidak ada konversi Baileys auth.

### D7 — Control panel di binary utama pada Part 5

**Status:** accepted.

Default satu lifecycle dan embedded UI. Pisahkan hanya jika security boundary atau scaling membutuhkannya.

### D8 — Artifact deployment

**Status:** accepted.

Default release berupa reproducible artifact/container. Self-update via writable Git checkout bukan core capability.

## Risk register

| ID | Risiko | Severity | Mitigasi / Gate |
|---|---|---:|---|
| R1 | Hypermeow tidak mendukung fitur WhatsApp penting | Critical | Native capability matrix sebelum fitur dipromosikan ke Part terkait |
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
| R21 | Target same-day mendorong bypass durability/security | Critical | Part 1 scope dipotong, tetapi dedup, allowlist, receipt, bounds, dan kill switch tidak boleh dipotong |
| R22 | Canary mengganggu service/account lama | Critical | Dedicated account, data root, port, process, logs, dan no-touch rule |
| R23 | Giant interface/service locator tumbuh kembali | High | Consumer-owned narrow ports, composition root only, import checks |
| R24 | Sender ref dipakai sebagai authority | High | Sender ref hanya presentation handle; authority berasal dari trusted current identity/role resolver |
| R25 | Per-chat Agent menjadi god object | High | Agent is a façade composed from Config, History, TurnStore, ModelInvoker, and ResponseDispatcher; adapters remain separate |
| R26 | Direct Config field mutation melewati persistence/validation | High | Private fields, durable refresh, expected-version CAS, defensive snapshots, best-effort post-commit notifications |
| R27 | Permission logic tersebar atau masuk Agent core | Critical | All entry points use external policy/application handlers; executor rechecks every external effect |
| R28 | Agent registry tumbuh tanpa batas | High | Lazy construction, hard bound, in-flight pinning, idle eviction, durable reconstruction |
| R29 | Replay Invocation memanggil model/membuat action kedua | Critical | Unique InvocationID + canonical digest, generation lease, atomic stored plan, replay skips model after plan |
| R30 | Prompt/base/override tertukar | High | Explicit prompt composition; Part 1 `/prompt` mutates only per-chat append override |
| R31 | Config cache stale lintas process | High | Durable Refresh before authorization/new generation; notifications are hints only |
| R32 | Process-local Agent lock dianggap global | Critical | One account owner process; require durable chat lease/ordered partition before per-account fan-out |
| R33 | Permission changes between external check and history mutation | Critical | Immutable policy revisions plus Config-version guard verified in history store transaction |

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

## Keputusan Part 0

Keputusan arsitektur kritis untuk Part 1 dan evolution path telah dipilih. Keputusan masih dapat dibatalkan hanya bila capability spike atau implementation evidence menghasilkan bukti yang bertentangan.

### D9 — Part-based delivery dan scope Part 1

**Status:** accepted.

- Part adalah vertical slice deployable, bukan layer teknis.
- Part 0 hanya persiapan, keputusan, spike, dan plan.
- Part 1 adalah `v0.1-canary`, bukan stable release.
- Product surface Part 1 hanya satu dedicated test account.
- Internal identity, repositories, paths, runtime, dan actions tetap tenant-aware.
- Part 1 hanya incoming/outgoing text, opaque durable sender ref, base/per-chat prompt, `/prompt`, dan satu text-only LLM.
- Eligible DM memicu agent; group hanya ketika bot di-mention dan target berada dalam allowlist.
- Part 1 tidak memiliki history, debounce, quote/replied-to-bot, model tools, media, atau commands selain `/prompt`.
- Reliability conversation, typed actions, media, multi-account/control plane, scheduler, sub-agent, dan advanced WhatsApp masuk Part 2–8.
- Stable release membutuhkan Part 9 gates.

Konsekuensi: same-day goal dicapai dengan membuang breadth, bukan dengan membuang tenant isolation, durable dedup/action state, bounds, allowlist, or rollback.

### D10 — SQLite driver pure-Go

**Status:** accepted pending capability gate.

Gunakan `modernc.org/sqlite` sebagai default driver dan build release dengan `CGO_ENABLED=0`.

Alasan:

- target wajib mencakup Debian, Windows, dan Android Termux;
- `mattn/go-sqlite3` membutuhkan CGO, compiler C per target, MinGW untuk Windows, serta Android NDK untuk APK;
- Hypermeow menerima `*sql.DB` melalui `sqlstore.NewWithDB` dan mengenali dialect yang diawali `sqlite`;
- modernc mendaftarkan driver `sqlite`, mendukung foreign keys, WAL, busy timeout, transaction, UPSERT, dan multi-statement migration yang dibutuhkan Hypermeow.

Ketentuan:

- pin versi modernc beserta versi `modernc.org/libc` yang tepat;
- pin Hypermeow ke immutable pseudo-version/commit yang resolvable;
- gunakan explicit SQLite pragmas dan verifikasi setelah open;
- jangan mengimpor mattn dan `ncruces/go-sqlite3` dalam binary yang sama karena keduanya dapat mendaftarkan nama driver `sqlite3`;
- `ncruces/go-sqlite3` menjadi fallback spike bila modernc gagal pada perangkat Termux;
- custom implementation untuk seluruh Hypermeow store interface ditolak untuk roadmap sekarang.

Default DSN target:

```text
file:<path>?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL
```

Capability gate wajib menguji foreign keys, WAL, busy timeout, migration Hypermeow, seluruh representative device stores, reopen, contention, forced termination, integrity check, checkpoint, backup, dan restore.

### D11 — Database topology berdasarkan ownership

**Status:** accepted.

Gunakan dua SQLite database per tenant:

```text
<data-root>/tenants/<tenant-id>/app.db
<data-root>/tenants/<tenant-id>/whatsapp.db
```

- `app.db` dimiliki migration/store aplikasi dan menampung hanya domain state yang sudah dipromosikan pada Part berjalan; Part 1 berisi prompt, identity mapping, inbox, outbound text action, dan receipt minimum.
- `whatsapp.db` dimiliki Hypermeow sqlstore dan hanya menampung device/session/protocol state.
- Application transaction tidak boleh mengasumsikan atomic commit lintas kedua database.
- Backup mengoordinasikan checkpoint dan consistent snapshot kedua database ketika account intake dihentikan sementara.

Alasan: action, history, dan delivery membutuhkan satu transactional boundary, sedangkan schema dan migration device store dimiliki library eksternal.

### D12 — Modular monolith single binary

**Status:** accepted.

Satu binary memiliki lifecycle untuk config, HTTP health, account runtime, agent, action worker, persistence, dan observability. Module berkomunikasi melalui typed consumer-owned ports, bukan global state atau giant gateway/service interfaces. Tidak ada Node/Python sidecar atau internal WebSocket protocol. Pemisahan service memerlukan ADR baru dan bukti kebutuhan operasional.

### D13 — Config startup snapshot

**Status:** accepted.

Part 1 membaca config sekali saat startup, menghasilkan immutable validated snapshot, dan gagal sebelum network startup jika invalid. Hot reload ditunda. Per-chat prompt merupakan domain data dan dapat berubah transactional tanpa reload process.

Secret:

- berasal dari environment atau file dengan permission terbatas;
- tidak diterima melalui query parameter;
- selalu direduksi pada log dan diagnostic output;
- perubahan startup config memerlukan restart yang terkontrol.

### D14 — Opaque UUIDv7 identifiers dan sender ref terpisah

**Status:** accepted.

Gunakan opaque UUIDv7 text untuk `TenantID`, `MessageID`, `ActionID`, `CorrelationID`, dan durable entity IDs. Tambahkan semantic Go types agar ID tidak saling tertukar.

- Provider message ID/JID disimpan terpisah sebagai adapter/store mapping dan tidak menjadi public domain identity.
- Internal `MessageID` dibuat saat inbound event diklaim atau outgoing intent dibuat.
- Quoted message menunjuk `MessageID`; unresolved external quote disimpan sebagai optional external reference sampai retention berakhir.
- ID tidak didaur ulang dan tidak memiliki wrap-around.
- History row dapat dipangkas oleh retention, tetapi receipt/tombstone minimal dipertahankan sesuai dedup window.
- Short six-digit `contextMsgId` legacy ditolak sebagai durable entity ID.
- `SenderRef` adalah random human-readable presentation handle yang disimpan per tenant/chat/participant.
- Sender ref tidak diturunkan dari phone/JID dan tidak menjadi principal, role, atau authorization proof.

### D15 — Interactive features deferred

**Status:** accepted.

Buttons, carousel, copy-code, quiz, Lottie, HTML rendering, dan interactive settings tidak masuk Part 1. Jangan mengirim raw protobuf workaround. Ketika fitur dibuka kembali pada Part 8, default fallback adalah representasi text yang aman dan capability-aware.

### D16 — Control panel deferred dan future session auth

**Status:** accepted.

Control panel tidak masuk Part 1. Operasi Part 1 menggunakan startup config dan fixed `/prompt` flow yang terbatas. Ketika control panel dibangun pada Part 5:

- initial bootstrap memakai one-time token pada local/explicit setup flow;
- browser menggunakan `Secure`, `HttpOnly`, `SameSite=Strict` session cookie;
- mutation menggunakan CSRF token dan Origin validation;
- session server-side memiliki idle dan absolute expiry;
- recovery/rotation tidak pernah mengembalikan secret lama;
- bearer token dapat ditambahkan terpisah untuk machine API, bukan digunakan sebagai browser storage default.

### D17 — Canary dan platform support

**Status:** accepted pending native gates.

Primary deployment dan optimasi operasional adalah Debian Linux. Part 1 hanya menargetkan isolated production-host canary pada host yang dipilih. Stable release gate Part 9 mencakup:

- Debian Linux `amd64` dan `arm64`;
- Windows `amd64`;
- Android Termux `arm64` pada perangkat fisik.

macOS bersifat best-effort. APK native ditunda dan memerlukan ADR mobile tersendiri untuk `gomobile`/JNI, app-private storage, background lifecycle, Android force-stop, ABI packaging, backup/export, dan upgrade preservation.

Part 1 canary wajib memakai dedicated WhatsApp account, data root, port, process/service, logs, mandatory allowlist, default-disabled agent flag, kill switch, dan backup awal. Service lama tidak boleh dihentikan atau dimodifikasi.

Stable release artifact:

- binary dibangun dengan `CGO_ENABLED=0`;
- Debian menerima archive dan service example;
- Windows menerima archive binary dan service/manual runbook;
- Termux menerima archive/install script tanpa menulis database ke shared storage;
- database Termux berada di private `$HOME`;
- reproducible build mencatat Go toolchain, module sums, source commit, target, dan checksum.

### D18 — Initial SLO dan resource limits

**Status:** accepted sebagai baseline yang dapat diketatkan setelah benchmark.

Reliability objectives:

- zero cross-tenant read/write/path violation;
- zero automatic replay untuk destructive action dengan outcome tidak diketahui;
- accepted inbound event dan committed action intent survive process restart;
- process readiness tercapai dalam 10 detik pada fresh DB, tidak termasuk account connect;
- healthy paired account mencapai `open` dalam 60 detik setelah network tersedia pada p95;
- graceful shutdown budget 30 detik;
- internal durable inbound claim p95 di bawah 250 ms dan p99 di bawah 1 detik, tidak termasuk WhatsApp transport;
- non-LLM application overhead p95 di bawah 250 ms, tidak termasuk SQLite contention, provider, dan WhatsApp network;
- Part 1 bounded canary window tidak menunjukkan pertumbuhan queue, goroutine, file, atau WAL tanpa batas;
- Part 9 24-hour soak tidak menunjukkan pertumbuhan resource tanpa batas.

Default limits:

| Resource | Default |
|---|---:|
| Active account pada Part 1 | 1 |
| Global inbound queue | 512 events |
| Live Agent instances per process | 256 |
| Concurrent LLM calls desktop | 4 |
| Concurrent LLM calls Termux | 2 |
| Message text | 32 KiB |
| Per-chat prompt | 16 KiB |
| LLM response text | 16 KiB |
| Model effects per Part 1 turn | 1 `SendText` |
| Terminal inbox/action/receipt tombstone retention | 30 hari |
| Terminal message/action content scrub | 24 jam |
| SQLite busy timeout | 5 detik |
| Graceful shutdown | 30 detik |

Media, history, batching, and context limits are selected in Parts 2 and 4 based on tests; they are not speculative Part 1 schema/config.

Overflow policy:

- jangan drop durable inbound yang sudah diterima tanpa status;
- hentikan intake atau terapkan backpressure ketika queue penuh;
- jangan block event loop/goroutine tanpa deadline;
- resource exhaustion menghasilkan stable `resource_exhausted` dan metric;
- destructive action tidak boleh diulang otomatis dari `unknown_outcome`.

### D19 — Consumer-owned narrow ports

**Status:** accepted.

- Tidak ada application-wide giant `Gateway`, `Services`, atau generic repository interface.
- Pair/connect, message source, model invocation, Config persistence, durable turn claim/planning, response dispatch, and text send are separate consumer-owned capabilities.
- `internal/app` is the only composition root and may know concrete adapters only for construction/lifecycle.
- A module is extracted to a service only after measurement or security boundary evidence and a new ADR.

Konsekuensi: the Hypermeow adapter may implement several small ports, but a consumer depends only on the methods it actually uses. Tests fake small ports instead of mocking an entire runtime.

### D20 — Part 1 senderRef

**Status:** accepted.

- Sender ref is random, opaque, human-readable, and scoped to tenant/chat/participant.
- Mapping and collision handling are durable and transactional.
- Raw phone/JID is not embedded in the ref, sent to the model, or logged normally.
- Sender ref is a presentation handle only and cannot prove identity, ownership, or role.

### D21 — Part 1 canary isolation

**Status:** accepted.

The same-day target cannot relax isolation controls. Canary requires dedicated WhatsApp account, data root, port, process/service, logs, mandatory allowlist, default-disabled agent feature flag, kill switch, and backup. Old runtime remains untouched. The result is labeled `v0.1-canary`.

### D22 — Chat-scoped Agent aggregate

**Status:** accepted.

- One live Agent represents exactly one `(TenantID, AccountID, ChatID)`.
- Agent exposes `Key`, `Config`, and `Invoke`; `History` is added in Part 2. Speculative `Status` is not a public Part 1 API.
- `New(ctx, key, deps)` performs bounded initialization and does not retain caller context.
- `AgentRegistry` constructs lazily, coalesces concurrent first access, pins in-flight Agents, enforces a hard bound, and idle-evicts memory only.
- Agent recreation loads durable state; registry memory is never the source of truth.
- `Agent.Invoke()` is the public façade but delegates durable turn planning to `TurnStore` and delivery of the stored action to `ResponseDispatcher` rather than importing Hypermeow.
- Account native connection remains owned by `AccountRuntime` and shared through narrow capabilities.

Konsekuensi: API becomes object-oriented and chat-local without creating a permanent goroutine/object for every historical chat or a god object that owns infrastructure details.

### D23 — Authorization outside Agent and explicit Config change

**Status:** accepted.

- Trigger, actor identity verification, and authorization happen in application/policy handlers before Agent core methods; policy may read a refreshed immutable Agent Config snapshot.
- `Agent.Config()` and `Agent.History()` mutation methods accept no actor and evaluate no permission.
- Agent still validates domain values, versions, state transitions, and consistency.
- Permission stored in Agent Config is an immutable policy reference/revision interpreted externally, not proof of authority.
- Config has private fields, durable refresh, defensive snapshots, an externally-authorized expected version, durable compare-and-swap mutation, atomic in-memory publication, and a non-sensitive best-effort `ConfigChanged` notification after commit.
- Store creates Config version 1 and exclusively owns monotonic version increments.
- A Config conflict requires the application layer to refresh policy data and reauthorize; blind retry is forbidden.
- A dropped/reordered `ConfigChanged` does not fail a committed setter; consumers reconcile by durable version refresh.
- Externally authorized History read/reset receives the authorized Config version and verifies it in the storage transaction.
- Action executor independently rechecks current external policy before every side effect, including ordinary text delivery.

Konsekuensi: Go's lack of automatic property `onChange` is handled explicitly and safely. Direct field assignment is not part of the contract.

### D24 — Durable invocation identity and replay

**Status:** accepted.

- `InvocationID` identifies one logical turn and is bound to a canonical digest of Agent key, cause/causation, sender, content, and capabilities.
- New generation verifies the Config version used by external policy before claiming/calling the model; stale decisions conflict.
- First claim owns a bounded generation lease; same ID/different digest is `conflict`.
- Model computation may repeat only after an expired pre-plan lease.
- Response text, response/action IDs, outbound intent, and planned state commit atomically.
- Once a plan exists, replay loads it, skips the model, and dispatches/observes the same ActionID.
- Unknown delivery outcome is never turned into a replacement action automatically.

### D25 — Prompt and Config semantics

**Status:** accepted.

- Agent Config `Prompt` is the configurable base prompt copied from validated defaults on first creation.
- `PromptOverride` is optional durable per-chat instruction; Part 1 `/prompt set` uses append mode and `/prompt clear` removes only this override.
- Non-overridable application safety/system policy and provider credentials are never stored in Agent Config.
- `Permission` is an immutable policy reference/revision; policy rules remain externally owned.

### D26 — Registry freshness and scale boundary

**Status:** accepted.

- Registry uniqueness and invocation gates are process-local.
- Shared construction uses Registry lifecycle/timeout; canceling one waiter does not cancel work needed by another.
- Config notification marks an existing Agent stale but never constructs or evicts one; durable refresh remains authoritative.
- A durable account lease routes an account to one Agent process. Splitting one account across workers requires a durable chat lease or ordered partition first.

## Remaining validation gates

Part 0 is complete when:

- master plan, ownership, scope, non-scope, and Part 1 exit gate are approved;
- foundation passes current local format/vet/test/race/build checks;
- modernc/Hypermeow store spike evidence is recorded without calling it a production adapter;
- canary allowlist, kill switch, and rollback procedures are defined.

Part 1 still requires current evidence for:

- real-device pairing, session reopen, DM/group-mention text, and restart;
- sender-ref, prompt, dedup, action receipt, concurrency, and log-redaction tests;
- production-host isolation and rollback probe.

Debian/Windows/Termux full capability and 24-hour soak gates belong to Part 9 stable release, not Part 0 or Part 1 canary.

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

Hentikan Part/canary/release bila:

- required capability hanya lulus happy path;
- native session tidak pulih setelah restart/network loss;
- durable state machine belum lulus crash injection;
- side-effect outcome tidak dapat direconcile;
- tenant identity/path containment belum terjamin;
- secret/auth setup tidak fail-closed;
- resource limits tidak diterapkan;
- build tidak reproducible pada deployment target.
- canary berbagi account, data path, port, atau mutable state dengan runtime lama;
- allowlist atau kill switch tidak fail-closed.
