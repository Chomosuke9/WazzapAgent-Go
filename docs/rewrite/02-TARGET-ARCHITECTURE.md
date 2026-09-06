# Arsitektur Target

## Prinsip

1. Domain tidak bergantung langsung pada `hypermeow`, Baileys, HTTP framework, atau SQLite driver.
2. Semua operasi tenant menerima `TenantID` eksplisit dan dependency scope tenant.
3. `folderPath` tetap ada di protocol v2 compatibility adapter, bukan primary identity internal.
4. Satu komponen memiliki schema dan menjalankan migrations.
5. Network dan DB work memakai `context.Context`, deadline, bounded queue, dan graceful cancellation.
6. Side effect memakai idempotency key dan durable transactional state.
7. Adapter Node dan native Go dapat dipertukarkan selama migration.

## Topologi transisi

```text
                    +---------------------------+
                    | Go service                |
                    | config/account/control    |
Control panel HTTP  | agent/LLM/jobs/subagent   |
<------------------>| stores/protocol/outbox    |
                    +-------------+-------------+
                                  |
                         typed adapter interface
                                  |
                 +----------------+----------------+
                 |                                 |
       Node/Baileys sidecar              native Go/hypermeow
       compatibility path                experimental then canary
```

Phase awal tetap memakai protocol v2 WebSocket menuju Node/Baileys. Setelah agent dan persistence stabil, sidecar interface dipindahkan dari wire-specific API ke canonical internal API.

## Package layout

```text
cmd/
  wazzapagent/
  migrate/
  compat-probe/
internal/
  app/
  config/
  tenant/
  protocol/
  transport/
  account/
  whatsapp/
    adapter/
      sidecar/
      native/
      shadow/
    message/
    group/
    media/
    interactive/
  command/
  permission/
  activation/
  agent/
    batch/
    history/
    context/
    actions/
  llm/
    provider/
    router/
    responder/
  jobs/
  subagent/
  store/
    migration/
    settings/
    stats/
    moderation/
    stickers/
    delivery/
  controlpanel/
  security/
  observability/
web/
  controlpanel/
```

Package final boleh disederhanakan setelah dependency graph terbukti. Hindari `pkg/` untuk application internals. Existing prototype `pkg/whatsapp/socket.go` bukan executable karena `main` berada dalam `package whatsapp`; pindahkan spike yang masih berguna ke adapter native dan gunakan `cmd/wazzapagent` sebagai entrypoint. Referensi: `pkg/whatsapp/socket.go:1-26`.

## Komponen utama

### Application lifecycle

`internal/app` bertanggung jawab atas startup order, dependency wiring, readiness, shutdown, dan draining. Tidak memuat business logic.

Startup order:

1. load dan validasi config;
2. init logging/metrics;
3. buka migration lock dan database;
4. jalankan migrations atau dry-run;
5. load tenant catalog;
6. start control panel dan health endpoint;
7. start WhatsApp adapters;
8. start agent sessions setelah adapter ready;
9. arm scheduler dan sub-agent recovery hanya setelah tenant WhatsApp `open`.

Shutdown order membalik alur: stop intake, cancel jobs, drain outbox, disconnect adapters, checkpoint WAL, lalu close DB.

### Tenant registry

```go
type TenantID string

type Runtime struct {
    ID       TenantID
    RootPath string
    Stores   Stores
    WhatsApp WhatsAppGateway
    Agent    AgentSession
}
```

- Stable ID berasal dari account catalog, bukan absolute path.
- Compatibility map menyimpan canonical path yang harus di-echo untuk protocol v2.
- Windows path normalization hanya dilakukan di boundary.
- Registry state machine: `stopped`, `starting`, `pairing`, `connecting`, `open`, `draining`, `failed`.
- Per-tenant lock mencegah start/stop/reconnect race.

### Canonical WhatsApp port

```go
type Gateway interface {
    Status(context.Context) Status
    Events(context.Context) <-chan Event
    Send(context.Context, SendRequest) (SendResult, error)
    React(context.Context, ReactionRequest) error
    Delete(context.Context, DeleteRequest) error
    Kick(context.Context, KickRequest) (KickResult, error)
    ChatContext(context.Context, ChatContextRequest) (ChatContext, error)
    Pair(context.Context, PairRequest) (PairResult, error)
    Disconnect(context.Context) error
}
```

Port memakai canonical result, bukan raw Baileys/hypermeow protobuf. Protocol v2 adapter mengonversi legacy raw result hanya untuk compatibility.

`adapter/shadow` mengimplementasikan port yang sama tetapi hanya merekam action intent dan diff terhadap output Python. Adapter ini dilarang membuka socket WhatsApp atau menulis state production, sehingga shadow dan active mode memakai kontrak identik tanpa side effect.

### Protocol dan transport

