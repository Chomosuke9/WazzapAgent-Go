package hypermeow

import (
	"context"
	"errors"
	"time"

	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// MarkRead is automatic AI-lane feedback. It is intentionally not exposed as
// an LLM tool and failures remain best-effort so provider UX cannot fail a
// durable conversation turn.
func (adapter *Adapter) MarkRead(ctx context.Context, key agent.Key, messageID identity.MessageID) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "mark WhatsApp message read", errors.New("account is not connected"))
	}
	chat, providerMessageID, sender, occurredAt, err := adapter.resolveEffectTarget(ctx, key, messageID)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if err := adapter.client.MarkRead(requestCtx, []types.MessageID{providerMessageID}, occurredAt, chat, sender); err != nil {
		return nativeEffectError(requestCtx, "send automatic WhatsApp read receipt", err)
	}
	return nil
}

// SetComposing automatically brackets model generation with composing/paused.
func (adapter *Adapter) SetComposing(ctx context.Context, key agent.Key, composing bool) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "set WhatsApp composing state", errors.New("account is not connected"))
	}
	target, err := adapter.resolveChatTarget(ctx, key)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if composing {
		state = types.ChatPresenceComposing
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if err := adapter.client.SendChatPresence(requestCtx, target, state, types.ChatPresenceMediaText); err != nil {
		return nativeEffectError(requestCtx, "send automatic WhatsApp presence", err)
	}
	return nil
}

func (adapter *Adapter) DeleteMessage(ctx context.Context, key agent.Key, messageID identity.MessageID) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "delete WhatsApp message", errors.New("account is not connected"))
	}
	chat, providerMessageID, sender, _, err := adapter.resolveEffectTarget(ctx, key, messageID)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	if err := adapter.authorizeMessageDeletion(requestCtx, chat, sender); err != nil {
		return err
	}
	_, err = adapter.client.SendMessage(requestCtx, chat, adapter.client.BuildRevoke(chat, sender, providerMessageID))
	if err != nil {
		return nativeEffectError(requestCtx, "delete muted WhatsApp message", err)
	}
	return nil
}

// ExecuteEffect is the native edge for a typed effect. The effect package
// carries only internal IDs; provider message IDs and JIDs are resolved here,
// after the dispatcher has completed its policy recheck.
func (adapter *Adapter) ExecuteEffect(ctx context.Context, stored effect.Stored) (string, error) {
	if !adapter.Ready() {
		return "", agent.NewError(agent.ErrorNotReady, "execute WhatsApp effect", errors.New("account is not connected"))
	}
	if value, ok := stored.Request.Effect.(effect.Sticker); ok {
		return adapter.executeSticker(ctx, stored.Request.Ref.Key, value)
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	var targetID identity.MessageID
	switch typed := stored.Request.Effect.(type) {
	case effect.React:
		targetID = typed.TargetMessageID
	case effect.DeleteMessage:
		targetID = typed.TargetMessageID
	default:
		return "", agent.NewError(agent.ErrorIntegrityFailure, "execute WhatsApp effect", errors.New("effect type is invalid"))
	}
	chat, messageID, sender, _, err := adapter.resolveEffectTarget(requestCtx, stored.Request.Ref.Key, targetID)
	if err != nil {
		return "", err
	}
	stripe := adapter.sendStripe(stored.Request.Ref.Key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	if value, ok := stored.Request.Effect.(effect.React); ok {
		response, sendErr := adapter.client.SendMessage(requestCtx, chat, adapter.client.BuildReaction(chat, sender, messageID, value.Emoji))
		if sendErr != nil {
			return "", nativeEffectError(requestCtx, "send WhatsApp reaction", sendErr)
		}
		return string(response.ID), nil
	}
	if err := adapter.authorizeMessageDeletion(requestCtx, chat, sender); err != nil {
		return "", err
	}
	response, sendErr := adapter.client.SendMessage(requestCtx, chat, adapter.client.BuildRevoke(chat, sender, messageID))
	if sendErr != nil {
		return "", nativeEffectError(requestCtx, "send WhatsApp revoke", sendErr)
	}
	return string(response.ID), nil
}

// executeSticker sends a catalog sticker the model chose. A name that was
// removed from the catalog since the model saw it fails without sending.
func (adapter *Adapter) executeSticker(ctx context.Context, key agent.Key, value effect.Sticker) (string, error) {
	if adapter.stickers == nil {
		return "", agent.NewError(agent.ErrorUnavailable, "send WhatsApp sticker", errors.New("sticker catalog is not configured"))
	}
	stored, err := adapter.stickers.LoadSticker(ctx, key, value.Name)
	if err != nil {
		return "", err
	}
	return adapter.sendSticker(ctx, key, stored, value.QuotedMessageID)
}

func (adapter *Adapter) resolveChatTarget(ctx context.Context, key agent.Key) (types.JID, error) {
	address, err := adapter.targets.ResolveChatAddress(ctx, key)
	if err != nil {
		return types.EmptyJID, err
	}
	target, err := types.ParseJID(address)
	if err != nil || target.IsEmpty() {
		return types.EmptyJID, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp target", errors.New("stored target is invalid"))
	}
	return target.ToNonAD(), nil
}

func (adapter *Adapter) resolveEffectTarget(ctx context.Context, key agent.Key, targetID identity.MessageID) (types.JID, types.MessageID, types.JID, time.Time, error) {
	address, providerMessageID, senderAddress, occurredAt, err := adapter.targets.ResolveMessageTarget(ctx, key, targetID)
	if err != nil {
		return types.EmptyJID, "", types.EmptyJID, time.Time{}, err
	}
	chat, err := types.ParseJID(address)
	if err != nil || chat.IsEmpty() || providerMessageID == "" || occurredAt.IsZero() {
		return types.EmptyJID, "", types.EmptyJID, time.Time{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp effect target", errors.New("stored message target is invalid"))
	}
	sender := types.EmptyJID
	if senderAddress != "" {
		sender, err = types.ParseJID(senderAddress)
		if err != nil || sender.IsEmpty() {
			return types.EmptyJID, "", types.EmptyJID, time.Time{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp effect target", errors.New("stored message sender is invalid"))
		}
	}
	return chat.ToNonAD(), types.MessageID(providerMessageID), sender.ToNonAD(), occurredAt.UTC(), nil
}

func nativeEffectError(ctx context.Context, operation string, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		return agent.NewError(agent.ErrorTimeout, operation, ctx.Err())
	}
	if ctx.Err() == context.Canceled {
		return agent.NewError(agent.ErrorCancelled, operation, ctx.Err())
	}
	return agent.NewError(agent.ErrorProviderFailure, operation, err)
}
