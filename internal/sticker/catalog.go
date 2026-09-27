package sticker

import (
	"context"
	"regexp"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// namePattern is what a catalog name may look like: short, lowercase, and
// safe to list in a model tool schema.
var namePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ValidName reports whether name can be stored in a catalog.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Sticker is one named catalog entry. It holds either WebP bytes or, for
// WhatsApp's Lottie (premium) stickers, the provider payload that resends
// the sticker unchanged.
type Sticker struct {
	Name     string
	WebP     []byte
	Animated bool
	Lottie   []byte
}

// Catalog is each chat's named sticker collection. Names are unique per chat.
type Catalog interface {
	// SaveSticker adds the sticker, or replaces the one with the same name.
	SaveSticker(ctx context.Context, key agent.Key, sticker Sticker) (replaced bool, err error)
	DeleteSticker(ctx context.Context, key agent.Key, name string) (deleted bool, err error)
	// StickerNames lists the chat's sticker names, sorted.
	StickerNames(ctx context.Context, key agent.Key) ([]string, error)
	// LoadSticker returns an agent.ErrorNotFound error for an unknown name.
	LoadSticker(ctx context.Context, key agent.Key, name string) (Sticker, error)
}
