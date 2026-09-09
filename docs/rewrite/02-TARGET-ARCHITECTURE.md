# Arsitektur Target

## Sasaran

Membangun modular monolith Go yang sederhana untuk Part 1, tetapi memiliki ownership, contracts, tenant isolation, durability, dan concurrency model yang dapat berkembang tanpa mengulang monolit Node/Python lama.

Scalable pada dokumen ini berarti:

- capability dapat ditambah tanpa mengubah adapter/domain lain;
- account dan tenant tidak berbagi mutable state secara implisit;
- load dibatasi dan dapat diukur;
- state penting survive restart;
- module dapat diekstrak ketika ada bukti kebutuhan;
- bukan berarti membuat microservices, broker, atau generic framework sejak awal.

## Prinsip

1. Satu binary dan satu process owner sejak Part 1.
2. Setiap event hanya melewati satu inbound pipeline.
3. Setiap state/schema memiliki tepat satu owner.
4. Domain/application tidak bergantung pada Hypermeow, HTTP router, LLM SDK, atau concrete SQLite types.
5. Interface didefinisikan oleh consumer dan sekecil capability yang dipakai.
6. Semua operasi stateful menerima explicit `TenantID`.
7. Correctness state disimpan durable sebelum external side effect.
8. User/model input tidak dapat membuat principal, role, tenant, target, atau idempotency key sendiri.
9. Semua queue, concurrency, retry, data size, retention, dan goroutine memiliki batas.
10. Setiap Part menyelesaikan vertical slice dan mempertahankan guarantee Part sebelumnya.

## Topologi

```text
                         +--------------------------+
WhatsApp <-> Hypermeow   | account connector        |
            adapter ---->| canonical message source |
                         +------------+-------------+
                                      |
                                inbound.Handler
                                      |
                       identity + dedup + senderRef
                                      |
                   AgentRegistry.AgentFor(chat key)
                                      |
                       Config.Refresh + external policy
                                      |
                               Agent(chat).Invoke
                         /            |             \
                     Config        History         ModelInvoker
                       |           Part 2               |
                 SQLite CAS       SQLite          LLM adapter
                         \            |             /
                         TurnStore + ResponseDispatcher
                                      |
                         durable outbox / action worker
                                      |
                              Hypermeow adapter

HTTP/command -> identity -> Agent lookup + Config.Refresh -> external authorization -> Agent child method
app composition root -> wires every concrete dependency and owns lifecycle
```

No internal WebSocket is required. No Node or Python sidecar exists.

## Package layout

```text
cmd/
  wazzapagent/

internal/
  app/
  config/
  observability/
  identity/
  account/
  conversation/
  agent/
  inbound/
  policy/
  action/
  command/                    # Part 2/3
  adapters/
    whatsapp/hypermeow/
    llm/openai/
    sqlite/
  transport/
    httpapi/
```

Jangan membuat semua folder di depan. Folder dibuat ketika mempunyai implementation atau contract yang dipakai.

### Package responsibilities

| Package | Responsibility | Tidak boleh memiliki |
|---|---|---|
| `app` | composition, startup/shutdown ordering | business rules, SQL, raw provider event |
| `identity` | semantic internal IDs and validation | provider JID parsing, DB access |
| `account` | pairing/connect/reconnect lifecycle state | conversation/LLM logic |
| `conversation` | canonical message, senderRef, content parts | raw protobuf, LLM DTO, authorization |
| `agent` | chat-scoped Agent, Registry, Config, History, Invoke | actor authorization, raw WhatsApp/SQL |
| `inbound` | durable claim, allowlist/trigger, Agent selection | LLM/provider internals |
| `policy` | trusted actor resolution and authorization outside Agent | model orchestration, SQL/HTTP DTO |
| `action` | intent, receipt, execution and final policy recheck | model prompt, HTTP handler |
| adapters | external protocol/storage implementations | cross-feature orchestration |
| `httpapi` | request parsing/auth/response mapping | direct SQL/Hypermeow access |

Tidak ada package `utils`, `helpers`, `common`, atau mega-package `domain`. Shared type hanya masuk `identity` bila benar-benar stabil dan semantic.

## Dependency direction

```text
identity <- account
identity <- conversation
identity <- agent
identity <- action
conversation/agent/policy <- inbound
agent/policy <- command
agent/action/policy <- app use cases
consumer ports <- adapters
app -> concrete constructors
```

Rules:

