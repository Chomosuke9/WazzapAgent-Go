# Arsitektur Target

## Prinsip

1. Project adalah aplikasi baru; source lama hanya referensi fitur.
2. Domain tidak bergantung langsung pada `hypermeow`, HTTP router, LLM SDK, atau SQLite driver.
3. Semua operasi menerima `TenantID` eksplisit dan tenant-scoped dependencies.
4. Native WhatsApp adapter memakai canonical request/result, bukan raw protobuf.
5. Schema versioned sejak v1 dan hanya migration package yang menjalankan DDL.
6. Side effect memakai idempotency key dan durable transactional state.
7. Semua network/DB work memakai `context.Context`, deadline, bounded queue, dan graceful cancellation.

## Topologi

```text
Clients / Control Panel
          |
       HTTP API
          |
+---------v-----------------------------------------+
| Go service                                        |
| app | accounts | agent | LLM | jobs | sub-agent  |
| stores | inbox/outbox | control | observability  |
+-------------------------+-------------------------+
                          |
                 canonical Gateway port
                          |
                 native hypermeow adapter
                          |
                       WhatsApp
```

Tidak ada Node/Python sidecar dan tidak ada protocol compatibility layer.

## Package layout

```text
cmd/
  wazzapagent/
internal/
  app/
  config/
  tenant/
  account/
  whatsapp/
    adapter/hypermeow/
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
    account/
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

Application internals berada di `internal/`. Existing prototype `pkg/whatsapp/socket.go` bukan executable karena `main` berada dalam `package whatsapp`; pindahkan hasil spike yang berguna ke `internal/whatsapp/adapter/hypermeow` dan gunakan `cmd/wazzapagent` sebagai entrypoint. Referensi: `pkg/whatsapp/socket.go:1-26`.

## Application lifecycle

Startup order:

1. load dan validasi config;
2. init logging/metrics;
3. buka database dan jalankan versioned migrations;
4. load tenant catalog;
5. start HTTP control/health endpoints;
6. start native WhatsApp clients;
7. start agent sessions setelah account ready;
8. arm jobs dan sub-agent recovery setelah WhatsApp `open`.

Shutdown:

1. stop HTTP/action intake;
2. mark runtime draining;
3. cancel pending LLM/media work;
4. persist/release job claims;
5. drain bounded outbox sampai deadline;
6. disconnect WhatsApp;
7. checkpoint dan close SQLite.

## Tenant dan account registry

```go
type TenantID string

type Runtime struct {
    ID       TenantID
    RootPath string
    Stores   Stores
    WhatsApp Gateway
    Agent    AgentSession
}
```

- Tenant ID immutable dan bukan filesystem path.
- Root dibuat oleh aplikasi di data directory.
- Registry state: `stopped`, `starting`, `pairing`, `connecting`, `open`, `draining`, `failed`.
- Per-tenant state, DB handles, queues, caches, media, dan auth store terisolasi.
- Per-tenant lock mencegah start/stop/pair/reconnect race.
- Account removal default menonaktifkan runtime; penghapusan data memerlukan aksi terpisah dan konfirmasi.

## Canonical WhatsApp port

```go
type Gateway interface {
    Status(context.Context) Status
    Events(context.Context) <-chan Event
    Send(context.Context, SendRequest) (SendResult, error)
    React(context.Context, ReactionRequest) error
    Delete(context.Context, DeleteRequest) error
    Kick(context.Context, KickRequest) (KickResult, error)
    MarkRead(context.Context, ReadRequest) error
    SetPresence(context.Context, PresenceRequest) error
    ChatContext(context.Context, ChatContextRequest) (ChatContext, error)
    Pair(context.Context, PairRequest) (PairResult, error)
    Logout(context.Context) error
    Disconnect(context.Context) error
}
```

- Adapter menerjemahkan event `hypermeow` ke canonical domain event.
- Raw protobuf hanya berada di package adapter.
- Capability flags menyatakan dukungan interactive/Lottie/message variants.
- Unsupported feature menghasilkan stable typed error dan optional fallback.
- Fake gateway dipakai unit/integration tests tanpa network.

## Message pipeline

```text
native WhatsApp event
  -> normalize and validate tenant
  -> deduplicate inbound event
  -> moderation/mute gate
  -> append history
  -> per-chat batch/debounce
  -> activation and trigger decision
  -> LLM1 routing
  -> LLM2 response/tool actions
  -> permission validation
  -> durable action claim
  -> native WhatsApp gateway
  -> finalize receipt and history
