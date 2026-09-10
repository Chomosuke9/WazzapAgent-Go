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
   registry; persist the model-tool opt-in behind owner-only `/permission`.
3. **3.2 Typed effect outbox**: durable, idempotent `react`, `delete`,
   `mark-read`, and chat-presence effects. No string command is ever
   dispatched.
4. **3.3 Model tool bridge**: expose only explicitly granted typed tools to an
   OpenAI-compatible provider, validate the returned typed calls, atomically
   plan them with the response, then authorize again immediately before native
   execution.
5. **3.4 Provider resilience**: explicit configured fallback chain and bounded
   retry classification. It never retries an ambiguous external effect.

Until the corresponding slice is complete, an operation remains unavailable;
the registry must deny it rather than emulate it with text or a generic command.

### Implementation status — 2026-09-10

Slices 3.0–3.4 have local implementation and test coverage. The default for a
new chat remains **no model tools**. An owner must explicitly use
`/permission set ...` for the three non-destructive model capabilities. The
real-device canary gate remains pending; that is the only point at which an
opt-in tool should be enabled for a live account.

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
chat.permission.write

message.react
message.delete
message.mark-read
chat.presence
chat.context.read
```

The initial model capability baseline is deliberately empty. The only model
opt-ins are `message.react`, `message.mark-read`, and `chat.presence`; an
owner changes them durably with:

```text
/permission view
/permission set none
/permission set react mark-read presence
```

`message.delete` is a typed outbox capability for a future explicitly
authorized human flow, but is not a model privilege in Part 3. Group
moderation (kick, subject, description, invite, open/close) is out of scope.

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

The Part 2 commands (`/help`, `/info`, `/reset`, `/prompt`) retain their
existing behavior. `/permission` is owner-only and alters only the durable
allow-list above through config CAS plus an inbound mutation journal, so a
crash after the config commit cannot apply it twice. It deliberately does not
copy the old implicit 0–3 model. Unknown slash text remains ordinary chat text
unless a future trigger policy explicitly changes that decision.

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
no arbitrary destination. The current live read port is
`ChatAuthorityReader`: it returns only chat kind and bot/actor admin facts for
policy rechecks. A broader `GetChatContext` model read is intentionally
deferred until its data-minimization contract has a caller.

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
cross-chat targets, malformed/trailing JSON, and tool calls not covered by the
request's capability set. The schemas deliberately expose no message ID,
tenant, account, chat, or destination: each message-targeted call is bound to
the current inbound `MessageID` by the decoder, then verified again by both
`Agent` and `TurnStore` before the transaction commits.

The model cannot call `/group`, `/permission`, `/prompt`, `/reset`, or any
other human command. It cannot select a tenant, account, chat, principal,
capability, action ID, retry policy, or raw WhatsApp address. A text reply may
exist alongside zero or more planned typed effects; both are committed under
the same claimed invocation before any provider call. A unique model call ID
inside that transaction makes replay load the original effect refs rather than
planning another native action. The effect dispatcher runs only after the
same turn's text response has a durable success receipt; recovery therefore
cannot react or mark-read while its companion reply is still pending or
unknown.

## Fallback boundary

The primary OpenAI-compatible client is followed by one optional fallback
candidate configured with `WAZZAP_LLM_FALLBACK_ENDPOINT` and
`WAZZAP_LLM_FALLBACK_API_KEY`; it uses the same configured model name. The
chain advances only after timeout, rate-limit, provider-unavailable, provider-
failure, or internal client failures. Validation, permission, and cancellation
failures stop immediately. Fallback is model-only: it never retries an
ambiguous WhatsApp effect.

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
