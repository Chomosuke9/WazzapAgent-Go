# Kontrak Agent-Centric

## Status dan tujuan

Dokumen ini adalah kontrak normatif untuk object `Agent`. Kontrak ini menggantikan rancangan service-centric yang menempatkan `agent.Service` global sebagai pusat percakapan.

Satu `Agent` mewakili core runtime untuk tepat satu kombinasi:

```text
(TenantID, AccountID, ChatID)
```

Tujuannya:

- API pemakai sederhana dan berorientasi object;
- invocation chat yang sama konsisten dan serial;
- config/history dimiliki sebagai child capability object;
- state durable tetap menjadi source of truth;
- authorization actor tetap berada di luar Agent;
- object dapat di-recreate dan di-evict tanpa kehilangan state.

## Object graph

```text
AccountRuntime
  |
  +-- shared native connection / message source
  |
AgentRegistry
  +-- Agent(Tenant A, Account A, Chat 1)
  |     +-- Config
  |     +-- History             # durable sejak Part 2
  |     +-- ModelInvoker
  |     +-- ResponseDispatcher
  |
  +-- Agent(Tenant A, Account A, Chat 2)
        +-- Config
        +-- History             # durable sejak Part 2
        +-- ModelInvoker
        +-- ResponseDispatcher
```

`AccountRuntime` dimiliki account layer dan dibagi oleh banyak Agent. Agent tidak memiliki atau dapat mengganti native account connection. Agent hanya menerima narrow capability yang diperlukan.

## Boundary authorization

Agent tidak menentukan apakah actor boleh:

- mengubah model/prompt/prompt override/permission;
- membaca atau mereset history;
- melakukan privileged command;
- meminta destructive side effect.

Boundary wajib:

```text
WhatsApp / HTTP / scheduler input
  -> trusted identity resolution
  -> application handler
  -> AgentRegistry.AgentFor(...)
  -> external trigger and authorization policy
  -> Agent core method
```

Policy boleh membaca policy data melalui dedicated read port atau immutable `Agent.Config().Snapshot()`. Mengambil Agent/snapshot bukan bukti authorization; application handler tetap tidak boleh memanggil mutation, history operation, atau privileged invocation sebelum policy lulus.

Agent tetap bertanggung jawab atas domain validation dan invariants. Contoh:

- authorization luar menentukan siapa yang boleh mengganti model;
- `Agent.Config().SetModel()` menentukan apakah model reference valid dan update konsisten;
- authorization luar menentukan siapa yang boleh reset history;
- `Agent.History().Reset()` menjaga transaction, tombstone, dan invocation ordering.

Permission configuration boleh tersimpan dalam `Agent.Config`, tetapi interpretasi dan enforcement-nya dilakukan oleh policy/application/action layer. Model tidak pernah dianggap sebagai actor yang berwenang hanya karena data config masuk prompt.

## Agent identity

```go
package agent

type Key struct {
    TenantID  identity.TenantID
    AccountID identity.AccountID
    ChatID    identity.ChatID
}
```

Rules:

- seluruh field wajib non-empty dan tervalidasi;
- key immutable selama lifetime Agent;
- key tidak menerima raw JID, phone number, path, atau provider message ID;
- target response selalu berasal dari `Key.ChatID`, tidak dipilih model;
- equality memakai seluruh tiga semantic IDs.

## Public object contract

Target contract:

```go
type Agent struct {
    // fields unexported
}

func (a *Agent) Key() Key
func (a *Agent) Config() *Config
func (a *Agent) History() *History
func (a *Agent) Invoke(
    context.Context,
    Invocation,
) (InvokeResult, error)
```

Part 1 contract subset:

```go
func (a *Agent) Key() Key
func (a *Agent) Config() *Config
func (a *Agent) Invoke(context.Context, Invocation) (InvokeResult, error)
```

`History()` diimplementasikan pada Part 2 sebagai child object durable. Blok "Part 1 contract subset" di atas dipertahankan sebagai catatan kontrak historis, bukan status implementasi saat ini.

Agent tidak mengekspos mutable fields berikut:

```go
// Dilarang:
agent.Config.Model = model
agent.History.Messages = messages
agent.Account.Socket = socket
```

Child object diperoleh melalui method sehingga pointer child tidak dapat diganti oleh caller:

```go
snapshot, err := agent.Config().Refresh(ctx)
if err == nil {
    _, err = agent.Config().SetPromptOverride(
        ctx,
        snapshot.Version,
        PromptOverride{Mode: PromptAppend, Text: prompt},
    )
}
```

## Agent construction

```go
type Dependencies struct {
    Defaults      ConfigValues
    ConfigStore   ConfigStore
    HistoryStore  HistoryStore
    Turns         TurnStore
    Context       ContextBuilder
    HistoryWindow uint32
    Model         ModelInvoker
    Responses     ResponseDispatcher
    Events        ConfigEventSink
    Clock         Clock
}

func New(ctx context.Context, key Key, deps Dependencies) (*Agent, error)
```

Constructor wajib:

- memvalidasi `Key`;
- menolak required nil dependencies;
- memvalidasi dan defensive-copy credential-free `Defaults`;
- memuat atau membuat immutable Config version 1 melalui `ConfigStore.LoadOrCreate` dan caller context;
- tidak memulai goroutine tersembunyi;
- tidak melakukan connect/pair account;
- tidak membuat tenant/chat dari ambient global state.

