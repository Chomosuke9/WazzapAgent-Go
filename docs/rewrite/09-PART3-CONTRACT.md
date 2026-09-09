# Part 3 — Authority, Commands, and Typed Effects

## Status and scope

Part 3 extends the durable conversation core from Part 0–2. It does **not**
turn the model into an administrator and does not restore the legacy generic
`run_command` mechanism. The old implementation is useful evidence for which
WhatsApp operations exist, but its synthetic command identity is explicitly
not a compatible authority model for this rewrite.

Part 3 is delivered in vertical slices so every new side effect has its full
contract, persistence, authorization, provider adapter, recovery semantics,
and tests before the next one is enabled:

1. **3.0 Authority and command foundation**: typed principals, capability
   vocabulary, declarative command registry, and live authority read port.
2. **3.1 Human command authorization**: migrate existing commands onto that
   registry; add `/permission` only after its durable policy model exists.
3. **3.2 Typed effect outbox**: durable, idempotent `react`, `delete`,
   `mark-read`, and chat-presence effects plus a typed `GetChatContext` read
   use case. No string command is ever dispatched.
4. **3.3 Model tool bridge**: expose only explicitly granted typed tools to an
   OpenAI-compatible provider, validate the returned typed calls, atomically
   plan them with the response, then authorize again immediately before native
   execution.
5. **3.4 Provider resilience**: explicit configured fallback chain and bounded
   retry classification. It never retries an ambiguous external effect.

Until the corresponding slice is complete, an operation remains unavailable;
the registry must deny it rather than emulate it with text or a generic command.

## Non-negotiable invariants

- `Agent` validates its own values and turn state but receives no actor and
  makes no authorization decision.
- A raw WhatsApp JID, provider message ID, LID, `senderRef`, prompt, quoted
  content, or model output is never authority by itself.
- A `senderRef` remains a display/history reference. The identity boundary
  resolves it only through the durable, fail-closed `senderRef <-> LID` map.
- Every effect has a concrete Go type. There is no `RunCommand`, shell command,
  slash-command string, arbitrary JSON payload, destination selected by the
  model, or provider DTO in the model/domain boundary.
- A planned effect is bound to the originating `(tenant, account, chat,
  invocation, causation)` and cannot be redirected by model output.
- The executor rereads current policy and live provider authority immediately
  before every external side effect. A stale plan may be denied but is never
  silently broadened.
- Any outcome that could mean the provider already performed an effect becomes
  `unknown_outcome`; recovery records it and never blindly repeats it.
- Read operations and ephemeral hints have explicit semantics. They are not
  hidden writes in the outbound-action queue.

## Principal contract

```go
type PrincipalKind uint8

const (
    PrincipalHuman PrincipalKind = iota + 1
    PrincipalModel
    PrincipalSystem
    PrincipalRecovery
)

type Principal struct {
    Kind          PrincipalKind
    TenantID      identity.TenantID
    AccountID     identity.AccountID
    ChatID        identity.ChatID
    ParticipantID identity.ParticipantID // Human only
    LID           identity.LID           // Human only
    InvocationID  identity.InvocationID  // Model/recovery only
}
```

`PrincipalHuman` is created only after durable inbound identity resolution. It
contains the verified LID and internal participant surrogate, never a claimed
role from message text. `PrincipalModel` is scoped to one already-claimed
invocation and may receive capabilities, but is never owner, group admin, or
an unrestricted representation of the bot account. `PrincipalSystem` and
`PrincipalRecovery` are internal operational principals with a narrow,
documented capability set; they are not a way to bypass executor checks.

The principal type is owned by the application/policy boundary, not by
`agent.Agent`, provider adapters, or model DTOs.

## Capabilities and authority

Capabilities are named immutable values, not integer permission levels:

```text
chat.command.help
chat.command.info
chat.history.reset
chat.prompt.write

message.react
message.delete
message.mark-read
chat.presence
chat.context.read
```

The initial model capability baseline is deliberately empty. A future model
tool bridge may grant `message.react`, `message.mark-read`, and
`chat.presence` per invocation only after policy resolves them. `message.delete`
is destructive and is not granted to a model by default. Group moderation
(kick, subject, description, invite, open/close) is out of this Part 3 scope.

