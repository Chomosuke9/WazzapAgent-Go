package hypermeow

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func TestCommandMediaPrefersOwnMediaThenQuotedMedia(t *testing.T) {
	quoted := &waE2E.ContextInfo{
		StanzaID:      proto.String("quoted"),
		QuotedMessage: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{DirectPath: proto.String("/sticker"), IsAnimated: proto.Bool(true)}},
	}
	own := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/image"), Caption: proto.String("/sticker"), ContextInfo: quoted}}

	decode := func(payload []byte) *waE2E.Message {
		t.Helper()
		message := &waE2E.Message{}
		if err := protojson.Unmarshal(payload, message); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		return message
	}
	fromOwn := decode(commandMedia("/sticker", own, quoted))
	if fromOwn.GetImageMessage().GetDirectPath() != "/image" || fromOwn.GetImageMessage().GetContextInfo() != nil {
		t.Fatalf("own media = %v", fromOwn)
	}
	text := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("/sticker"), ContextInfo: quoted}}
	fromQuote := decode(commandMedia("/sticker", text, quoted))
	if fromQuote.GetStickerMessage().GetDirectPath() != "/sticker" || !fromQuote.GetStickerMessage().GetIsAnimated() {
		t.Fatalf("quoted media = %v", fromQuote)
	}
	if commandMedia("look at this", own, quoted) != nil {
		t.Fatal("media was captured for a message that is not a command")
	}
	if commandMedia("/help", &waE2E.Message{Conversation: proto.String("/help")}, nil) != nil {
		t.Fatal("media was captured for a command without media")
	}
}

func TestCommandMediaUnwrapsViewOnceAndMarksLottie(t *testing.T) {
	quoted := &waE2E.ContextInfo{QuotedMessage: &waE2E.Message{LottieStickerMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{DirectPath: proto.String("/lottie"), Mimetype: proto.String("application/was")}},
	}}}
	payload := commandMedia("/add-sticker party", &waE2E.Message{Conversation: proto.String("/add-sticker party")}, quoted)
	media, err := (&Adapter{}).DownloadMedia(context.Background(), payload)
	if err != nil || media.Kind != command.MediaSticker || len(media.Lottie) == 0 || len(media.Data) != 0 || !media.Animated {
		t.Fatalf("lottie media = %#v, err=%v", media, err)
	}

	viewOnce := &waE2E.ContextInfo{QuotedMessage: &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{DirectPath: proto.String("/video"), GifPlayback: proto.Bool(true)}},
	}}}
	message := &waE2E.Message{}
	if err := protojson.Unmarshal(commandMedia("/sticker", &waE2E.Message{Conversation: proto.String("/sticker")}, viewOnce), message); err != nil ||
		message.GetVideoMessage().GetDirectPath() != "/video" {
		t.Fatalf("view-once media = %v, err=%v", message, err)
	}
}

func TestBoundedFileRefusesWritesPastTheLimit(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "bounded")
	if err != nil {
		t.Fatal(err)
	}
	defer temp.Close()
	bounded := &boundedFile{file: temp, limit: 8}
	if _, err := io.Copy(bounded, strings.NewReader("12345678")); err != nil {
		t.Fatalf("write within limit: %v", err)
	}
	if _, err := io.Copy(bounded, strings.NewReader("9")); !errors.Is(err, errMediaTooLarge) {
		t.Fatalf("write past limit err = %v", err)
	}
	if _, err := bounded.WriteAt([]byte("xx"), 7); !errors.Is(err, errMediaTooLarge) {
		t.Fatalf("WriteAt past limit err = %v", err)
	}
}