`New` tidak menerima actor atau permission claim. Constructor tidak menyimpan `ctx`; context tersebut hanya membatasi initialization I/O.

Semua dependency Part 2 wajib tersedia. `HistoryWindow` harus berada dalam batas page history. Deployment tanpa listener tetap menginjeksi explicit no-op `ConfigEventSink`, bukan memakai perilaku nil.

## Invocation contract

### Invocation

```go
type Invocation struct {
    ID            identity.InvocationID
    Causation     CausationRef
    Cause         InvocationCause
    Sender        *SenderContext
    Quote         *QuoteContext
    Input         []ContentPart
    Capabilities  CapabilitySet
    PolicyVersion ConfigVersion
    RequestedAt   time.Time
}

type InvocationCause uint8

const (
    CauseInboundMessage InvocationCause = iota + 1
    CauseScheduledTask
    CauseDirectRequest
    CauseSubagentResult
)

type CausationKind uint8

const (
    CausationMessage CausationKind = iota + 1
    CausationTask
    CausationRequest
    CausationSubagent
)

type CausationRef struct {
    Kind CausationKind
    ID   identity.CausationID
}

type SenderContext struct {
    ParticipantID identity.ParticipantID
    Ref           identity.SenderRef
    DisplayName   string
}

type QuoteContext struct {
    Sequence  uint64 // durable transcript ordering metadata; optional on input
    MessageID identity.MessageID
    Role      HistoryRole
    SenderRef identity.SenderRef
    Text      string
}

type Capability string

type CapabilitySet struct {
    // canonical sorted values are unexported
}

func NewCapabilitySet(...Capability) (CapabilitySet, error)
func (s CapabilitySet) Has(Capability) bool
func (s CapabilitySet) Values() []Capability
```

`SenderContext` adalah trusted conversation metadata yang dibangun application boundary. Ia bukan permission proof di dalam Agent.

Inbound-message invocation requires non-nil `Sender`; scheduled and system-owned invocation requires nil `Sender`; authenticated direct invocation may include a trusted sender only when the application boundary can bind it to a real participant. Empty/mismatched sender data is `invalid_argument`.

`CausationRef` memakai internal opaque ID, bukan provider message ID. `Cause` dan `Causation.Kind` harus merupakan pasangan yang valid. Part 2 hanya memakai inbound message; bentuk lain sudah dicadangkan agar scheduler, direct invoke, dan sub-agent tidak memerlukan breaking change pada `Invocation`.

`CapabilitySet` adalah immutable value object dengan private storage dan defensive-copy constructor/accessor. Ia dihitung policy layer di luar Agent. Agent hanya memakai set tersebut untuk membatasi capability yang ditawarkan ke model. Action executor tetap melakukan authorization recheck sebelum side effect. Part 2 masih selalu memakai empty set.

`PolicyVersion` is the Agent Config version used by the external policy decision. For a new generation, Agent refreshes Config and requires an exact match before claiming the turn. This is a stale-decision consistency check, not actor authorization. A mismatch returns `conflict` so the application can refresh and re-run policy. Stored-plan replay keeps its original Config version and relies on the executor's current policy recheck before delivery.

Part 2 hanya menerima text content dan meneruskan model context melalui role serta provenance yang bertipe:

```go
type ContentPart interface {
    isContentPart()
}

type TextPart struct {
    Text string
}

func (TextPart) isContentPart() {}

type ModelRole uint8

const (
    ModelSystem ModelRole = iota + 1
    ModelUser
    ModelAssistant
)

type ModelProvenance uint8

const (
    ProvenanceBasePrompt ModelProvenance = iota + 1
    ProvenancePromptOverride
    ProvenanceHistoryUser
    ProvenanceHistoryAssistant
    ProvenanceHistorySystem
    ProvenanceCurrentUser
)

type ModelMessage struct {
    Role       ModelRole
    Provenance ModelProvenance
    Content    string
}

type ContextBuildRequest struct {
    Config              ConfigSnapshot
    History             []HistoryEntry
    CurrentInvocationID identity.InvocationID
}

type ContextBuilder interface {
    Build(ContextBuildRequest) ([]ModelMessage, error)
}

type ModelRequest struct {
    Key           Key
    InvocationID  identity.InvocationID
    ConfigVersion ConfigVersion
    Model         ModelConfig
    Messages      []ModelMessage
    Capabilities  CapabilitySet
}

type ModelResult struct {
    Text string
}

type ModelInvoker interface {
    Generate(context.Context, ModelRequest) (ModelResult, error)
}
```

Part berikutnya dapat menambah `ImagePart`, `FilePart`, atau `SubagentResultPart` tanpa mengganti signature `Invoke`. System instructions are not `ContentPart`; they come only from trusted Config/application policy paths.

Input tidak boleh menggunakan `any`, raw provider DTO, local path, atau model-selected target.

`ContextBuilder` mengubah Config dan bounded view dari canonical durable transcript menjadi `[]ModelMessage`. Base prompt dan prompt override tetap menjadi system messages dengan provenance terpisah. History dirender menggunakan format transcript compact kompatibel WazzapAgent lama, bukan envelope JSON:

```text
【#000040】 12:56
REPLYING TO 【#000038】
Alice 【u_01234567】: lanjutkan
```

Sequence ditampilkan sebagai enam digit (`000000`–`999999` dengan wrap tampilan), waktu sebagai `HH:MM` UTC pada Part 2 saat ini, dan assistant memakai identitas `You 【You】`. Metadata lengkap (message ID, timestamp, quote, delivery, dan sequence durable) tetap berada di History/SQLite dan tidak hilang dari penyimpanan. Raw sender name dan message text tetap untrusted karena berada pada model message history yang terpisah dari policy system; jangan menggabungkan history dengan safety prompt menjadi satu system message. Hanya assistant history dengan delivery `succeeded` yang masuk context, dan current user message wajib berada paling akhir. View di-anchor pada invocation yang sedang diproses agar passive message yang tiba sesudah trigger tidak menyusup ke context turn tersebut.

`ModelInvoker` dibangun dengan non-overridable application safety/system policy dan provider credentials. Adapter selalu menaruh policy tersebut sebelum seluruh `ModelMessage`, lalu memvalidasi pasangan role/provenance. Config, history, atau output model tidak dapat mengganti policy itu. `ModelResult` hanya berisi candidate content—tidak pernah target, actor, action ID, atau authorization data.

### Invocation identity and durable replay

`Invocation.ID` adalah idempotency identity untuk satu logical turn. Agent menghitung canonical SHA-256 `InvocationDigest` dari Agent key, cause/causation, trusted sender identity, optional canonical quote, ordered content parts, dan sorted capability IDs. Encoding is versioned and length-delimited; it never relies on map iteration or ambiguous string concatenation. Valid UTF-8 user text is hashed as exact bytes, without lossy normalization. Invocation tanpa quote mempertahankan encoding v1 agar turn Part 1 tetap replayable setelah upgrade; invocation ber-quote memakai encoding v2. `PolicyVersion`, `RequestedAt`, deadline, trace ID, serta retry-attempt metadata tidak masuk digest.

```go
type InvocationDigest [32]byte
type TurnLease string

type TurnState uint8

const (
    TurnGenerating TurnState = iota + 1
    TurnFailedRetryable
    TurnResponsePlanned
    TurnDeliveryPending
    TurnSucceeded
    TurnFailedTerminal
    TurnUnknownOutcome
)

type DeliveryStatus uint8

const (
    DeliveryNotStarted DeliveryStatus = iota
    DeliveryPending
    DeliverySucceeded
    DeliveryFailedTerminal
    DeliveryUnknownOutcome
)

type ClaimTurnRequest struct {
    Key        Key
    Invocation Invocation
    Digest     InvocationDigest
    Now        time.Time
}

type TurnClaim struct {
    State     TurnState
    Lease     TurnLease
    MessageID identity.MessageID
    Plan      *StoredPlan
}

type CommitPlanRequest struct {
    Key           Key
    InvocationID  identity.InvocationID
    Lease         TurnLease
    ConfigVersion ConfigVersion
    ResponseText  string
}

type FailGenerationRequest struct {
    Key          Key
    InvocationID identity.InvocationID
    Lease        TurnLease
    Code         ErrorCode
    Retryable    bool
    RetryAfter   time.Time
}

type DispatchRef struct {
    Key      Key
    ActionID identity.ActionID
}

type StoredPlan struct {
    InvocationID  identity.InvocationID
    ConfigVersion ConfigVersion
    ResponseID    identity.MessageID
    ActionID      identity.ActionID
    Text          string
    CreatedAt     time.Time
    Dispatch      DispatchRef
}

type TurnRecord struct {
    Key          Key
    InvocationID identity.InvocationID
    MessageID    identity.MessageID
    Digest       InvocationDigest
    State        TurnState
    Plan         *StoredPlan
    Delivery     DeliveryStatus
    UpdatedAt    time.Time
}

type TurnStore interface {
    Claim(context.Context, ClaimTurnRequest) (TurnClaim, error)
    CommitPlan(context.Context, CommitPlanRequest) (StoredPlan, error)
    FailGeneration(context.Context, FailGenerationRequest) error
    Load(context.Context, Key, identity.InvocationID) (TurnRecord, error)
}
```

`TurnRecord` is a read-only durable projection of the stored invocation, plan, action receipt, and terminal result. It returns defensive copies and never exposes provider credentials/raw address.

Required behavior:

- first valid `(Key, InvocationID, Digest)` claim returns a bounded generation lease;
- same ID plus different digest returns `conflict`;
- same ID plus same digest while an unexpired generator owns it returns `in_progress`;
- an expired generation lease may repeat model computation because no response effect exists yet;
- a handled pre-plan model failure calls `FailGeneration`; retryable failures release the lease with bounded attempt/backoff state, while terminal failures remain terminal;
- a process crash before `FailGeneration` is recovered only through lease expiry;
- `CommitPlan` validates the lease and atomically stores response text, application-created response/action IDs, outbound intent, and `TurnResponsePlanned` state;
- Part 2 extends that same transaction with pending assistant history; it does not add a second planning path;
- after a plan exists, every replay loads that plan and must skip the model even if Config has changed;
- replay of a planned/pending action asks `ResponseDispatcher` to observe or safely resume the same `ActionID`;
- `TurnUnknownOutcome` is returned as-is and is never converted into a new send automatically;
- terminal replay returns the stored result;
- concrete storage may use `inbound_events` for Part 1 and add a general invocation table in Part 6, but behavior above cannot change.

