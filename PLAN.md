# Rencana Rewrite WazzapAgent ke Go

## Tujuan

Membangun WazzapAgent baru sebagai modular monolith Go yang dapat bertumbuh dari satu account menjadi banyak tenant tanpa mengulang arsitektur lama. Project `../wazzapagents/wazzapagent` hanya menjadi referensi perilaku produk, edge case, dan skenario pengujian.

Rewrite bersifat greenfield:

- tidak mengimpor database, config, JSON state, media, audit, atau auth Baileys lama;
- tidak mempertahankan protocol WebSocket Node/Python lama;
- tidak menjalankan Node atau Python sebagai sidecar;
- setiap account melakukan pairing baru;
- setiap Part menghasilkan vertical slice yang dapat dijalankan dan diuji;
- fitur Part berikutnya menambah capability melalui port baru, bukan membongkar core Part sebelumnya.
- selama status masih V0, backward compatibility tidak boleh ditambahkan tanpa persetujuan eksplisit; breaking change dan reset data diperbolehkan.

## Arti Part dan Release

- **Part** adalah increment produk yang deployable, bukan layer teknis.
- **Part 0** hanya persiapan, eksplorasi, keputusan, spike, dan rencana.
- **Part 1** adalah barebone production canary, target hari ini.
- **Part 2** adalah reliable conversation core; implementasi lokal selesai 2026-09-09 dan real-device canary masih harus dibuktikan.
- Lulus Part 1 tidak berarti stable release. Artifact diberi label `v0.1-canary`.
- Stable release baru ditetapkan setelah reliability, security, recovery, dan soak gate pada Part 9 lulus.
- Setiap Part memiliki scope, non-scope, exit gate, kill switch, dan rollback sendiri.

## Pelajaran dari Project Lama

Project lama membagi ownership antara Node/Baileys dan Python bridge. Akibat yang tidak boleh disalin:

- dua runtime memiliki ownership state dan SQLite yang tumpang tindih;
- incoming message dan control event dapat hilang ketika bridge terputus;
- satu event WhatsApp ditangani beberapa listener yang dapat interleave;
- tenant routing dan cache memakai mutable global state;
- model-generated command terlalu dekat dengan authority owner/admin;
- history, target ID, dan sebagian queue bersifat volatile;
- orchestrator tumbuh menjadi file besar dengan banyak state machine;
- control panel menjangkau implementation detail internal secara langsung.

Rewrite memakai satu ownership model, satu pipeline inbound, durable state untuk correctness, explicit principals, dan dependency yang diarahkan ke domain/application ports.

# Desain Sistem

## Bentuk Sistem

Gunakan satu binary modular monolith:

```text
WhatsApp
   |
Hypermeow adapter
   |
single inbound pipeline
   |
inbound handler -> identity / dedup / senderRef
   |
AgentRegistry.AgentFor(TenantID, AccountID, ChatID)
   |
Config.Refresh -> external trigger / authorization
   |
Agent(chat).Invoke(...)
   +-- Config()  -> durable versioned config
   +-- History() -> durable history mulai Part 2
   +-- TurnStore -> durable invocation claim / response plan
   +-- ModelInvoker -> OpenAI adapter
   +-- ResponseDispatcher -> action outbox -> action worker -> Hypermeow

HTTP health / future control API -> application services
SQLite adapter                 -> consumer-owned store ports
```

Microservice tidak diperlukan sekarang. Batas modul tetap dibuat agar tenant runtime, action worker, HTTP control plane, atau application database dapat diekstrak kemudian tanpa mengubah domain contract.

## OOP Gaya Go