- `app` adalah satu-satunya composition root.
- Adapter mengimpor contract yang diimplementasikan; contract tidak mengimpor adapter.
- Interface berada di package yang memakai capability tersebut.
- Tidak ada global registry untuk DB, account, prompt, model, cache, atau tenant.
- Tidak ada service locator seperti `Runtime.Services.Get(...)`.
- Constructor menerima dependency secara eksplisit dan fail-fast bila nil/invalid.
- Transport/application handlers verify actor and permission before calling Agent mutation methods.
- Agent methods do not accept actor for authorization decisions.

## OOP model

Go object digunakan ketika state memiliki invariant atau transition.

Contoh object yang memiliki behavior:

- `account.Runtime`: `Start`, `BeginPairing`, `MarkOpen`, `Drain`, `Stop`, `Fail`;
- `agent.Registry`: lazy per-chat construction, coalescing, pinning, idle eviction;
- `agent.Agent`: per-chat `Invoke`, config/history access, invocation serialization;
- `agent.Config`: immutable snapshots, durable CAS mutations, post-commit change events;
- `agent.History`: list/append/reset/trim mulai Part 2;
- `action.Receipt`: `Claim`, `Start`, `Succeed`, `Fail`, `MarkUnknown`;
- `conversation.SenderRef`: validated opaque value;

Plain records tetap sederhana:

- `IncomingCandidate`;
- `IncomingMessage`;
- `ModelRequest`;
- `SendTextRequest`;
- `PromptSnapshot`.

Method mutating hanya tersedia pada aggregate owner. Store merekonstruksi object melalui validated constructor, bukan mengubah field internal secara acak.

Kontrak normatif object, mutation, authorization boundary, `onChange`, dan concurrency berada di [Kontrak Agent-centric](04-AGENT-CONTRACT.md).

## Identifiers

```go
type TenantID string
type AccountID string
type ChatID string
type ParticipantID string
type MessageID string
type ActionID string
type CorrelationID string
```

- Durable IDs memakai canonical lowercase UUIDv7.
- Defined Go types mencegah accidental ID mixing.
- Empty/malformed values ditolak pada boundary/constructor.
- `TenantID` bukan path, JID, phone number, or process slot.
- Provider addresses/IDs disimpan dalam adapter mapping table.
- External values tidak pernah menjadi authorization fact tanpa trusted resolution.

## SenderRef

`SenderRef` adalah privacy-preserving display handle, bukan entity ID atau principal.

```go
type SenderRef struct {
    value string
}

func ParseSenderRef(string) (SenderRef, error)
func (r SenderRef) String() string
```

Part 1 format recommendation:

```text
u_<8 Crockford Base32 characters>
```

Rules:

- generated dengan `crypto/rand` saat participant pertama terlihat pada chat;
- scoped oleh `(tenant_id, chat_id, participant_id)`;
- unique di `(tenant_id, chat_id, sender_ref)`;
- generation dan claim dilakukan dalam transaction dengan collision retry;
- mapping survive restart/backup restore;
- raw JID/phone tidak menjadi input generation dan tidak masuk model/log normal;
- later mention/action resolution memakai durable mapping;
- role/owner/admin selalu berasal dari current trusted identity/role resolver, never from sender ref.

## Canonical Part 1 message

```go
type IncomingMessage struct {
    ID          identity.MessageID
    TenantID    identity.TenantID
    ChatID      identity.ChatID
    SenderID    identity.ParticipantID
    SenderRef   SenderRef
    SenderName  string
    ChatKind    ChatKind
    Text        string
    MentionsBot bool
    FromMe      bool
    Owner       bool
    OccurredAt  time.Time
    ReceivedAt  time.Time
}
```

`Owner` is a trusted snapshot created after adapter address resolution and configured-owner matching. It is accepted only for the fixed Part 1 `/prompt` policy. Later Parts replace this narrow rule with an explicit current-role authorizer before every privileged effect.

The canonical record never contains:

- raw protobuf/provider DTO;
- raw JID/phone number;
- local filesystem path;
- model-provided role/tenant/target;
- quoted/media/history fields that Part 1 does not support.

## Consumer-owned ports

### Inbound application handler

```go
type InboundStore interface {
    ClaimAndResolveSender(
        context.Context,
        IncomingCandidate,
    ) (ClaimedMessage, error)
}
```

`ClaimAndResolveSender` atomically:

- resolves/creates internal chat and participant mapping;
- resolves/creates sender ref;
- creates internal message ID;
- claims provider dedup key;
- stores normalized text needed for safe restart replay.

