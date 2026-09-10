# Part 3 — Permission, Commands, and Effects

## Status and scope

Part 3 extends the durable Part 0–2 conversation core with the moderation
mechanism used by the old project, while retaining the rewrite's explicit
principal and durable outbox boundaries.

The model receives exactly these provider tools:

- `reply_message`: visible reply plus optional silent slash commands;
- `react_to_message`: reaction to a message from the supplied history.

`mark-read` and composing presence are automatic AI-lane behavior. They are
not permissions and are never offered to the model. Delete, mute, and kick are
not model tools either: they are `/group delete`, `/group mute`, and
`/group kick` command strings carried by `reply_message`.

The implementation is complete locally. A real-device production canary is
still required.

## Permission levels

`/permission` is owner-only and stores one moderation level per chat:

```text
/permission          show the current level
/permission view     show the current level
/permission 0        reaction only; no moderation command
/permission 1        reaction plus /group delete
/permission 2        level 1 plus /group mute
/permission 3        level 2 plus /group kick
```

Reaction is always available and is not controlled by the level. The level is
checked when building the model request and checked again immediately before a
stored group command reaches WhatsApp.

## `reply_message` command transport

The contract deliberately follows the old project:

```json
{
  "context_msg_id": "000123",
  "text": "Pesan yang terlihat oleh user",
  "command": ["/group delete"],
  "command_context_msg_id": ["000122"]
}
```

`context_msg_id` is a six-digit ID from the compact history, or `none`.
`command` is `null` or a bounded array of slash-command strings.
`command_context_msg_id` is `null` to reuse the reply anchor, or an aligned
array of history IDs. Provider IDs, JIDs, tenant IDs, and database IDs are
never exposed to the model.

Only the three moderation members of the `/group` family are accepted in this
Part. Each returned command becomes a typed, durable `RunGroupCommand` effect;
it is never posted into the WhatsApp chat as text. The effect stores the exact
command and the internally resolved message anchor, if applicable.

## Identity and authority

- `Agent` remains pure conversation core and receives no actor DTO.
- Human command authorization stays in the inbound/policy layer.
- Model commands use an explicit invocation-scoped `PrincipalModel`; they are
  never converted into a fake owner or a synthetic human message.
- `senderRef` is resolved only through the durable fail-closed
  `senderRef <-> LID` mapping. LID remains the canonical participant identity.
- The executor rereads the current permission level, allowlist, policy
  revision, and live group-admin state before native moderation.
- Every message anchor is resolved through the durable history row belonging
  to the same tenant/account/chat.

This preserves the useful old `reply_message -> run command` behavior without
copying its unsafe `fromMe=true`/owner impersonation shortcut.

## Automatic WhatsApp behavior

For an authorized AI batch, the runtime marks all batch messages as read and
sets composing presence before invoking the model. It always attempts to set
presence back to paused when processing finishes. These signals are
best-effort and ephemeral: they do not enter the durable effect outbox and a
failure cannot invalidate an otherwise durable conversation turn.

Mute is implemented as durable per-chat enforcement keyed by `senderRef`.
While active, a new group message from that sender is deleted before it enters
either the command or AI queue. Expired mute rows are removed lazily.

## Isolation and recovery

Incoming human commands and ordinary AI messages use separate bounded queues
and worker pools. A frozen model call therefore cannot consume command workers.

Model-generated group commands use the typed effect outbox. They execute only
after the companion visible reply has a successful durable receipt. A network
outcome that might already have changed WhatsApp becomes `unknown_outcome` and
is not blindly replayed. SQLite migrations preserve the moderation level,
group-command payload, and mute state across restart.

## Exit criteria

- Only `reply_message` and `react_to_message` are exposed as model tools.
- Permission levels 0–3 grant exactly the documented moderation commands.
- Mark-read and presence work automatically without permission configuration.
- Model command strings cannot exceed the current level or escape `/group`.
- Model execution never gains human owner/admin identity.
- Cross-chat message targets and unresolved `senderRef` values fail closed.
- AI and human command lanes remain operationally independent.
- Full tests, race tests, vet, build, and a real-device canary complete before
  Part 3 is considered production-verified.