- Gunakan `struct` untuk state dan method untuk menjaga invariant/state transition.
- Gunakan composition, bukan inheritance.
- Satu live `Agent` mewakili satu `(TenantID, AccountID, ChatID)`.
- `Agent` menjadi façade untuk `Invoke`, `Config`, dan `History` chat tersebut; jangan menambah public status API sebelum use case-nya jelas.
- `AgentRegistry` membuat Agent secara lazy, coalesces concurrent creation, dan melakukan idle eviction tanpa menghapus durable state.
- Constructor menolak dependency atau state yang tidak valid.
- Field dibuat private hanya bila ada invariant yang perlu dijaga; plain immutable records boleh sederhana.
- Interface didefinisikan oleh consumer dan dibuat kecil.
- Jangan membuat generic `Manager`, service locator, atau `Runtime` yang menyimpan seluruh service.
- Jangan membuat getter/setter untuk setiap field hanya agar terlihat OOP.
- Jangan memakai generic CRUD repository; store port mengikuti use case dan transaction boundary.
- Actor verification, trigger, dan authorization berada di application/policy layer di luar Agent.
- Agent tetap memvalidasi domain invariant dan consistency transition.
- Go tidak memiliki automatic field `onChange`; mutation memakai explicit method, durable refresh, externally-authorized expected version, durable CAS, atomic snapshot swap, lalu best-effort typed post-commit notification.

Contoh narrow ports:

```go
// package agent
type ModelInvoker interface {
    Generate(context.Context, ModelRequest) (ModelResult, error)
}

type ResponseDispatcher interface {
    Dispatch(context.Context, DispatchRef) (DeliveryResult, error)
}

type ConfigStore interface {
    LoadOrCreate(context.Context, Key, ConfigValues) (ConfigSnapshot, error)
    Load(context.Context, Key) (ConfigSnapshot, error)
    CompareAndSwap(context.Context, Key, ConfigVersion, ConfigValues) (ConfigSnapshot, error)
}

// package action
type TextSender interface {
    SendText(context.Context, SendTextRequest) (SendTextResult, error)
}
```

Satu `hypermeow.Adapter` boleh mengimplementasikan account connector, event source, dan text sender. Consumer tidak bergantung pada satu interface `Gateway` besar.

## Package Layout Bertahap

Hanya buat package ketika Part yang membutuhkannya dikerjakan:

```text
cmd/
  wazzapagent/

internal/
  app/                    # composition root dan process lifecycle
  config/                 # immutable validated startup config
  observability/          # logger, metrics, tracing helpers
  identity/               # semantic TenantID, ChatID, MessageID, SenderRef
  account/                # account lifecycle dan state machine
  conversation/           # canonical message, sender ref, content parts
  agent/                  # chat-scoped Agent, Registry, Config, History, Invoke
  inbound/                # dedup, trigger, allowlist, invoke application handler
  policy/                 # actor resolution dan authorization di luar Agent
  action/                 # intent, outbox, receipt, executor
  command/                # external command handlers; authorization before Agent
  adapters/
    whatsapp/hypermeow/   # raw protobuf/JID hanya di sini
    llm/openai/           # provider DTO hanya di sini
    sqlite/               # migrations dan implementasi store ports
  transport/
    httpapi/              # health sekarang, control API kemudian
```

Aturan import:

```text
identity <- account/conversation/agent/action/policy
conversation <- inbound/agent
agent <- inbound/command
policy <- inbound/command/action
consumer ports <- adapters
app -> semua concrete constructor untuk wiring
```

- `identity`, domain model, dan state machine hanya bergantung pada standard library.
- Adapter boleh mengimpor package domain/application yang port-nya diimplementasikan.
- Domain/application tidak boleh mengimpor Hypermeow, provider SDK, HTTP router, atau concrete SQLite type.
- `internal/app` boleh mengimpor semuanya, tetapi hanya untuk wiring dan lifecycle.
- Tidak ada package `utils`, `helpers`, `common`, atau `domain` sebagai tempat membuang kode campuran.

## Ownership