### InvokeResult

```go
type InvokeResult struct {
    InvocationID  identity.InvocationID
    ConfigVersion ConfigVersion
    ResponseID    identity.MessageID
    ActionID      identity.ActionID
    Text          string
    Delivery      DeliveryStatus
}
```

`Invoke()` adalah façade synchronous dari sudut pandang caller, tetapi tidak mengirim langsung melalui Hypermeow. Internal flow:

```text
acquire Agent invocation gate
  -> validate + digest
  -> load existing durable turn and replay immutable plan when present
  -> Config.Refresh + verify external PolicyVersion for genuinely new work
  -> durable TurnStore.Claim
  -> return/resume stored result when already planned
  -> canonical inbound transcript was persisted atomically at intake
  -> append/reconcile the current user history idempotently
  -> load bounded History view through the current invocation
  -> build typed model request
  -> invoke model
  -> validate response
  -> TurnStore.CommitPlan                   # response + action + pending assistant history atomically
  -> ResponseDispatcher.Dispatch stored DispatchRef
  -> wait/observe receipt within context
  -> action transaction finalizes receipt, turn, and assistant history delivery
  -> release gate
```

Part 1 historically used inbound/action tables without conversation history. Part 2 preserves the dispatch contract and adds a full canonical transcript within the existing intake/claim/plan transactions. Trigger policy still decides whether a stored inbound entry gets an invocation: passive allowlisted group traffic is retained, but does not call the model or send a response.

Semantics:

- `error == nil` means delivery reached confirmed `succeeded`;
- if an intent was committed but delivery is still pending, return result IDs plus typed `delivery_pending` error;
- if send may have happened but confirmation is unavailable, return `unknown_outcome`;
- caller must use the same `InvocationID` for lookup/reconciliation and must not create a replacement invocation blindly;
- same ID and same digest never creates a second response plan or action;
- same ID with a different digest fails closed with `conflict`;
- after `CommitPlan`, recovery never invokes the model again for that invocation;
- Agent never claims success solely because model generation succeeded.

## Config child object

### Snapshot

```go
type ConfigVersion uint64

const InitialConfigVersion ConfigVersion = 1

type ModelConfig struct {
    ProviderID      identity.ProviderID
    Model           string
    MaxOutputTokens uint32
}

type PromptOverrideMode uint8

const (
    PromptAppend PromptOverrideMode = iota + 1
    PromptReplace
)

type PromptOverride struct {
    Mode PromptOverrideMode
    Text string
}

type PermissionConfig struct {
    PolicyID identity.PolicyID
    Revision uint64
}

type ConfigValues struct {
    Model          ModelConfig
    Prompt         string
    PromptOverride *PromptOverride
    Permission     PermissionConfig
}

type ConfigSnapshot struct {
    Version        ConfigVersion
    Model          ModelConfig
    Prompt         string
    PromptOverride *PromptOverride
    Permission     PermissionConfig
}
```

Semantics:

- `Model` hanya berisi provider/model reference dan bounded generation settings; endpoint credential/API key tetap dependency adapter dan tidak pernah masuk Agent Config;
- `Prompt` adalah configurable base agent prompt yang di-copy dari validated defaults ketika Config pertama dibuat;
- `PromptOverride` adalah optional durable per-chat instruction;
- `PromptAppend` mengirim base dan override sebagai ordered separate instruction messages;
- `PromptReplace` mengganti configurable `Prompt`, tetapi tidak pernah mengganti non-overridable application safety/system policy;
- `/prompt set <text>` Part 1 menulis `PromptOverride{Mode: PromptAppend, Text: text}` dan `/prompt clear` hanya menghapus override;
- `SetPrompt` mengubah configurable base prompt untuk administrative use case; command `/prompt` tidak memanggilnya;
- `Permission` hanya stable policy reference/revision yang diinterpretasikan policy layer, bukan authorization result atau rules engine di dalam Agent;
- policy contents are immutable for a `(PolicyID, Revision)` pair; changing rules creates a new revision and updates Config through CAS;
- snapshot immutable setelah dikembalikan: pointer/slice/map yang kelak ditambahkan harus selalu defensive-copy dan tidak boleh membagikan backing storage;
- satu invocation memakai tepat satu snapshot version dari awal sampai selesai.

### Read and mutation API

```go
func (c *Config) Snapshot() ConfigSnapshot
func (c *Config) Refresh(context.Context) (ConfigSnapshot, error)

func (c *Config) SetModel(
    context.Context,
    ConfigVersion,
    ModelConfig,
) (ConfigSnapshot, error)

func (c *Config) SetPrompt(
    context.Context,
    ConfigVersion,
    string,
) (ConfigSnapshot, error)

func (c *Config) SetPromptOverride(
    context.Context,
    ConfigVersion,
    PromptOverride,
) (ConfigSnapshot, error)

func (c *Config) ClearPromptOverride(
    context.Context,
    ConfigVersion,
) (ConfigSnapshot, error)

func (c *Config) SetPermission(
    context.Context,
    ConfigVersion,
    PermissionConfig,
) (ConfigSnapshot, error)
```

