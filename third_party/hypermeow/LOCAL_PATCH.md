This directory is a local copy of `github.com/polymorfa/hypermeow` at
`930d77bfc312a5c3ae78e8b4a136cf4de1767b6d` (module version
`v0.0.0-20260811011529-930d77bfc312`). The upstream license is retained in
`LICENSE`.

Local change in `send.go`: native-flow messages whose buttons are all
`single_select` use `name="mixed"` in the outgoing `biz/interactive/native_flow`
stanza. The protobuf message and its `buttonParamsJSON` are untouched. The
previous WazzapAgent Baileys sender used `name="mixed"` for the same settings
menu, while this Hypermeow revision emitted `name="single_select"` and WhatsApp
rejected the send with server ACK error 405.

Remove the `go.mod` replacement and this copy when upstream provides the same
behavior or a supported override. A successful build only validates the local
integration; delivery still needs a one-group live canary.
