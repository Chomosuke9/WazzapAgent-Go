# Local patches to hypermeow

This is github.com/polymorfa/hypermeow at v0.0.0-20260811011529-930d77bfc312,
used through the `replace` directive in the root go.mod. Only the change below
differs from upstream. To upgrade, copy the new upstream module here and
re-apply it.

## Native-flow stanza nodes (send.go)

Upstream sends native-flow messages (quick-reply buttons and similar) with a
business-hosting `biz` node: `actual_actors`, `host_storage` and
`privacy_mode_ts` attributes plus a `quality_control` child. WhatsApp rejects
that from an ordinary account with error 405.

`buildPersonalNativeFlowNodes` sends the nodes Baileys-based senders use
instead, which is what the original WazzapAgent bot sent successfully:

    <biz><interactive type="native_flow" v="1"><native_flow v="9" name="mixed"/></interactive></biz>
    <bot biz_bot="1"/>   (only when the chat is not a group)

Test: `TestPersonalNativeFlowNodes` in messaging_test.go.
