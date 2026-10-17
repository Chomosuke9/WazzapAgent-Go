package policy_test

import (
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestEvaluatePermissionSupportsNegatedBotOrigin(t *testing.T) {
	human := policy.PermissionFacts{IsPrivate: true}
	bot := policy.PermissionFacts{IsPrivate: true, FromMe: true}

	allowed, err := policy.EvaluatePermission("isPrivate and !fromMe", human)
	if err != nil || !allowed {
		t.Fatalf("human permission = %v, %v", allowed, err)
	}
	allowed, err = policy.EvaluatePermission("isPrivate and !fromMe", bot)
	if err != nil || allowed {
		t.Fatalf("bot permission = %v, %v", allowed, err)
	}
}

func TestEvaluatePermissionUsesActualBotSenderFacts(t *testing.T) {
	bot := policy.PermissionFacts{IsGroup: true, IsAdmin: true, FromMe: true}
	allowed, err := policy.EvaluatePermission("isGroup and senderIsAdmin", bot)
	if err != nil || !allowed {
		t.Fatalf("admin bot permission = %v, %v", allowed, err)
	}
	allowed, err = policy.EvaluatePermission("(isGroup and senderIsAdmin) and !fromMe", bot)
	if err != nil || allowed {
		t.Fatalf("fromMe bot permission = %v, %v", allowed, err)
	}
}

func TestEvaluatePermissionHonorsNotAndPrecedence(t *testing.T) {
	facts := policy.PermissionFacts{IsOwner: true, IsGroup: true, IsAdmin: false}
	for expression, want := range map[string]bool{
		"owner or group and admin":    true,
		"(owner or group) and !admin": true,
		"!owner or group and admin":   false,
		"from_me or isOwner":          true,
	} {
		got, err := policy.EvaluatePermission(expression, facts)
		if err != nil || got != want {
			t.Errorf("EvaluatePermission(%q) = %v, %v; want %v", expression, got, err, want)
		}
	}
}

func TestValidatePermissionRejectsMalformedAndUnknownExpressions(t *testing.T) {
	for _, expression := range []string{
		"",
		"owner and",
		"(owner or admin",
		"owner xor admin",
		"owner && admin",
		"isBot",
	} {
		if err := policy.ValidatePermission(expression); err == nil {
			t.Errorf("ValidatePermission(%q) accepted invalid expression", expression)
		}
	}
}