| Concern | Satu owner |
|---|---|
| Process startup/shutdown | `internal/app` |
| Account connect/pair/reconnect state | `internal/account` |
| Raw WhatsApp/JID/protobuf translation | Hypermeow adapter |
| Inbound dedup, allowlist, trigger, actor resolution | `internal/inbound` + `internal/policy` |
| Live Agent uniqueness dan idle eviction | `internal/agent.Registry` |
| Per-chat invocation ordering, config, history, model orchestration | `internal/agent.Agent` dan child objects |
| Outbound intent, receipt, retry policy | `internal/action` |
| App schema dan transaction | SQLite adapter |
| Hypermeow device schema | Hypermeow sqlstore |

Tidak boleh ada dua module yang dapat mengubah state machine atau schema yang sama.

## Tenant dan Scale Model

- Semua operasi stateful menerima `TenantID` eksplisit.
- `TenantID` bukan path, slot number, atau WhatsApp JID.
- Provider JID dan message ID disimpan sebagai adapter mapping, bukan domain authority.
- Semua row application state memiliki tenant scope dan composite uniqueness yang menyertakan tenant.
- Semua path berasal dari validated tenant data root, tidak dari input bebas.
- Tidak ada mutable package global untuk tenant, cache, prompt, DB, atau provider config.
- Satu `AccountRuntime` hanya memiliki state lifecycle, cancellation, dan resource account itu sendiri.
- Product surface Part 1 hanya satu account, tetapi isolation harness selalu memakai minimal dua tenant.

Evolusi scale:

1. Satu process, satu active account, bounded concurrency.
2. Beberapa `AccountRuntime` dalam satu process dengan resource limit per tenant.
3. Tenant dapat di-shard antarprocess melalui explicit account ownership/lease.
4. Application store dapat diganti PostgreSQL dan internal wake-up dapat diganti message broker melalui port yang sama.
5. Hypermeow device store tetap dimiliki process yang memegang lease account tersebut.

Tidak ada langkah di atas yang dikerjakan sebelum ada kebutuhan atau pengukuran. Contract dan ownership-nya disiapkan sejak Part 1.

## Concurrency dan Backpressure

- Satu bounded inbound queue untuk membatasi memory.
- Chat yang sama selalu serial; chat berbeda dapat concurrent.
- Part 1 memakai satu lazy Agent per active chat; Agent memiliki invocation gate tanpa goroutine permanen.
- Registry memiliki hard active-Agent limit, pins in-flight Agent, dan idle-evicts Agent yang tidak aktif.
- LLM memakai global semaphore dan timeout.
- Outbound send per chat/JID serial.
- Setiap background goroutine terikat root context dan `WaitGroup`.
- Queue penuh menghasilkan backpressure atau typed `resource_exhausted`; event durable tidak boleh diam-diam dibuang.
- `go test -race ./...` menjadi gate setiap Part.

## Persistence

Per tenant:

```text
<data-root>/tenants/<tenant-id>/app.db
<data-root>/tenants/<tenant-id>/whatsapp.db
```

- `app.db` dimiliki aplikasi.
- `whatsapp.db` dimiliki Hypermeow sqlstore.
- Tidak ada asumsi transaksi atomik lintas kedua database.
- Migration immutable, embedded, versioned, dan memiliki checksum.
- Port persistence mengikuti transaction use case seperti `ClaimInbound`, `ResolveSenderRef`, dan `EnqueueText`; bukan CRUD generik.
- SQLite cocok untuk single-node. Store port menjaga opsi PostgreSQL ketika horizontal scale benar-benar dibutuhkan.

# Roadmap per Part

## Part 0 — Persiapan dan Rencana

### Tujuan

Menentukan scope, boundary, risiko, dependency, quality gate, dan delivery sequence sebelum fitur produk dibangun.

### Termasuk

