package policy_test

import (
	"context"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestFixedGateAlwaysAllowsReactionCapability(t *testing.T) {
	key := fixedEffectKey(t)
	policyID, _ := identity.ParsePolicyID("part3-effects.v1")
	configs := &fixedConfigReader{snapshot: agent.ConfigSnapshot{Version: 1, Permission: agent.PermissionConfig{
		PolicyID: policyID, Revision: 1, ModerationLevel: agent.ModerationNone,
	}}}
	chats := fixedChatAccess{}
	authority := &fixedAuthority{value: policy.ChatAuthority{ChatKind: conversation.ChatDirect, ObservedAt: time.Now().UTC().UnixMilli()}}
	gate, err := policy.NewFixedGate(policyID, 1, configs, chats, authority, "", true)
	if err != nil {
		t.Fatalf("create fixed gate: %v", err)
	}
	invocationID, _ := identity.NewInvocationID()
	principal, err := policy.ModelPrincipal(key, invocationID)
	if err != nil {
		t.Fatalf("create model principal: %v", err)
	}
	request := policy.EffectAuthorization{Key: key, Principal: principal, Capability: policy.CapabilityMessageReact}
	if err := gate.AuthorizeEffect(context.Background(), request); err != nil {
		t.Fatalf("authorize opted-in effect: %v", err)
	}
	configs.snapshot.Permission.ModerationLevel = agent.ModerationDeleteMuteKick
	if err := gate.AuthorizeEffect(context.Background(), request); err != nil {
		t.Fatalf("moderation level changed reaction authorization: %v", err)
	}
}

func TestFixedGateUsesPerChatInvocationTriggers(t *testing.T) {
	key := fixedEffectKey(t)
	policyID, _ := identity.ParsePolicyID("part3-effects.v1")
	gate, err := policy.NewFixedGate(policyID, 1, &fixedConfigReader{}, fixedChatAccess{}, &fixedAuthority{}, "Vivy", true)
	if err != nil {
		t.Fatal(err)
	}
	permission := agent.PermissionConfig{PolicyID: policyID, Revision: 1}
	message := fixedInvocationMessage(t, key, conversation.ChatGroup, "hey VIVY please help")

	if err := gate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission}); err == nil {
		t.Fatal("group message without an enabled trigger was accepted")
	}
	triggers := agent.TriggerConfig{Name: true}
	if err := gate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission, Triggers: triggers}); err != nil {
		t.Fatalf("assistant-name trigger was denied: %v", err)
	}
	triggers = agent.TriggerConfig{Name: true, NameRegex: true, NamePattern: `(?i)help$`}
	message.Text = "Vivy, HELP"
	if err := gate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission, Triggers: triggers}); err != nil {
		t.Fatalf("custom regex trigger was denied: %v", err)
	}
	triggers = agent.TriggerConfig{Name: true, NameRegex: true, NamePattern: `^Vivy`}
	message.Text = conversation.PlaceholderImage + " Vivy, what is this?"
	if err := gate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission, Triggers: triggers}); err != nil {
		t.Fatalf("anchored trigger did not match an image caption: %v", err)
	}
	imageGate, err := policy.NewFixedGate(policyID, 1, &fixedConfigReader{}, fixedChatAccess{}, &fixedAuthority{}, "Image", true)
	if err != nil {
		t.Fatal(err)
	}
	message.Text = conversation.PlaceholderImage
	if err := imageGate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission, Triggers: agent.TriggerConfig{Name: true}}); err == nil {
		t.Fatal("a media placeholder matched the assistant-name trigger")
	}
	message.ChatKind = conversation.ChatDirect
	if err := gate.AuthorizeInvocation(context.Background(), message, agent.ConfigSnapshot{Version: 1, Permission: permission}); err != nil {
		t.Fatalf("direct chat was incorrectly gated by group triggers: %v", err)
	}
}

func fixedInvocationMessage(t *testing.T, key agent.Key, kind conversation.ChatKind, text string) conversation.IncomingMessage {
	t.Helper()
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	participantID, _ := identity.NewParticipantID()
	senderRef, _ := identity.NewSenderRef()
	lid, _ := identity.ParseLID("10000000009@lid")
	now := time.Now().UTC()
	return conversation.IncomingMessage{
		ID: messageID, InvocationID: invocationID, CausationID: causationID,
		TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID,
		SenderID: participantID, SenderRef: senderRef, SenderLID: lid,
		SenderName: "Tester", ChatKind: kind, Text: text, Allowlisted: true,
		OccurredAt: now.Add(-time.Second), ReceivedAt: now,
	}
}

type fixedConfigReader struct{ snapshot agent.ConfigSnapshot }

func (reader *fixedConfigReader) Load(context.Context, agent.Key) (agent.ConfigSnapshot, error) {
	return reader.snapshot, nil
}

type fixedChatAccess struct{}

type fixedGroupChatAccess struct{ fixedChatAccess }

func (fixedGroupChatAccess) ReadHumanAccess(context.Context, policy.Principal) (policy.HumanAccess, error) {
	return policy.HumanAccess{ChatKind: conversation.ChatGroup, Allowlisted: true}, nil
}

func (fixedChatAccess) IsChatAllowlisted(context.Context, agent.Key) (bool, error) { return true, nil }
func (fixedChatAccess) ReadHumanAccess(context.Context, policy.Principal) (policy.HumanAccess, error) {
	return policy.HumanAccess{ChatKind: conversation.ChatDirect, Allowlisted: true}, nil
}

type fixedAuthority struct {
	value policy.ChatAuthority
	calls int
}

func (authority *fixedAuthority) ReadChatAuthority(context.Context, policy.Principal) (policy.ChatAuthority, error) {
	authority.calls++
	return authority.value, nil
}

func fixedEffectKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
