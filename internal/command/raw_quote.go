package command

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

// RawQuotedMessage is command-only provider data used by /catch. It must not
// be added to model input or conversation history.
type RawQuotedMessage struct {
	ProviderMessageID string
	RemoteJID         string
	FromMe            bool
	MessageJSON       []byte
}

type RawQuotedMessageReader interface {
	ReadRawQuotedMessage(context.Context, conversation.IncomingMessage) (RawQuotedMessage, error)
}