- audit sistem lama dan daftar perilaku yang perlu dipertahankan;
- keputusan greenfield, single binary, modular monolith, dan fresh pairing;
- target architecture, package boundary, ownership, dan scale evolution;
- normative chat-scoped Agent contract, Config/History child objects, Registry lifecycle, and external authorization boundary;
- typed config, structured logger, lifecycle, health endpoint, dan CI skeleton;
- spike modernc/Hypermeow device store;
- scope dan canary runbook Part 1;
- daftar fitur Part 2–9 tanpa speculative implementation.

### Tidak termasuk

- bot yang menjawab pesan nyata;
- pairing yang dianggap production-ready;
- history, media, tool calling, scheduler, sub-agent, atau control panel.

### Exit gate

- roadmap dan scope Part 1 disetujui;
- architecture rules dan ownership tertulis;
- barebone acceptance test tertulis sebelum implementasi;
- foundation lokal dapat format, vet, test, race, dan build;
- spike tidak diperlakukan sebagai production adapter.

### Status 2026-09-08

- Audit dan keputusan awal: selesai.
- Executable/config/logger/health/lifecycle skeleton: tersedia lokal.
- Modernc/Hypermeow sqlstore spike Windows: tersedia lokal.
- Roadmap Part-based dan Part 1 canary scope: diperbarui.
- Agent-centric contract pada `docs/rewrite/04-AGENT-CONTRACT.md`: diperbarui sesuai keputusan actor authorization di luar Agent.
- Current foundation validation: `gofmt -l` bersih; `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./cmd/...`, dan `go mod verify` lulus.
- `govulncheck` Part 1 kemudian dijalankan pada 2026-09-08; tidak ada vulnerable symbol/package yang reachable.
- Native real-device verification dan deployment: belum dilakukan oleh perubahan rencana ini.

## Part 1 — Barebone Production Canary

### Target

Target hari ini adalah satu alur end-to-end yang aman untuk account test:

```text
pair/connect
  -> receive one text message
  -> normalize identity
  -> durable inbound claim
  -> resolve stable senderRef
  -> AgentRegistry.AgentFor(chat)
  -> Config.Refresh + external trigger/allowlist decision
  -> bind decision to Config version
  -> Agent.Invoke(text invocation)
       -> verify policy version + durable InvocationID/digest claim
       -> replay stored plan OR capture refreshed Agent.Config snapshot
       -> one text-only LLM request
       -> atomic response/outbound-action plan
       -> durable ResponseDispatcher for stored ActionID
       -> send WhatsApp text
       -> finalize receipt
```

### Fitur wajib

1. **Runtime**
   - satu active tenant/account pada product surface;
   - fresh QR atau pairing-code flow, minimal salah satu yang lulus real-device test;
   - persistent Hypermeow session dan reconnect setelah process restart;
   - graceful shutdown, health live/ready, structured redacted logs.
2. **Pesan**
   - incoming dan outgoing text saja;
   - eligible DM dijawab;
   - group hanya dijawab ketika bot di-mention;
   - pesan dari bot sendiri, status/broadcast, unsupported wrappers, dan duplicate event diabaikan dengan alasan tercatat;
   - tidak ada WhatsApp quoted reply requirement pada Part 1.
3. **senderRef**
   - opaque reference per `(tenant, chat, participant)`;
   - dibuat random dari `crypto/rand`, human-readable, dan disimpan durable;
   - unique constraint menangani collision dengan retry;
   - stabil setelah restart/restore;
   - JID/nomor telepon tidak dikirim ke model dan tidak dicetak ke log normal;
   - `senderRef` hanya presentation handle, bukan bukti role atau authority.