`Snapshot()` returns the latest locally published defensive copy and performs no I/O. `Refresh()` loads durable state and atomically publishes only a newer version; it never regresses the cache. `Agent.Invoke()` and every application handler that makes an authorization decision must call `Refresh()` before capturing a snapshot.

Tidak ada method di atas yang menerima actor. Authorization telah dilakukan application handler sebelum method dipanggil. Expected `ConfigVersion` mengikat mutation ke snapshot yang dipakai untuk mengambil keputusan; ini adalah consistency check, bukan permission check.

### Mutation ordering

Setiap mutation wajib:

```text
compare caller's expected version with current snapshot
  -> construct next immutable snapshot
  -> validate domain values
  -> CompareAndSwap durable store using Version
  -> atomically publish new in-memory snapshot
  -> best-effort TryPublish ConfigChanged after commit
```

Direct field assignment tidak memiliki `onChange` di Go dan dilarang oleh private fields.

Config store port:

```go
type ConfigStore interface {
    LoadOrCreate(
        context.Context,
        Key,
        ConfigValues,
    ) (ConfigSnapshot, error)
    Load(context.Context, Key) (ConfigSnapshot, error)
    CompareAndSwap(
        context.Context,
        Key,
        ConfigVersion,
        ConfigValues,
    ) (ConfigSnapshot, error)
}
```

Store owns version assignment. `LoadOrCreate` atomically creates a missing record at `InitialConfigVersion`; `CompareAndSwap` requires an exact expected version and returns a snapshot incremented exactly once. Caller cannot choose the next version. Version zero, regression, and wraparound fail closed as `integrity_failure`.

Concurrent stale update menghasilkan typed `conflict`; application handler wajib `Refresh`, menjalankan authorization/decision lagi, lalu mengirim mutation baru. Jangan melakukan blind retry karena policy data mungkin ikut berubah.

### ConfigChanged event

```go
type ConfigField uint8

const (
    ConfigFieldModel ConfigField = iota + 1
    ConfigFieldPrompt
    ConfigFieldPromptOverride
    ConfigFieldPermission
)

type ConfigChanged struct {
    Key           Key
    Previous      ConfigVersion
    Current       ConfigVersion
    ChangedFields []ConfigField
    OccurredAt    time.Time
}

type ConfigEventSink interface {
    TryPublish(ConfigChanged) bool
}

type Clock interface {
    Now() time.Time
}
```

Rules:

- publication is attempted only after durable commit and atomic snapshot swap;
- `TryPublish` must be non-blocking and is never called while Config mutex is held;
- does not contain API keys, raw prompt, raw addresses, or message content;
- used for cache invalidation, metrics, UI refresh, and cross-runtime wake-up;
- never used as the primary persistence mechanism;
- a false/drop result records a metric but the setter still returns the committed snapshot with `nil` error;
- event order is not guaranteed; consumers retain the highest observed `Current` version and call `Refresh()`;
- correctness survives duplicate, reordered, or lost events because authorization paths and `Invoke()` refresh durable state.

This explicit event is the Go equivalent of an `onChange` hook. Direct field assignment itself cannot trigger a hook.

## History child object

History diimplementasikan mulai Part 2:

```go
type History struct {
    // key, store, and shared invocation gate are unexported
}

type HistoryRole uint8

const (
    HistoryUser HistoryRole = iota + 1
    HistoryAssistant
    HistorySystem
)

type HistoryEntry struct {
    Sequence     uint64 // assigned by the durable store; callers must leave it zero
    MessageID    identity.MessageID
    InvocationID identity.InvocationID
    Causation    CausationRef
    Role         HistoryRole
    Sender       *SenderContext
    Quote        *QuoteContext
    Content      []ContentPart
    Delivery     DeliveryStatus
    CreatedAt    time.Time
}

type HistoryCursor string

type HistoryQuery struct {
    Before              HistoryCursor
    ThroughInvocationID identity.InvocationID
    Limit               uint32
}

type HistoryPage struct {
    Entries []HistoryEntry
    Next    *HistoryCursor
}

type RetentionPolicy struct {
    KeepLatest uint32
    MaxAge     time.Duration
}

type TrimResult struct {
    Removed uint64
}

type HistoryStore interface {
    ListIfConfigVersion(
        context.Context,
        Key,
        ConfigVersion,
        HistoryQuery,
    ) (HistoryPage, error)
    Append(context.Context, Key, HistoryEntry) error
    ResetIfConfigVersion(
        context.Context,
        Key,
        ConfigVersion,
        time.Time,
    ) error
    Trim(
        context.Context,
        Key,
        RetentionPolicy,
    ) (TrimResult, error)
}

func (h *History) List(
    context.Context,
    ConfigVersion,
    HistoryQuery,
) (HistoryPage, error)

func (h *History) Append(
    context.Context,
    HistoryEntry,
) error

func (h *History) Reset(context.Context, ConfigVersion) error

func (h *History) Trim(
    context.Context,
    RetentionPolicy,
) (TrimResult, error)
```

There is intentionally no arbitrary `SetHistory([]Message)` method.

Rules:

