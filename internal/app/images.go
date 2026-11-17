package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// messageImages downloads the image of an incoming chat message from
// WhatsApp, for the model to look at. It implements agent.ImageReader.
type messageImages struct {
	adapter *whatsapp.Adapter
	inbound *appsqlite.InboundStore
	logger  *slog.Logger
}

func (images messageImages) MessageImage(ctx context.Context, key agent.Key, messageID identity.MessageID) (string, []byte, error) {
	payload, err := images.inbound.ReadMessageMedia(ctx, key, messageID)
	if err != nil {
		// A view-once image, or one received before media was kept.
		return "", nil, err
	}
	media, err := images.adapter.DownloadMedia(ctx, payload)
	if err != nil {
		images.logger.Warn("image not shown to the model: download failed", "code", agent.CodeOf(err), "error", err)
		return "", nil, err
	}
	mimeType := http.DetectContentType(media.Data)
	if media.Kind != command.MediaImage || !strings.HasPrefix(mimeType, "image/") {
		return "", nil, agent.NewError(agent.ErrorNotFound, "read message image", errors.New("message media is not an image"))
	}
	return mimeType, media.Data, nil
}
