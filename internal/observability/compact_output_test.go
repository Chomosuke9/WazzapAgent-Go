package observability

import (
	"bytes"
	"fmt"
	"testing"
)

func TestCompactHandlerActualOutput(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.Info("message received", "chatId", "120363429302106476@g.us", "senderId", "185775253680238@lid", "msgKey", "A5C0845F83BD47997B92EA5876C0BE23", "type", "notify", "msgContentType", "extendedTextMessage,messageContextInfo")

	logger.Info("prefix mode: no match; skipping", "chat_name", "HC (Hobi Coding)")

	logger.Info("LLM2 invoke start (provider=primary, mode=text, attempt=1/1, model=gemini/gemini-3.5-flash-lite)", "chat_name", "ChitChat")

	logger.Warn("reply target ignored: context id 000689 not present in allowed context ids", "chat_name", "ChitChat")

	fmt.Println("\n=== Actual Output ===")
	fmt.Print(buf.String())
	fmt.Println("=== End Output ===")
}
