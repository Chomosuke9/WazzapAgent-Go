package main

import (
	"os"
	"testing"
)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	previous, existed := os.LookupEnv("WAZZAP_HTTP_ADDRESS")
	if err := os.Setenv("WAZZAP_HTTP_ADDRESS", "invalid-address"); err != nil {
		t.Fatalf("set environment: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("WAZZAP_HTTP_ADDRESS", previous)
		} else {
			_ = os.Unsetenv("WAZZAP_HTTP_ADDRESS")
		}
	})

	if code := run(); code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
}