4. **Prompt**
   - non-overridable application safety/system instruction;
   - versioned `Agent.Config` snapshot per chat di `app.db`;
   - snapshot memiliki credential-free `Model`, configurable base `Prompt`, durable per-chat `PromptOverride`, dan immutable `Permission` policy reference/revision;
   - hanya command `/prompt`, dengan operasi `view`, `set`, dan `clear`;
   - `/prompt set|clear` hanya mengubah `PromptOverride`; base/safety prompt tidak tersentuh;
   - external application handler melakukan `Config.Refresh`, memverifikasi configured bot owner, lalu memanggil actor-free `Agent.Config()` mutation;
   - `/prompt` diproses deterministik dan tidak pernah diteruskan ke LLM;
   - Config mutation mengikat external authorization ke expected snapshot version, lalu memakai durable compare-and-swap, atomic snapshot swap, dan best-effort `ConfigChanged` notification;
   - user text, authenticated opaque senderRef, untrusted display name, dan system prompt tetap menjadi model message/data yang terpisah.
5. **LLM**
   - satu OpenAI-compatible text provider;
   - non-streaming, satu model, timeout, response-size limit, dan bounded retry hanya untuk failure yang aman;
   - plain text response saja;
   - tool calls, model-generated commands, image, history, fallback chain, dan streaming ditolak pada Part 1.
6. **Minimum durability**
   - embedded migration untuk tenant/chat mapping, participant/sender refs, prompts, inbound claims, outbound text actions, dan receipts;
   - inbound provider identity memiliki unique dedup key;
   - TurnStore mengikat unique `(AgentKey, InvocationID)` ke canonical digest dan generation lease;
   - response text, response/action IDs, outbound intent, dan `response_planned` disimpan atomically;
   - replay setelah plan memakai response/action yang sama dan tidak memanggil LLM lagi;
   - LLM call tidak dilakukan di dalam DB transaction;
   - outbound intent disimpan sebelum send;
   - send dengan outcome tidak diketahui tidak diulang otomatis;
   - restart dapat melanjutkan event/action yang masih aman untuk dilanjutkan.
7. **Bounds**
   - inbound text maksimal 32 KiB;
   - prompt maksimal 16 KiB;
   - response text memiliki configurable hard limit;
   - bounded inbound queue;
   - per-chat serialization;
   - global LLM concurrency limit;
   - deadline untuk DB, LLM, pairing, connect, dan send.

### Schema minimum Part 1

```text
schema_migrations
tenants
accounts
chats
participants
sender_refs
agent_configs
inbound_events
outbound_actions
action_receipts
```

`agent_configs` menggantikan `chat_prompts`; jangan membuat keduanya. Jangan membuat table history, media, scheduler, sub-agent, sticker, moderation, atau control-panel session pada Part 1.

### Urutan implementasi hari ini

1. **Freeze contracts:** implement contract pada `docs/rewrite/04-AGENT-CONTRACT.md`: Agent key, Registry, Config, generic causation, idempotent TurnStore, ResponseDispatcher, sender ref, typed errors, dan narrow ports.
2. **Minimum store:** embedded migrations serta transaction untuk inbound/InvocationID claim, sender ref, versioned Agent config, atomic response/action plan, dan receipt.
3. **Fake vertical slice:** fake inbound -> AgentRegistry -> Config.Refresh/external policy -> Agent.Invoke -> TurnStore -> fake model -> real durable dispatcher -> fake sender.
4. **Native vertical slice:** pindahkan spike ke internal Hypermeow adapter; pair/connect, canonical text source, dan text sender.
5. **LLM vertical slice:** OpenAI-compatible text adapter, request separation, timeout, output validation, dan redaction.
6. **Recovery/concurrency:** duplicate replay, unknown outcome, per-Agent invocation serialization, bounded AgentRegistry/queue/semaphore, graceful shutdown.
7. **Canary hardening:** allowlist, safe normal defaults, readiness, metrics, backup, optional kill switch, dan rollback probe.

Jalankan tests setelah setiap langkah. Jangan mengerjakan history, media, generic command framework, atau control panel untuk mempercepat Part 1.

### Non-scope Part 1

- conversation history dan contextMsgId;
- batching/debounce;
- replied-to-bot dan quoted reply;
- commands selain `/prompt`;
- reaction, delete, read, presence, kick, atau generic tool/action;
- image, video, audio, document, sticker, interactive message;
- multi-account product surface;
- scheduler, direct invoke, sub-agent, control panel;
- migration data/auth lama;
- stable production release atau penggantian service lama.