Duplicate claim returns a typed duplicate result, not a second `IncomingMessage`. The external inbound handler then applies allowlist/trigger policy, resolves the Agent key, and calls `AgentRegistry.AgentFor(...).Invoke(...)`.

### Agent

```go
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
```

`Agent` is bound to one tenant/account/chat. `New(ctx, key, deps)` performs bounded initialization without retaining caller context. Agent claims a durable invocation, refreshes and captures one Config version, invokes the model only when no response plan exists, atomically commits the response/action plan, and asks `ResponseDispatcher` to dispatch that stored action. From the caller's perspective `Agent.Invoke()` causes and observes delivery; it never imports or calls Hypermeow directly.

`Agent.Config()` mutation methods do not accept actor. External application/policy handlers call `Refresh()`, authorize that immutable snapshot, then pass its expected version to the mutation. Config persists through compare-and-swap, publishes a defensive snapshot atomically, then attempts a non-sensitive `ConfigChanged` notification after commit. Event loss does not fail the committed mutation. A conflict requires reload and reauthorization, never a blind retry.

### Action executor

```go
type PendingActionStore interface {
    ClaimNext(context.Context, identity.TenantID) (ClaimedAction, error)
    Complete(context.Context, CompleteAction) error
    Fail(context.Context, FailAction) error
    MarkUnknown(context.Context, UnknownAction) error
}

type TextSender interface {
    SendText(context.Context, SendTextRequest) (SendTextResult, error)
}
```

The Hypermeow adapter implements `TextSender`; it does not expose a large application-wide `Gateway` interface.

### Account lifecycle

```go
type Connector interface {
    Pair(context.Context, PairRequest) (PairResult, error)
    Connect(context.Context) error
    Disconnect(context.Context) error
}

type MessageSource interface {
    Run(context.Context, MessageSink) error
}
```

Pairing/lifecycle consumers do not depend on send, reaction, media, group, or future interactive methods.

## Part 1 inbound pipeline

```text
native event
  -> Hypermeow normalization
  -> validate tenant and bounds
  -> ClaimAndResolveSender transaction
  -> ignore duplicate/self/status/basic non-trigger
  -> AgentRegistry.AgentFor(chat)
  -> Config.Refresh
  -> external allowlist/trigger/actor policy
  -> bind policy decision to refreshed Config version
  -> parse exact /prompt command OR Agent.Invoke
  -> authorized /prompt handler changes only PromptOverride with expected Config version
  -> Agent.Invoke acquires its per-chat invocation gate
  -> verify policy Config version, then durable InvocationID/digest claim
  -> replay existing plan OR capture the refreshed immutable ConfigSnapshot
  -> build separate trusted metadata/system/user messages
  -> text-only ModelInvoker
  -> validate bounded plain text
  -> atomically CommitPlan(response + outbound action)
  -> ResponseDispatcher dispatches stored ActionID
  -> observe/finalize delivery receipt
  -> release Agent invocation gate
```

The Agent invocation gate includes the LLM call in Part 1 so replies from the same chat cannot reorder. Different Agent keys can run concurrently subject to global/per-tenant model limits. Registry creates Agents lazily, pins in-flight instances, and idle-evicts only in-memory state. Part 2 introduces batching without changing the public `Agent.Invoke()` contract.

## Agent Config and prompt model

`Agent.Config().Snapshot()` contains a versioned immutable view of `Model`, configurable base `Prompt`, optional durable per-chat `PromptOverride`, and immutable `Permission` policy reference/revision. Credentials and non-overridable application safety instructions are not Agent Config. Permission is data interpreted and enforced only by external policy/application handlers; changing policy contents creates a new immutable revision and updates Config through CAS.

Config mutation uses explicit actor-free methods such as `SetPrompt`, `SetPromptOverride`, `SetModel`, and `SetPermission`. Each receives the externally authorized snapshot version, validates, commits with compare-and-swap, atomically swaps the in-memory snapshot, then attempts a safe `ConfigChanged` notification. The store creates version 1 and owns monotonic increments. Go direct field assignment has no `onChange` hook and is not exposed.

Prompt sources:

1. non-overridable application safety/system policy;
2. configurable base Agent `Prompt`;
3. optional durable per-chat `PromptOverride` (`append` in Part 1);
4. trusted current sender/chat metadata;
5. exact current user text.

These remain separate model messages/data structures until the provider adapter. Do not concatenate a custom plaintext transcript that mixes trusted metadata and user content.

Part 1 command grammar is intentionally closed:

```text
/prompt
/prompt view
/prompt set <text>
/prompt clear
```

