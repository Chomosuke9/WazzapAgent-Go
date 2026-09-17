package mention

import "testing"

func TestRewriteUsesExactBoundedTokensWithoutCascading(t *testing.T) {
	text := "@123 dan @1234; x@123; @123_5; @999"
	got := Rewrite(text, map[string]string{
		"@123":  "@Alice @999 (a1b2c3)",
		"@1234": "@Bob (d4e5f6)",
		"@999":  "@Carol (g7h8i9)",
	})
	want := "@Alice @999 (a1b2c3) dan @Bob (d4e5f6); x@123; @123_5; @Carol (g7h8i9)"
	if got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
}

func TestContainsAndValidToken(t *testing.T) {
	if !Contains("halo @123!", "@123") || Contains("halo @1234", "@123") || Contains("x@123", "@123") {
		t.Fatal("mention boundaries were not enforced")
	}
	for _, invalid := range []string{"", "123", "@", "@abc", "@123_"} {
		if ValidToken(invalid) {
			t.Fatalf("accepted invalid token %q", invalid)
		}
	}
}
