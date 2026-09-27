package commands

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sort"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

// Shared helpers for command tests. See README.md, "Testing a command".

// builtinRegistry is the registry built from every command file.
func builtinRegistry(t *testing.T) *command.Registry {
	t.Helper()
	registry, err := command.NewRegistry(All())
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	return registry
}

// recordingText, recordingButtons and handledStore stand in for the
// WhatsApp adapter and the inbox store.
type recordingText struct{ sent []string }

func (text *recordingText) SendText(_ context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	text.sent = append(text.sent, request.Text)
	return action.SendTextResult{}, nil
}

type recordingButtons struct{ sent []action.SendButtonsRequest }

func (buttons *recordingButtons) SendButtons(_ context.Context, request action.SendButtonsRequest) (action.SendTextResult, error) {
	buttons.sent = append(buttons.sent, request)
	return action.SendTextResult{}, nil
}

type handledStore struct {
	command.Store
	handled int
}

func (store *handledStore) MarkCommandHandled(context.Context, conversation.IncomingMessage) error {
	store.handled++
	return nil
}

// testChatKey returns a fresh, valid chat key.
func testChatKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}

// mediaStore is an inbox store whose command message carried media payload.
type mediaStore struct {
	handledStore
	payload []byte
}

func (store *mediaStore) ReadCommandMedia(context.Context, conversation.IncomingMessage) ([]byte, error) {
	if store.payload == nil {
		return nil, agent.NewError(agent.ErrorNotFound, "read command media", errors.New("none"))
	}
	return store.payload, nil
}

// fakeMedia "downloads" the same media for every payload.
type fakeMedia struct{ media command.Media }

func (fake fakeMedia) DownloadMedia(context.Context, []byte) (command.Media, error) {
	return fake.media, nil
}

// recordingStickers records sent stickers and keeps an in-memory catalog.
type recordingStickers struct {
	sent    []sticker.Sticker
	quoted  []identity.MessageID
	catalog map[string]sticker.Sticker
}

func (fake *recordingStickers) SendSticker(_ context.Context, _ agent.Key, value sticker.Sticker, quoted identity.MessageID) error {
	fake.sent = append(fake.sent, value)
	fake.quoted = append(fake.quoted, quoted)
	return nil
}

func (fake *recordingStickers) SaveSticker(_ context.Context, _ agent.Key, value sticker.Sticker) (bool, error) {
	if fake.catalog == nil {
		fake.catalog = map[string]sticker.Sticker{}
	}
	_, replaced := fake.catalog[value.Name]
	fake.catalog[value.Name] = value
	return replaced, nil
}

func (fake *recordingStickers) DeleteSticker(_ context.Context, _ agent.Key, name string) (bool, error) {
	_, found := fake.catalog[name]
	delete(fake.catalog, name)
	return found, nil
}

func (fake *recordingStickers) StickerNames(context.Context, agent.Key) ([]string, error) {
	names := make([]string, 0, len(fake.catalog))
	for name := range fake.catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (fake *recordingStickers) LoadSticker(_ context.Context, _ agent.Key, name string) (sticker.Sticker, error) {
	value, found := fake.catalog[name]
	if !found {
		return sticker.Sticker{}, agent.NewError(agent.ErrorNotFound, "load sticker", errors.New("none"))
	}
	return value, nil
}

// runStickerCommand dispatches text as sent by a private-chat user whose
// message carried media (nil for none).
func runStickerCommand(t *testing.T, text string, facts command.PermissionFacts, media *command.Media, stickers *recordingStickers) (*recordingText, error) {
	t.Helper()
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatalf("%q is not a registered command", text)
	}
	key := testChatKey(t)
	messageID, _ := identity.NewMessageID()
	store := &mediaStore{}
	platformMedia := fakeMedia{}
	if media != nil {
		store.payload = []byte(`{}`)
		platformMedia.media = *media
	}
	replies := &recordingText{}
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message:  conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, ID: messageID, Text: text},
		Facts:    facts,
		Platform: command.Platform{Text: replies, Media: platformMedia, Stickers: stickers, Catalog: stickers},
		Store:    store,
	})
	return replies, err
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