### Tests sebelum canary

```text
gofmt -l .  # output wajib kosong
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/...
govulncheck ./...
```

Wajib ada test untuk:

- senderRef collision, persistence, tenant/chat isolation, dan tidak bocor JID;
- prompt owner authorization, size limit, set/view/clear, dan restart;
- same InvocationID/digest setelah planning tidak memanggil LLM atau membuat response/action kedua;
- same InvocationID dengan digest berbeda fail closed;
- duplicate action key tidak menghasilkan send kedua;
- dropped/reordered Config notification diperbaiki durable Refresh;
- cancellation/timeout dan graceful shutdown;
- same-chat serial dan cross-chat concurrent;
- malformed/empty/oversized LLM response;
- secret dan message content tidak masuk log normal.

### Production canary gate hari ini

Canary hanya boleh dimulai bila:

- memakai dedicated WhatsApp test account, bukan account service lama;
- memakai data directory, port, process/service, dan log terpisah;
- recipient/chat allowlist wajib dan fail-closed;
- response agent aktif pada jalur normal; dedicated account dan allowlist sempit wajib benar sebelum pairing;
- health dan account-ready dapat dibedakan;
- operator memiliki kill switch satu langkah;
- service lama tidak dihentikan atau dimodifikasi;
- backup awal `app.db` dan `whatsapp.db` dibuat setelah pairing;
- error, reconnect, queue depth, LLM latency, dan duplicate count diamati;
- minimal DM probe, group-mention probe, prompt set/clear, restart, dan duplicate replay lulus.

Part 1 startup config membuat opaque tenant/account identity sekali lalu menyimpannya di data root; environment tidak mengoverride identity durable. Owner identity, LLM endpoint/model/key, timeout/limit, dan canary allowlist tetap divalidasi saat runtime WhatsApp aktif. Jalur normal mengaktifkan WhatsApp dan Agent serta menampilkan QR session baru di terminal tanpa tiga environment switch; override disable tetap tersedia sebagai kill switch. Redacted config tidak boleh memuat ID mentah, secret, raw address, prompt, atau message content.

Canary hari ini membuktikan alur dan boundary. Canary tidak membuktikan long-run reliability. Jika gate ini belum lulus, Part 1 tetap belum selesai walaupun binary berhasil build.

### Rollback canary

1. Disable agent/stop process Go.
2. Jangan menghapus data directory; simpan untuk diagnosis.
3. Pastikan service lama tetap berjalan pada account dan path terpisah.
4. Jika WhatsApp session bermasalah, logout hanya account test.
5. Catat inbound/action terakhir dan state receipt sebelum restart berikutnya.

### Definition of Done Part 1

- barebone end-to-end lulus dengan fake adapter dan fake LLM;
- real dedicated account dapat pair, reconnect, menerima, dan mengirim text;
- senderRef dan per-chat prompt survive restart;
- duplicate/replay test tidak membuat planned duplicate response;
- canary allowlist, kill switch, dan rollback telah dicoba;
- hasil canary dilaporkan sebagai canary, bukan stable production release.

### Status implementasi 2026-09-08

- Contract, SQLite store, fake vertical slice, native Hypermeow adapter, OpenAI-compatible adapter, recovery, bounds, metrics, content scrubbing, dan operator runbook: implemented pada working tree.
- Local verification lulus pada Go 1.27.0; test dan vet juga lulus pada minimum Go 1.26.5. Gate mencakup format, module tidy/verify, seluruh test, race detector, `CGO_ENABLED=0` build Windows amd64/Linux amd64/Linux arm64/Android arm64, dan `govulncheck` reachable-symbol scan.
- Binary Windows juga lulus process smoke untuk `/health/live`, `/health/ready`, dan `/metrics` dengan WhatsApp/agent disabled; ini bukan native WhatsApp test.
- Real-device pairing/reconnect serta production-host canary: belum dijalankan karena dedicated account/config/host tidak tersedia pada checkout ini.
- Karena exit gate real-device belum terbukti, status release tetap **Part 1 implementation ready for canary**, bukan Part 1 production-canary complete dan bukan stable release.