- `List` returns immutable copies/pages, not the internal mutable slice;
- `List` is the canonical durable transcript for an allowlisted chat from the moment this runtime accepts an event; it is not a retroactive provider-history import;
- inbound text and text-only sticker placeholders are stored for allowlisted DM/group chats before trigger filtering; a passive group entry is context, not an invocation reason;
- generated model replies and command replies are assistant entries whose delivery state is finalized with the same action receipt; manual outgoing messages outside this outbox are not claimed as transcript entries;
- every stored row has a monotonic `Sequence` and `CreatedAt`; quote metadata points to the canonical internal message and may carry its sequence, while provider IDs/JIDs never cross into model context;
- `ThroughInvocationID` bounds a context read at the current user entry and cannot be combined with `Before`; it prevents messages arriving during debounce from changing an in-flight context;
- `List` and `Reset` receive the exact Config version whose Permission reference was externally authorized;
- their store transaction verifies that `agent_configs.version` still matches before reading/resetting history, otherwise returns `conflict`;
- `Append` is normally used only by Agent invocation/recovery internals;
- Part 2 accepts exactly one valid UTF-8 `TextPart` per history entry; later media parts require an explicit schema/contract extension;
- `Append` is idempotent by message/invocation identity and rejects conflicting content digests;
- `List` recomputes the immutable identity/content digest for every row and returns `integrity_failure` on corruption;
- `Reset` coordinates with the Agent invocation gate, writes a reset tombstone, dan membatalkan pre-reset staged/generating inbound work;
- staging batching memeriksa tombstone yang sama agar pesan lama tidak dapat masuk lagi setelah race dengan reset;
- an older received/generating/retryable/planned turn blocks a newer conversation batch; order uses the durable SQLite intake sequence rather than timestamp/UUID coincidence, recovery reuses the original durable batch anchor, and a pre-Part-2 turn without an anchor is recovered as a one-message batch;
- `Trim` is called by retention workflow and pins pending history only. Unknown history is terminal and may age out; the independent turn/action receipt remains the anti-resend tombstone;
- methods do not accept actor or evaluate permission;
- application handlers authorize read/reset/import before calling the core method;
- permission policy revisions are immutable; changing policy rules creates a new revision and updates Agent Config, so a Config-version guard cannot silently authorize against replaced rules;
- optional future `Import` must be a separate explicit audited use case.

## External application handlers

### Invoke path

```go
func (h *InboundHandler) Handle(
    ctx context.Context,
    candidate IncomingCandidate,
) error {
    message, err := h.inbox.ClaimAndResolve(ctx, candidate)
    if err != nil {
        return err
    }

    currentAgent, err := h.agents.AgentFor(ctx, AgentKeyFrom(message))
    if err != nil {
        return err
    }

    snapshot, err := currentAgent.Config().Refresh(ctx)
    if err != nil {
        return err
    }

    decision, err := h.policy.Evaluate(
        ctx,
        message,
        snapshot.Permission,
    )
    if err != nil || !decision.Invoke {
        return err
    }

    decision.Invocation.PolicyVersion = snapshot.Version

    _, err = currentAgent.Invoke(ctx, decision.Invocation)
    return err
}
```

Identity/inbox claim happens before Agent lookup. External trigger/allowlist/actor policy then evaluates the refreshed immutable policy reference before `Agent.Invoke()`.

### Config mutation path

```go
func (h *SetPromptHandler) Handle(
    ctx context.Context,
    actor policy.Principal,
    key agent.Key,
    prompt string,
) error {
    currentAgent, err := h.agents.AgentFor(ctx, key)
    if err != nil {
        return err
    }

    snapshot, err := currentAgent.Config().Refresh(ctx)
    if err != nil {
        return err
    }
    if err := h.authorizer.Require(
        ctx,
        actor,
        key,
        snapshot.Permission,
        policy.ChangePrompt,
    ); err != nil {
        return err
    }

    _, err = currentAgent.Config().SetPromptOverride(
        ctx,
        snapshot.Version,
        agent.PromptOverride{
            Mode: agent.PromptAppend,
            Text: prompt,
        },
    )
    return err
}
```

### History reset path

```go
func (h *ResetHistoryHandler) Handle(
    ctx context.Context,
    actor policy.Principal,
    key agent.Key,
) error {
    currentAgent, err := h.agents.AgentFor(ctx, key)
    if err != nil {
        return err
    }

    snapshot, err := currentAgent.Config().Refresh(ctx)
    if err != nil {
        return err
    }

    if err := h.authorizer.Require(
        ctx,
        actor,
        key,
        snapshot.Permission,
        policy.ResetHistory,
    ); err != nil {
        return err
    }

    return currentAgent.History().Reset(ctx, snapshot.Version)
}
```

No transport handler may bypass these use-case handlers for privileged operations.

## AgentRegistry contract

```go
type Registry struct {
    // fields unexported
}

type Factory interface {
    NewAgent(context.Context, Key) (*Agent, error)
}

type RegistryLimits struct {
    MaxLive             uint32
    IdleTTL             time.Duration
    ConstructionTimeout time.Duration
}

func NewRegistry(
    context.Context,
    Factory,
    RegistryLimits,
) (*Registry, error)

func (r *Registry) AgentFor(
    context.Context,
    Key,
) (*Agent, error)

func (r *Registry) NotifyConfigChanged(ConfigChanged)
func (r *Registry) Close(context.Context) error
```

Rules:

