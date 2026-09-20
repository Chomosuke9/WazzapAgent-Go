package policy

import "github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"

const (
	ChatAllowlistAll    = "*"
	ChatAllowlistDirect = "*@lid"
	ChatAllowlistGroup  = "*@g.us"
)

func IsChatAllowlistWildcard(value string) bool {
	switch value {
	case ChatAllowlistAll, ChatAllowlistDirect, ChatAllowlistGroup:
		return true
	default:
		return false
	}
}

func ChatAllowlistWildcardMatches(value string, kind conversation.ChatKind) bool {
	switch value {
	case ChatAllowlistAll:
		return kind == conversation.ChatDirect || kind == conversation.ChatGroup
	case ChatAllowlistDirect:
		return kind == conversation.ChatDirect
	case ChatAllowlistGroup:
		return kind == conversation.ChatGroup
	default:
		return false
	}
}
