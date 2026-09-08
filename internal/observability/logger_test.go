package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var output bytes.Buffer
	logger, instanceID, err := NewLogger(&output, "info", "json")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	if len(instanceID) != 32 {
		t.Fatalf("instance ID length = %d, want 32", len(instanceID))
	}
	logger.Info("ready")
	logged := output.String()
	if !strings.Contains(logged, `"instance_id":"`+instanceID+`"`) || !strings.Contains(logged, `"msg":"ready"`) {
		t.Fatalf("unexpected structured log: %s", logged)
	}
}

func TestNewLoggerRejectsInvalidOptions(t *testing.T) {
	if _, _, err := NewLogger(&bytes.Buffer{}, "trace", "json"); err == nil {
		t.Fatal("invalid level accepted")
	}
	if _, _, err := NewLogger(&bytes.Buffer{}, "info", "yaml"); err == nil {
		t.Fatal("invalid format accepted")
	}
	if _, _, err := NewLogger(nil, "info", "json"); err == nil {
		t.Fatal("nil output accepted")
	}
}
