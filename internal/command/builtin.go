package command

import (
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

type PromptMutation struct {
	ExpectedVersion agent.ConfigVersion
	AppliedVersion  agent.ConfigVersion
}

type PermissionCommandKind uint8

const (
	PermissionInvalid PermissionCommandKind = iota + 1
	PermissionView
	PermissionSet
)

type PermissionCommand struct {
	Kind  PermissionCommandKind
	Level agent.ModerationLevel
}

func ParsePermissionCommand(text string) (PermissionCommand, bool) {
	if text == "/permission" || text == "/permission view" {
		return PermissionCommand{Kind: PermissionView}, true
	}
	if !strings.HasPrefix(text, "/permission ") {
		return PermissionCommand{}, false
	}
	argument := strings.TrimSpace(strings.TrimPrefix(text, "/permission "))
	if len(argument) == 1 && argument[0] >= '0' && argument[0] <= '3' {
		return PermissionCommand{Kind: PermissionSet, Level: agent.ModerationLevel(argument[0] - '0')}, true
	}
	return PermissionCommand{Kind: PermissionInvalid}, true
}

func FormatModerationLevel(level agent.ModerationLevel) string {
	labels := [...]string{
		"Level 0: moderasi nonaktif.",
		"Level 1: delete.",
		"Level 2: delete dan mute.",
		"Level 3: delete, mute, dan kick.",
	}
	if !level.Valid() {
		return "Permission tidak valid."
	}
	return labels[level]
}

type PromptCommandKind uint8

const (
	PromptInvalid PromptCommandKind = iota + 1
	PromptView
	PromptSet
	PromptClear
)

type PromptCommand struct {
	Kind PromptCommandKind
	Text string
}

func ParsePromptCommand(text string) (PromptCommand, bool) {
	if text == "/prompt" || text == "/prompt view" {
		return PromptCommand{Kind: PromptView}, true
	}
	if text == "/prompt clear" {
		return PromptCommand{Kind: PromptClear}, true
	}
	if strings.HasPrefix(text, "/prompt set ") {
		value := strings.TrimPrefix(text, "/prompt set ")
		if strings.TrimSpace(value) == "" || len(value) > agent.MaxPromptBytes {
			return PromptCommand{Kind: PromptInvalid}, true
		}
		return PromptCommand{Kind: PromptSet, Text: value}, true
	}
	if text == "/prompt set" || strings.HasPrefix(text, "/prompt ") {
		return PromptCommand{Kind: PromptInvalid}, true
	}
	return PromptCommand{}, false
}