## Part 2 — Reliable Conversation Core

Menambah kemampuan percakapan tanpa tool berbahaya:

- full durable transcript untuk chat allowlisted, dengan bounded model context dan retention;
- pesan group pasif disimpan tanpa memicu invocation; inbound text/sticker diberi sequence, timestamp, senderRef, dan quote provenance;
- `Agent.History().List/Append/Reset/Trim` child-object implementation dengan authorized Config-version guard untuk read/reset;
- stable internal message ID dan quoted-message mapping;
- replied-to-bot trigger;
- per-chat batching/debounce dan burst cap;
- context builder deterministic dan golden tests;
- prompt injection boundary dan structured provenance;
- `/help`, `/info`, owner-only `/dump` untuk inspeksi exact Agent input, dan `/reset`;
- action reconciliation, backup/restore, retention, dan richer metrics;
- network-loss, process-kill, replay, dan concurrent-chat tests.

Exit: percakapan text survive restart, ordering benar, dan tidak bergantung pada in-memory history.

## Part 3 — Permission, Commands, dan Typed Actions

- principal eksplisit untuk human, model, system, dan recovery; scheduler tetap scope Part 6;
- narrow current-chat authority lookup dan permission recheck sebelum native side effect;
- command registry eksplisit dan `/permission [view|0|1|2|3]` untuk level moderasi per chat;
- `reply_message` membawa visible text dan optional command keluarga `/group *`; `react_to_message` adalah model tool kedua yang selalu aktif;
- level 0 tidak memberi moderasi, level 1 mengizinkan delete, level 2 menambah mute, dan level 3 menambah kick;
- mark-read serta composing/paused presence adalah perilaku runtime otomatis, bukan model tool atau permission;
- command/effect model baru dieksekusi setelah text response invocation yang sama berhasil terkirim;
- provider fallback dan bounded retry policy;
- model hanya dapat membawa `/group delete|mute|kick` sesuai level dan tidak pernah memperoleh human authority.

Exit: semua side effect melewati validation, authorization, durable claim, dan receipt state machine.

### Status implementasi 2026-09-10

- Contract `reply_message`/`react_to_message`, command `/permission` level 0–3, automatic mark-read/presence, fallback model, serta typed group-command effect outbox/recovery: implemented dan durable di SQLite.
- Authorization command tetap berada di application/policy layer. Agent core menerima capability snapshot yang sudah diotorisasi dan tidak menerima actor atau permission DTO.
- Effect model re-check capability, allowlist, policy revision, dan current chat authority tepat sebelum edge WhatsApp. Native effect tidak dapat berjalan sebelum response text invocation yang sama sukses.
- Media, scheduler, dan command execution di luar `/group delete|mute|kick` belum dibawa ke Part 3.
- Local gate lulus: `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./cmd/wazzapagent`, dan `git diff --check`.
- Real-device canary untuk capability model masih pending; statusnya **Part 3 implementation complete locally**, bukan production-canary complete.

## Part 4 — Media dan Rich Context

- image receive, lazy materialization, vision input, text-only fallback, dan image send;
- MIME sniffing, byte/pixel limit, hash, timeout, quota, dan cleanup;
- reply/mention rendering dan LID/phone mapping hardening;
- document/audio/video ditambahkan satu per satu hanya setelah capability/security gate.

Exit: media tidak membawa raw path/provider object ke domain/model dan tidak membuka SSRF/path traversal.

## Part 5 — Multi-account dan Control Plane

