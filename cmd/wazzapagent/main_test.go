package main

import "testing"

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("WAZZAP_ENV_FILE", "")
	t.Setenv("WAZZAP_HTTP_ADDRESS", "invalid-address")

	if code := run(); code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
}
