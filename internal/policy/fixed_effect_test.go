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

func TestFixedGateRechecksCurrentModelCapabilityAndLiveAuthority(t *testing.T) {
	key := fixedEffectKey(t)
	policyID, _ := identity.ParsePolicyID("part3-effects.v1")
	capabilities, _ := agent.NewCapabilitySet("message.react")
	configs := &fixedConfigReader{snapshot: agent.ConfigSnapshot{Version: 1, Permission: agent.PermissionConfig{
		PolicyID: policyID, Revision: 1, ModelCapabilities: capabilities,
	}}}
	chats := fixedChatAccess{}
	authority := &fixedAuthority{value: policy.ChatAuthority{ChatKind: conversation.ChatDirect, ObservedAt: time.Now().UTC().UnixMilli()}}
	gate, err := policy.NewFixedGate(policyID, 1, configs, chats, authority, true)
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
	if authority.calls != 1 {
		t.Fatalf("live authority calls = %d, want 1", authority.calls)
	}
	noCapabilities, _ := agent.NewCapabilitySet()
	configs.snapshot.Permission.ModelCapabilities = noCapabilities
	if err := gate.AuthorizeEffect(context.Background(), request); !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("revoked capability authorization = %v, want permission_denied", err)
	}
	if authority.calls != 1 {
		t.Fatalf("authority was read after an already revoked capability: %d", authority.calls)
	}
}

type fixedConfigReader struct{ snapshot agent.ConfigSnapshot }

func (reader *fixedConfigReader) Load(context.Context, agent.Key) (agent.ConfigSnapshot, error) {
	return reader.snapshot, nil
}

type fixedChatAccess struct{}

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