- command parser runs before LLM;
- external application/policy handler permits only the configured verified owner to view/mutate;
- mutation has size/UTF-8 validation and transaction;
- no aliases, nested commands, model execution, or role escalation;
- denial/output is deterministic application text.

## LLM boundary

Part 1 `ModelRequest` supplies:

- non-overridable safety/system policy;
- configurable base Agent prompt;
- optional per-chat PromptOverride;
- canonical sender ref/display name in trusted metadata;
- exact user text as user content;
- model and output limit selected by config, not user/model.

Part 1 result is plain bounded text. Tool calls, structured actions, media, provider-specific response objects, and model-selected destinations are rejected.

The adapter owns:

- OpenAI-compatible HTTP DTO;
- authentication header;
- deadline and safe retry classification;
- response extraction;
- provider error mapping and secret redaction.

## Durable state machine

### Inbound event

```text
received -> processing -> response_planned -> completed
                   \-> failed_retryable
                   \-> failed_terminal
```

- `received` is committed before ACKing internal intake.
- A lease/time marker prevents two workers processing the same event.
- LLM calls may be retried because they do not themselves create WhatsApp effects.
- Response intent persistence and inbound `response_planned` transition occur atomically.
- `(AgentKey, InvocationID)` is unique and bound to a canonical input digest;
- same ID/digest after `response_planned` loads the stored plan and skips the model;
- same ID with a different digest fails with `conflict`;
- only an expired lease before response planning may repeat model computation.

### Outbound text action

```text
pending -> executing -> succeeded
                     -> failed_retryable
                     -> failed_terminal
                     -> unknown_outcome
```

- action ID, payload digest, and idempotency key are application-created;
- same key plus same digest returns existing receipt;
- same key plus different digest is `conflict`;
- timeout/disconnect after send begins may be `unknown_outcome`;
- unknown outcome is never automatically resent in Part 1;
- later reconciliation must use provider evidence, not guessing.

## Persistence layout

Per tenant:

```text
<data-root>/tenants/<tenant-id>/app.db
<data-root>/tenants/<tenant-id>/whatsapp.db
```

