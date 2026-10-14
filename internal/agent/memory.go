package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxMemories is how many long-term memories one chat keeps.
	MaxMemories = 50
	// MaxMemoryRunes is the longest a single memory may be, in characters.
	MaxMemoryRunes = 500
)

// Memory is one long-term note the bot keeps about a chat (/memory). The
// model sees every memory of the chat in <chat_state> on each turn.
type Memory struct {
	// Code is the short ID /memory delete takes. It never changes.
	Code string
	Text string
}

// ValidateMemoryText reports whether text can be saved as a memory.
func ValidateMemoryText(text string) error {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxMemoryRunes {
		return fmt.Errorf("a memory must be valid text of 1 to %d characters", MaxMemoryRunes)
	}
	return nil
}