- one live Agent instance per valid `Key` within a process;
- concurrent first access is coalesced; never construct duplicate live instances;
- shared construction uses a Registry-owned context plus `ConstructionTimeout`, not the first waiter's context;
- canceling one waiter stops only that wait; construction continues while another waiter or Registry still needs it;
- failed construction is returned to current waiters but is not cached permanently;
- Agent is created lazily;
- inactive Agent can be evicted after configurable idle time;
- in-flight Agent is pinned and cannot be evicted;
- eviction removes only memory/cache/locks, never durable config/history/action state;
- a later access reconstructs the Agent from durable state;
- registry has a hard active-Agent bound and explicit backpressure/error policy;
- no permanent goroutine is created for every historical chat;
- registry is tenant-aware and never uses a mutable current-tenant global;
- `NotifyConfigChanged` never constructs or evicts an Agent; it records the highest version hint and marks an existing Config stale;
- an in-flight invocation keeps its captured Config, while the next operation calls `Refresh()`;
- duplicate, reordered, or missing notifications are harmless because durable refresh is authoritative;
- `Close` marks Registry draining, rejects new `AgentFor` calls, cancels Registry-owned construction/work, and waits only up to caller deadline.

Part 1 uses one account but still keys registry by tenant/account/chat.

Registry uniqueness and invocation serialization are process-local. Multi-process operation is correct only while the durable account-ownership lease routes one account's inbound turns to one Agent process. If turns for one account are later partitioned across workers, a durable chat-partition lease or ordered queue becomes mandatory before enabling that topology. Control-plane Config mutations may occur elsewhere because each authorization/invocation path performs durable `Refresh()`.

## Concurrency contract

- `Agent.Invoke()` is serial per Agent key.
- Invocations for different Agent keys may run concurrently.
- every new generation path calls `Config.Refresh()` before capturing its immutable version; replay with a stored plan keeps the original version/result;
- Global and per-tenant model semaphores exist outside/under shared model adapter policy.
- Config mutation uses optimistic version/CAS and does not mutate the snapshot already captured by an invocation.
- Authorization-backed Config mutation must use the exact snapshot version that was externally authorized; conflict requires reauthorization.
- History reset/import obtains the same exclusive invocation gate and verifies its externally authorized Config version in the storage transaction.
- Read-only config/history snapshots may run concurrently.
- No external callback/event listener runs while Agent/Config/History locks are held.
- Context cancellation releases locks and registry references.
- Account send ordering remains per chat/JID in the response dispatcher/adapter.
- Race detector tests cover invoke/config/reset/evict interleavings.

## ResponseDispatcher contract

Agent depends on a durable response capability, not Hypermeow directly:

```go
type DeliveryResult struct {
    ActionID    identity.ActionID
    Status      DeliveryStatus
    CompletedAt *time.Time
}

type ResponseDispatcher interface {
    Dispatch(
        context.Context,
        DispatchRef,
    ) (DeliveryResult, error)
}
```

`TurnStore.CommitPlan` has already persisted the application-created response/action IDs, target, causation, idempotency key, and candidate text before this method is called. `DispatchRef` selects that existing action; it cannot contain model-selected target/content.

Dispatcher owns:

- loading and validating the stored action against `DispatchRef.Key`;
- durable action claim/outbox/receipt;
- current external policy recheck immediately before every side effect, including ordinary text delivery;
- provider target resolution;
- native send;
- retry classification and unknown-outcome handling.

Calling `Dispatch` repeatedly for the same `ActionID` observes or safely resumes the same state machine; it never creates a second action. Thus `Agent.Invoke()` causes the send from the caller's perspective, while the safe delivery implementation remains separately owned and testable.

## Error contract

Core methods use `type ErrorCode string` with stable values:

```go
type ErrorCode string

const (
    ErrorInvalidArgument   ErrorCode = "invalid_argument"
    ErrorNotFound          ErrorCode = "not_found"
    ErrorPermissionDenied  ErrorCode = "permission_denied"
    ErrorConflict          ErrorCode = "conflict"
    ErrorInProgress        ErrorCode = "in_progress"
    ErrorNotReady          ErrorCode = "not_ready"
    ErrorUnsupported       ErrorCode = "unsupported"
    ErrorRateLimited       ErrorCode = "rate_limited"
    ErrorResourceExhausted ErrorCode = "resource_exhausted"
    ErrorTimeout           ErrorCode = "timeout"
    ErrorCancelled         ErrorCode = "cancelled"
    ErrorUnavailable       ErrorCode = "unavailable"
    ErrorStorageFailure    ErrorCode = "storage_failure"
    ErrorProviderFailure   ErrorCode = "provider_failure"
    ErrorIntegrityFailure  ErrorCode = "integrity_failure"
    ErrorDeliveryPending   ErrorCode = "delivery_pending"
    ErrorUnknownOutcome    ErrorCode = "unknown_outcome"
    ErrorInternal          ErrorCode = "internal"
)
```

Agent core itself should not normally create `permission_denied`; it propagates it only when an external collaborator such as the response policy/executor returns it.

## Implementation subsets

Part 1 historically implemented:

- lazy `AgentRegistry` keyed by tenant/account/chat;
- `Agent.Key`, `Agent.Config`, and text-only `Agent.Invoke`;
- Config snapshot containing model/base prompt/per-chat prompt override/permission reference, while `/prompt` mutates only the override;
- explicit Config setters with durable CAS and post-commit `ConfigChanged` event;
- external configured-owner authorization for `/prompt`;
- sender ref in `Invocation.Sender`;
- durable `TurnStore` claim/plan/replay semantics keyed by `InvocationID` and digest;
- text-only ModelInvoker;
- durable ResponseDispatcher for one `SendText` effect;
- per-Agent invocation serialization and registry idle eviction;
- fake registry/Agent dependencies for tests.

Part 1 does not implement:

- History child behavior or history table;
- batching/debounce;
- quoted/replied-to-bot context;
- model tools or generic commands;
- media input/output;
- model-selected destination;
- Agent-owned authorization.

Part 2 implements:

- full durable canonical transcript through `Agent.History().List/Append/Reset/Trim` with immutable paging, store-assigned sequence, reset tombstone, and retention;
- allowlisted DM/group inbound text plus text-only sticker placeholders are persisted before trigger filtering; passive group traffic is visible to the next eligible invocation without causing a response;
- atomic user-history intake and assistant-history creation/finalization around the existing durable turn/action lifecycle;
- deterministic bounded context view through the triggering invocation with sequence/timestamp/quote provenance, typed roles, and non-overridable adapter policy;
- canonical internal quote resolution and group reply-to-bot trigger;
- durable per-chat debounce/batching with bounded burst draining and restart recovery;
- `/help`, `/info`, and externally owner-authorized `/reset`;
- migration from the Part 1 schema and invocation-digest compatibility for unquoted stored turns;
- full-data offline backup, manifest verification, and restore into a new directory;
- history/batch metrics and retention maintenance.

Part 2 tetap tidak mengimplementasikan model tools, generic model commands, media, scheduler, sub-agent, model-selected destination, atau Agent-owned authorization.

## Contract tests

Required before Part 1 canary:

- same key returns the same live Agent;
- concurrent first access creates one Agent;
- canceling one Registry waiter does not cancel construction needed by another;
- constructor/load obeys initialization context and timeout;
- different chat/tenant keys never share Config state;
- idle eviction followed by recreation restores durable Config;
- in-flight Agent cannot be evicted;
- `Invoke` calls for one Agent serialize;
- different Agents can invoke concurrently within limits;
- stale externally decided `PolicyVersion` conflicts before a new turn claim/model call;
- same Invocation ID/digest after plan skips the model and reuses response/action IDs;
- same Invocation ID with a different digest returns `conflict`;
- an active generation lease returns `in_progress`, while an expired pre-plan lease may recompute;
- handled model failure releases/terminates its lease correctly and crash recovery waits for expiry;
- crash after `CommitPlan` never produces a second plan or model call;
- config mutation during invoke does not alter the captured version;
- missing Config is created once at version 1 and concurrent create is deterministic;
- stale config CAS returns `conflict`;
- `/prompt set|clear` modifies only `PromptOverride`, never base/safety prompt;
- Config snapshots make defensive copies and never regress during refresh;
- `ConfigChanged` occurs only after durable commit and contains no sensitive values;
- dropped or reordered Config events do not fail a committed setter or produce stale authorization;
- event loss does not lose committed config;
- failed config persistence does not mutate in-memory snapshot or publish change;
- unauthorized prompt request never reaches Agent mutation method;
- response target is always Agent key chat;
- model output cannot set actor, permission, target, action ID, or idempotency key;
- ResponseDispatcher rechecks current external policy before ordinary or privileged sends;
- send failure/pending/unknown outcomes are reported accurately;
- `NotifyConfigChanged` never creates/evicts an Agent and next operation refreshes durable Config;
- registry close cancels owned work without leaking goroutines.

Required Part 2 additions:

- history pagination, defensive copies, idempotent append, digest collision, version guard, reset, and retention;
- full passive-group transcript followed by a mention/reply trigger, sequence ordering, sticker placeholder, and through-invocation stale-context boundary;
- reset-vs-invoke exclusion and reset-vs-debounce race handling;
- deterministic context golden serialization, injection-as-data, context bound, stale-order rejection, and exclusion of undelivered assistant output;
- Part 1 digest compatibility plus quote-aware digest distinction;
- canonical quote lookup and group reply-to-bot behavior;
- debounce coalescing, burst splitting without remainder loss, active-generation/pre-batch restart recovery, and same-chat ordering;
- replayed response plan and assistant history use one normalized durable creation timestamp;
- successful/failed/unknown action delivery reflected atomically in assistant history;
- owner-only reset and command idempotency;
- schema upgrade from Part 1, backup checksum/tamper detection, restore, and history survival;
- full test suite, race detector, vet, module verification, vulnerability scan, and supported target builds before canary.

## Rejected contracts

```go
// Mutable property cannot be intercepted safely in Go.
agent.Config.Model = model

// Arbitrary replacement breaks identity, causation, and receipts.
agent.SetHistory(messages)

// Agent must not decide actor authorization.
agent.Config().SetPrompt(ctx, actor, prompt)
agent.History().Reset(ctx, actor)

// Raw input loses causation/sender/capability metadata.
agent.Invoke(ctx, anyInput)

// Full Account object creates shared ownership confusion.
agent.Account.Socket.Send(...)
```

Use explicit typed invocation, actor-free core mutation methods, external application authorization, and narrow durable collaborators instead.