The policy decision carries only the allowed set for this one request. It is
not persisted as a standing role. The executor validates all of the following
immediately before a native operation:

1. the effect is structurally valid and belongs to its stored chat;
2. current chat allowlist and policy revision still permit the capability;
3. the effect's principal and capability remain compatible;
4. live group membership/admin state permits the operation when WhatsApp
   requires it; and
5. each referenced internal message belongs to the same tenant/account/chat.

For human commands, role resolution is current at command time. A configured
owner is an external configuration fact; a group-admin decision comes from a
fresh provider group snapshot, not a cached message claim. The adapter exposes
this as a narrow `ChatAuthorityReader` port so policy does not import Hypermeow.

## Declarative command registry

A command descriptor has one canonical name, aliases, parser, required human
capability, and handler. The registry resolves aliases once and rejects duplicate
tokens at startup. A recognized-but-denied command is handled as a command and
receives a deterministic denial; it must not fall through into AI input.

```go
type CommandDescriptor struct {
    Name        CommandName
    Aliases     []string
    Capability  policy.Capability
    Parse       func(string) (CommandRequest, error)
    Handle      func(context.Context, policy.Principal, CommandRequest) error
}
```

The Part 2 commands (`/help`, `/info`, `/reset`, `/prompt`) migrate first with
their existing behavior. The future `/permission` command configures an
explicit policy object; it does not copy the old implicit 0–3 model until its
scope, ownership, and migration are specified. Unknown slash text remains
ordinary chat text unless a future trigger policy explicitly changes that
decision.

## Typed effect contract

Effects use internal IDs only:

```go
type Effect interface { isEffect() }

type React struct {
    TargetMessageID identity.MessageID
    Emoji           string
}

type DeleteMessage struct {
    TargetMessageID identity.MessageID
}

type MarkRead struct {
    TargetMessageID identity.MessageID
}

type SetChatPresence struct {
    State PresenceState // composing or paused
}
```

`React`, `DeleteMessage`, and `MarkRead` resolve the target through durable
message ownership at execution; provider message IDs are resolved only by the
provider adapter after authorization. `SetChatPresence` is chat-bound and has
no arbitrary destination. `GetChatContext` is a separate live read operation
returning typed, sanitized group metadata (chat kind, bot admin state, and
allowed capability-relevant flags); it is not an action and cannot cause a
side effect.

Each durable effect has its own idempotency identity and receipt. `react` and
`delete` are treated as ambiguous after network/provider uncertainty and become
`unknown_outcome`. `mark-read` and presence are best-effort, time-sensitive
hints: they are never replayed after a crash and do not block a response turn.
That distinction is explicit in the type and storage contract rather than an
executor special case.

## Model boundary

The provider request contains function/tool schemas generated from the
capabilities actually granted for that invocation. The response decoder accepts
only declared tool names and bounded JSON arguments, then turns them into the
typed Go values above. It rejects unknown tools, duplicate call identifiers,
cross-chat targets, malformed JSON, and tool calls not covered by the request's
capability set.

The model cannot call `/group`, `/permission`, `/prompt`, `/reset`, or any
other human command. It cannot select a tenant, account, chat, principal,
capability, action ID, retry policy, or raw WhatsApp address. A text reply may
exist alongside zero or more planned typed effects; both are committed under
the same claimed invocation before any provider call.

## Evidence taken from the old project

The old code validates the need for a declarative command registry, command
alias collision detection, live group-role checks, and separate native methods
for reaction, revocation, read receipt, presence, and chat-context lookup. It
is not the authority design for this rewrite: its model-driven `run_command`
route synthesizes a message identity, which is incompatible with this
contract's principal provenance and capability recheck.

## Part 3 exit criteria

- A model cannot gain owner/admin status by text, tool arguments, replay, or a
  synthetic command context.
- Every enabled effect passes validate -> plan -> claim -> current-policy/live-
  authority recheck -> execute -> receipt/unknown outcome.
- Aliases and permission expressions are deterministic; unknown and denied
  commands cannot accidentally invoke AI or a privileged handler.
- A message target from another chat/tenant is rejected before adapter access.
- Recovery cannot duplicate an ambiguous native effect.
- Full, race, persistence/restart, replay, authorization, and native-adapter
  fake tests pass before an effect is enabled in a real-device canary.