Part 1 `app.db` tables:

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
history_entries
history_resets
```

Rules:

- migration files embedded, immutable, ordered, and checksummed;
- foreign keys, WAL, busy timeout, and explicit transaction boundaries;
- uniqueness always includes tenant scope;
- all queries take tenant identity explicitly;
- no dynamic DDL outside migration package;
- no table for a future capability before that Part;
- application never writes Hypermeow-owned schema;
- coordinated backup pauses intake and checkpoints both DBs; no cross-DB atomicity is assumed.

Part 1 retains normalized inbound/outbound text only as long as required for recovery/diagnosis. Content should be scrubbed by a short configurable retention after terminal state while dedup/receipt tombstones remain longer. Exact retention is finalized before canary.

## Account runtime

```text
stopped -> starting -> pairing -> connecting -> open
open -> reconnecting -> open
open -> draining -> stopped
any non-terminal state -> failed
```

`AccountRuntime` owns only:

- immutable tenant/account IDs;
- connector/source lifecycle;
- context cancellation and `WaitGroup`;
- state transition lock;
- bounded reconnect policy;
- account readiness.

It does not expose a bag of repositories/services. `AgentRegistry` receives only the narrow account/model/store/dispatcher dependencies needed to construct per-chat Agents.

## Application lifecycle

Startup:

1. load and validate immutable config;
2. initialize redacted logging/metrics;
3. validate/create tenant root;
4. open `app.db`, verify/run migrations;
5. open Hypermeow store;
6. construct adapters and application services;
7. start health endpoint;
8. start action recovery worker;
9. connect/pair account and start message source;
10. mark account ready only after native connection is open.

Shutdown:

1. mark process/account draining;
2. stop new inbound and HTTP mutation intake;
3. cancel queued/in-flight LLM work;
4. finish or release safe claims within deadline;
5. stop action worker; do not replay ambiguous effect;
6. disconnect WhatsApp;
7. checkpoint and close databases;
8. wait for all owned goroutines.

## Concurrency and resource bounds

Part 1 defaults are configuration with validated maximums:

- bounded global inbound queue;
- one lazy Agent per active chat key with an invocation gate;
- hard active-Agent bound, pinning during work, and idle eviction after work;
- bounded global LLM semaphore;
- per-chat/JID outbound serialization;
- text, prompt, and model response hard limits;
- DB, LLM, connect, pair, and send deadlines;
- bounded retry with exponential backoff and jitter;
- no background goroutine without lifecycle owner.

Metrics:

- queue depth and oldest age;
- active/evicted/pinned Agents and invocation wait duration;
- LLM active/latency/error/timeout;
- inbound duplicate count;
- action pending/succeeded/failed/unknown;
- account reconnect/state duration;
- DB busy/WAL size;
- goroutine/heap growth.

## Production canary boundary

Part 1 is deployed only as isolated canary:

- dedicated WhatsApp account;
- dedicated data directory, port, process/service, and log files;
- mandatory fail-closed recipient/chat allowlist;
- normal runtime starts the Agent only behind a mandatory narrow allowlist;
- old service/account/data are never reused or modified;
- health and account readiness are externally visible;
- one-step disable/stop kill switch;
- initial backup after pairing;
- all claims label the result `canary`.

No architecture decision allows bypassing these controls to meet the same-day target.

### Part 1 config contract

Validated startup config includes:

- data root and versioned, automatically generated durable `TenantID`/account identity;
- HTTP bind address;
- configured owner address, normalized only inside trusted identity/adapter code;
- required chat/recipient allowlist;
- WhatsApp runtime dan response policy aktif secara default; optional disable overrides tetap menjadi operational kill switch;
- terminal/disabled pairing output override, dengan terminal sebagai default hanya ketika fresh session memerlukan QR;
- LLM endpoint/model/key and request timeout;
- inbound queue, LLM concurrency, text/prompt/response limits;
- reconnect/send/shutdown timeouts;
- log level/format with content logging default `false`.

Secrets and raw provider addresses are never included in `Redacted()` output. Empty allowlist while canary mode is active is a startup error, not “allow all”.

### Health semantics

- `/health/live` reports that the process event loop is alive.
- `/health/ready` reports component state; it is not ready until migrations are complete and required workers are running.
- Pada normal runtime, readiness juga mensyaratkan account state `open`; explicit WhatsApp-disabled diagnostic mode hanya melaporkan readiness process.
- The response may expose stable state/error codes and timestamps, never secrets, raw addresses, prompts, or message content.
- A reconnecting account makes readiness fail without making liveness fail.

## Scale-out seams

### Multiple accounts in one process

- `AccountRuntime` per tenant;
- per-tenant store factory, queue/semaphore budgets, logger fields, and data root;
- registry maps immutable ID to runtime, never a current-tenant global;
- noisy-neighbor tests precede enabling multiple accounts.

### Multiple processes

- a durable account ownership lease selects exactly one process for each native session;
- process-local Agent ordering is valid while that lease routes an account's turns to one Agent process;
- partitioning one account across Agent workers later requires a durable chat lease or ordered partition before enablement;
- tenant remains the partition key on work and state;
- action/inbound contracts remain stable;
- application store can move from SQLite to PostgreSQL behind consumer ports;
- wake-ups can move from local notification to a broker without making broker delivery the source of truth.

### Service extraction

Extract only when measurements/security require it. Likely boundaries are:

- account/WhatsApp worker because native session is stateful;
- action worker because delivery scales independently;
- control API because its exposure/security differs;
- sub-agent coordinator because long-running job load differs.

Domain IDs, action/inbound state machines, and authorization rules remain shared contracts. Do not extract by copying database access into multiple services.

## Architecture tests

- compile-time adapter interface assertions;
- import/dependency check preventing core imports of adapters;
- two-tenant store/path/sender-ref/prompt isolation tests;
- fake adapter end-to-end tests;
- duplicate, lease, retry, unknown-outcome, and restart tests;
- same-chat ordering and cross-chat concurrency tests;
- race detector;
- secret/raw JID/content log redaction tests;
- real-device tests only through dedicated allowlisted account.

## Explicitly rejected patterns

- giant `Gateway` or `Services` interface used everywhere;
- package globals/`ContextVar`-like implicit tenant routing;
- generic repository abstraction hiding transaction boundaries;
- direct SQL from handlers/domain;
- raw provider object passed through application layers;
- several independent listeners for the same inbound event;
- model-generated generic command execution;
- fake owner/admin flags;
- in-memory-only correctness queue;
- unbounded goroutine per message/chat;
- early microservice/message-broker complexity;
- speculative schema and empty package scaffolding.
- public mutable Agent Config/History fields;
- Agent core methods that accept actor and decide authorization;
- arbitrary `SetHistory([]Message)` replacement;
- config persistence driven only by asynchronous `onChange` listeners.
- replay that invokes the model after a response/action plan already exists;
- process-local per-chat locking presented as cross-process ordering without an account/chat ownership lease.