```

Per-chat actor/goroutine harus bounded dan memiliki idle eviction. Media dan LLM memakai global plus per-tenant semaphore.

## LLM providers

Gunakan OpenAI-compatible HTTP abstraction kecil:

- endpoint/model/key/timeout per role;
- primary/fallback chain;
- typed tool schema dan validation;
- bounded retry/backoff;
- multimodal request dan text-only fallback;
- secret/content redaction policy;
- fake deterministic provider untuk tests.

Streaming ditunda sampai non-streaming behavior stabil.

## Jobs

Satu scheduler dengan policy berbeda:

- one-shot diselesaikan sekali dan tetap pending bila shutdown membatalkan eksekusi;
- daily menyimpan IANA timezone, local time, dan next fire;
- transactional lease mencegah duplicate execution;
- WhatsApp readiness menunda execution tanpa menghapus job;
- retry policy dan terminal failure tercatat.

## Sub-agent

```text
submitted -> running -> completed_received -> outputs_staged
          -> delivery_pending -> delivered
          -> failed/dead_letter/tombstoned
```

- Webhook completion di-ACK setelah durable store.
- Progress memperbarui lease/keepalive.
- Output diverifikasi size/hash/path lalu ditulis atomically.
- Delivery checkpoint per output mencegah duplicate attachment.
- Admin retry/discard mengubah delivery envelope, bukan menghapus result.

## Persistence

Gunakan schema baru yang versioned sejak v1:

- migrations embedded dan immutable;
- `schema_migrations` plus checksum;
- explicit transaction boundaries;
- WAL, busy timeout, foreign keys, dan checkpoint policy;
- repositories tidak menjalankan DDL;
- transactional inbox/outbox/action receipts;
- retention untuk receipts, audit, caches, media, dan tombstones;
- backup API memakai SQLite-consistent snapshot.

Pilih single tenant database atau split database melalui ADR di Milestone 1. Tidak ada kebutuhan membaca schema project lama.

## Control panel

- Gunakan `net/http` kecuali routing complexity membuktikan library tambahan perlu.
- API baru memakai version prefix dan typed request/response.
- Static assets dapat di-embed dalam binary.
- Auth fail-closed, timing-safe, rate-limited, dan memiliki setup flow aman.
- Mutation memakai validation, transaction, audit, dan CSRF-safe auth design.
- Secrets tidak pernah dikembalikan setelah disimpan.

## Observability

Structured fields minimal:

- `instance_id`, `tenant_id`, account JID hash;
- chat JID hash sesuai privacy policy;
- request/action/job/sub-agent IDs;
- adapter status, duration, queue depth, retry, stable error code.

Endpoints:

- `/health/live`: process loop hidup;
- `/health/ready`: migrations selesai dan core services siap;
- account readiness terpisah untuk paired/connecting/open/failed.

Metrics:

- incoming/outgoing/dropped/deduplicated events;
- reconnect and pairing failures;
- action and LLM latency/errors/fallback;
- queue depth and oldest age;
- DB busy/WAL/checkpoint;
- jobs due/late/retry;
- sub-agent delivery state;
- goroutine, heap, file, dan media growth.

## Concurrency rules

- Tidak ada mutable package global untuk tenant state.
- Satu owner goroutine atau mutex jelas per registry/runtime/cache.
- Per-chat processing serialized; chat berbeda concurrent.
- Semua queue bounded dengan documented overflow policy.
- Background goroutine terikat lifecycle context dan WaitGroup.
- Blocking SDK calls dibungkus context/deadline bila library tidak mendukung langsung.
- `go test -race ./...` wajib di CI dan release gate.

## Dependency policy

Current repository memiliki:

- `github.com/polymorfa/hypermeow`;
- `github.com/mattn/go-sqlite3`;
- `github.com/mdp/qrterminal`;
- indirect `github.com/coder/websocket` dan `github.com/rs/zerolog`.

Referensi: `go.mod:5-28`.

Sebelum implementasi:

- validasi Go `1.26.5` tersedia di build environment;
- track `go.sum`;
- pilih CGO `go-sqlite3` atau pure-Go SQLite;
- pin `hypermeow` ke resolvable commit/version dan audit provenance;
- gunakan stdlib bila dependency baru tidak memberi manfaat jelas;
- dokumentasikan licenses dan update policy dependency.
