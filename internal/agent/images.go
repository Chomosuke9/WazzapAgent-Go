package agent

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	// MaxModelImages bounds the images one model request carries; the newest
	// are kept.
	MaxModelImages = 8
	// MaxModelImageBytes bounds one image. A larger one stays a placeholder.
	MaxModelImageBytes = 8 << 20
)

// ModelImage is one image the model sees with a request.
type ModelImage struct {
	// Label says what the image is; the model reads it right before the image.
	Label string
	Mime  string
	Data  []byte
}

// ImageReader reads the image a chat message carries.
type ImageReader interface {
	// MessageImage returns the image of message messageID and its MIME type,
	// or an error when the message has no image or it cannot be fetched.
	MessageImage(ctx context.Context, key Key, messageID identity.MessageID) (mimeType string, data []byte, err error)
}

// currentBurst returns the entries after the bot's last sent reply: the
// "current messages(burst)" section of the transcript.
func currentBurst(entries []HistoryEntry) []HistoryEntry {
	start := 0
	for index, entry := range entries {
		if entry.Role == HistoryAssistant && entry.Delivery == DeliverySucceeded {
			start = index + 1
		}
	}
	return entries[start:]
}

// currentImages collects the images the model sees on this turn: those of the
// current burst's messages and, on the turn that delivers a sub-agent result,
// the result's image files. An image in an older message is never sent again;
// the model only reads its placeholder. An image that cannot be read is
// skipped, so the turn still runs on the placeholder.
func (agent *Agent) currentImages(ctx context.Context, entries []HistoryEntry, chat ChatContext) []ModelImage {
	var images []ModelImage
	if agent.images != nil {
		for _, entry := range currentBurst(entries) {
			if entry.Role != HistoryUser || entry.MessageID.IsZero() {
				continue
			}
			if kind, _, ok := FileKind(flattenContent(entry.Content)); !ok || kind != "image" {
				continue
			}
			mimeType, data, err := agent.images.MessageImage(ctx, agent.key, entry.MessageID)
			if err != nil || len(data) == 0 || len(data) > MaxModelImageBytes {
				continue
			}
			images = append(images, ModelImage{
				Label: fmt.Sprintf("Image of message 【#%s】:", formatCompactContextID(entry.Sequence)),
				Mime:  mimeType, Data: data,
			})
		}
	}
	if chat.Subagent != nil {
		for _, image := range chat.Subagent.ResultImages {
			if len(image.Data) > 0 && len(image.Data) <= MaxModelImageBytes {
				images = append(images, image)
			}
		}
	}
	if len(images) > MaxModelImages {
		images = images[len(images)-MaxModelImages:]
	}
	return images
}