- Canonical schema menghasilkan Go structs dan golden JSON fixtures.
- Decoder strict pada discriminator dan required fields, tetapi compatibility mode menerima legacy nullable fields.
- Reliable delivery memakai bounded durable outbox untuk state penting.
- `requestId` tetap didukung; internal idempotency memakai stable tenant-scoped key.
- Heartbeat, reconnect, exponential backoff dengan jitter, dan queue metrics.
- Unknown protocol version ditolak dengan error yang dapat didiagnosis.

### Agent pipeline

```text
incoming event
  -> tenant validation
  -> moderation/mute gate
  -> normalize + history append
  -> per-chat batch/debounce
  -> activation + trigger decision
  -> LLM1 routing
  -> LLM2 response/tool actions
  -> permission validation
  -> durable action claim
  -> WhatsApp gateway
  -> ACK hydration + history finalize
```

Per-chat actor/goroutine boleh dipakai, tetapi harus bounded dan memiliki idle eviction. Cancellation dan restart semantics harus explicit.

### LLM providers

Gunakan OpenAI-compatible HTTP interface kecil:

- configurable endpoint/model/key/timeout;
- primary/fallback chain;
- streaming tidak diperlukan sampai behavior parity tercapai;
- preserve tool schema, prompt ordering, truncation, retries, and multimodal fallback;
- redact secrets dan content sesuai log policy;
- fake provider untuk deterministic golden tests.

### Jobs

Satu scheduler dengan dua policy berbeda:

- one-shot: delete setelah completion atau hard failure, keep pada shutdown cancellation;
- daily: calculate next local fire time dan tetap tersimpan setelah failure.

Claim due jobs secara transactional agar multi-instance tidak mengeksekusi dua kali. WhatsApp readiness menjadi execution gate, bukan trigger deletion.

### Sub-agent

State machine durable:

```text
submitted -> running -> completed_received -> outputs_staged
          -> delivery_pending -> delivered
          -> failed/dead_letter/tombstoned
```

- Webhook completion di-ACK setelah durable store, bukan setelah WhatsApp delivery.
- Output file diverifikasi size/hash dan ditulis atomically.
- Delivery checkpoint per output mencegah duplicate attachment.
- Admin retry/discard beroperasi pada delivery envelope, bukan menghapus result.

### Persistence

Awal: pertahankan split DB dan filenames untuk rollback mudah. Target state:

- versioned embedded SQL migrations;
- `schema_migrations` dan checksum;
- explicit transaction boundaries;
- busy timeout/WAL pragmas set konsisten;
- DB writer ownership tunggal;
- repositories tidak membuat schema;
- durable `action_receipts`, inbox/outbox, scheduler claims, dan sub-agent state.

Detail: [Migrasi data](04-DATA-MIGRATION.md).

### Control panel

- `net/http` cukup; gunakan dependency baru hanya jika dibutuhkan dan sudah disetujui.
- Pertahankan route dan response JSON selama compatibility period.
- Embed static assets dalam binary setelah UI parity.
- Auth middleware fail-closed, timing-safe, rate-limited.
- Mutation memakai validation, audit, dan atomic writes/transactions.
- Git self-update ditempatkan di optional deployment adapter, bukan core service.

### Observability

Structured fields minimal:

- `instance_id`, `tenant_id`, compatibility `folder_path` hash;
- `chat_id` hash bila log policy melarang raw JID;
- request/action/task/sub-agent IDs;
- adapter, status, duration, queue depth, retry count, error code.

Endpoints:

- `/health/live`: process loop hidup;
- `/health/ready`: migrations selesai dan control plane siap;
- tenant readiness terpisah untuk WS/sidecar dan WhatsApp status.

Metrics wajib: queue depth/drop, reconnects, action latency/failures/dedup, LLM latency/tokens/fallback, DB busy/recovery, jobs due/late, sub-agent delivery state, tenant state.

## Concurrency rules

- Tidak ada mutable package global untuk tenant state.
- Satu owner goroutine atau mutex jelas per registry/runtime/cache.
- Per-chat processing serialized.
- Media dan LLM memiliki global dan per-tenant semaphore.
- Semua queues bounded; overflow policy terdokumentasi.
- Background goroutine terikat lifecycle context dan WaitGroup.
- Jalankan `go test -race ./...` pada CI dan pre-cutover.

## Dependency policy

Current repository sudah memakai:

- `github.com/polymorfa/hypermeow`;
- `github.com/mattn/go-sqlite3`;
- `github.com/mdp/qrterminal`;
- indirect `github.com/coder/websocket` dan `github.com/rs/zerolog`.

Referensi: `go.mod:5-28`.

Sebelum coding:

- validasi Go version `1.26.5` tersedia di build environment;
- track `go.sum` untuk reproducible build;
- putuskan CGO requirement dari `go-sqlite3` versus pure-Go SQLite;
- pin `hypermeow` ke commit/version nyata; `v0.0.0` harus diverifikasi reproducibility dan provenance;
- jangan tambah framework dotenv/router/migration jika stdlib atau existing dependency cukup.
