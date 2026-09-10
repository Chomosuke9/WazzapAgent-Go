package identity

import "testing"

func TestSemanticUUIDsAndSenderRefs(t *testing.T) {
	tenant, err := NewTenantID()
	if err != nil {
		t.Fatalf("new tenant ID: %v", err)
	}
	if _, err := ParseTenantID(tenant.String()); err != nil {
		t.Fatalf("parse tenant ID: %v", err)
	}
	if _, err := ParseTenantID("not-an-id"); err == nil {
		t.Fatal("accepted malformed tenant ID")
	}

	first, err := NewSenderRef()
	if err != nil {
		t.Fatalf("new sender ref: %v", err)
	}
	second, err := NewSenderRef()
	if err != nil {
		t.Fatalf("new second sender ref: %v", err)
	}
	if first == second {
		t.Fatalf("two random sender refs collided: %s", first.String())
	}
	if len(first.String()) != 6 {
		t.Fatalf("sender ref length = %d, want 6", len(first.String()))
	}
	if _, err := ParseSenderRef(first.String()); err != nil {
		t.Fatalf("parse sender ref: %v", err)
	}
	if _, err := ParseSenderRef("u_01234567"); err == nil {
		t.Fatal("accepted legacy prefixed sender ref")
	}
	if _, err := ParseSenderRef("u_ILOUBAD0"); err == nil {
		t.Fatal("accepted malformed sender ref")
	}
}

func TestProviderAndPolicyIDsAreValidatedSlugs(t *testing.T) {
	if _, err := ParseProviderID("openai-compatible"); err != nil {
		t.Fatalf("parse provider ID: %v", err)
	}
	if _, err := ParsePolicyID("owner-only.v1"); err != nil {
		t.Fatalf("parse policy ID: %v", err)
	}
	if _, err := ParseProviderID("OpenAI"); err == nil {
		t.Fatal("accepted noncanonical provider ID")
	}
}