- beberapa active `AccountRuntime` dengan explicit resource budget;
- account registry dan ownership state machine;
- two-tenant real runtime isolation;
- versioned control API dan embedded UI;
- server-side session cookie, CSRF protection, rate limit, audit;
- pairing/reconnect/logout/disable/delete-data, prompt/model, backup/restore operations.

Exit: account tidak dapat membaca path, prompt, senderRef, message, action, atau secret account lain.

## Part 6 — Scheduler dan Direct Invoke

- shared cold invocation path;
- one-shot dan daily task dengan IANA timezone;
- transactional lease, readiness gate, retry, cancellation, dan audit;
- authenticated direct invoke API;
- explicit limited scheduler/direct-invoke principal, bukan fake owner.
- typed `CausationRef` untuk task/direct request, bukan memalsukan MessageID.

Exit: restart/timezone/lease tests tidak menggandakan execution dan cold invoke memakai live chat context.

## Part 7 — Sub-agent

- durable `SubagentJob` state machine;
- authenticated submission/callback;
- content-addressed input/output dengan size/hash validation;
- progress, steering, correction, cancellation, retry/dead-letter;
- delivery melalui action outbox yang sama;
- wait tidak memegang chat lock.

Exit: crash pada setiap transition tidak menghilangkan completion atau menggandakan file delivery.

## Part 8 — Advanced WhatsApp Features

- sticker, buttons, carousel, quiz, copy-code, Lottie, dan safe HTML rendering;
- group moderation, kick, broadcast, announcement;
- view-once, ephemeral, edited, dan uncommon wrappers;
- capability flags dan text/media fallback per client/platform.

Setiap feature memiliki adapter capability gate dan tidak boleh memakai raw-protobuf workaround di domain.

## Part 9 — Scale, Hardening, dan Stable Release

- full security review dan dependency/license inventory;
- load, fault injection, backup/restore, migration, dan 24+ hour soak;
- resource budgets per tenant dan noisy-neighbor tests;
- optional tenant sharding/lease hanya bila benchmark membuktikan perlu;
- Debian/Windows/Termux release artifacts, checksums, runbook, alerting;
- stable release sign-off.

Exit: zero tenant isolation violation, bounded resource growth, recovery teruji, dan artifact berjalan tanpa Node/Python.

# Critical Path

```text
Part 0  Plan dan architecture
  -> Part 1  Barebone canary hari ini
  -> Part 2  Reliable conversation
  -> Part 3  Permission dan typed actions
  -> Part 4  Media/rich context
  -> Part 5  Multi-account/control plane
  -> Part 6  Scheduler/direct invoke
  -> Part 7  Sub-agent
  -> Part 8  Advanced WhatsApp
  -> Part 9  Stable release hardening
```

Setiap Part harus tetap deployable. Part berikutnya tidak boleh menonaktifkan test atau reliability guarantee Part sebelumnya.

# Global Stop Conditions

Hentikan rollout bila:

- state/path/data melintasi tenant;
- session test account mengganggu account/service lama;
- secret, raw JID, prompt, atau message content bocor ke log normal;
- duplicate atau unknown outbound outcome diulang tanpa policy;
- queue, goroutine, WAL, file, atau memory tumbuh tanpa batas;
- permission bergantung pada claim dari model;
- startup/reconnect loop tidak bounded;
- database integrity atau migration checksum gagal;
- rollback/kill switch tidak bekerja.

# Definition of Done Setiap Part

- scope dan non-scope jelas;
- domain/application tidak mengimpor concrete adapter;
- dependency diberikan lewat constructor;
- test memakai fake pada port kecil, bukan mock seluruh runtime;
- format, vet, unit, integration, race, build, dan relevant security gates lulus;
- config, secret, queue, timeout, retry, dan resource bounds terdokumentasi;
- deployment/canary dibedakan jelas dari local verification;
- docs dan implementation status diperbarui tanpa mengklaim test yang belum dijalankan.
