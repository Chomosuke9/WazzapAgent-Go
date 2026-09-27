package hypermeow

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

const (
	// maxMediaDownloadBytes bounds what a command may pull into memory.
	maxMediaDownloadBytes = 64 << 20
	mediaDownloadTimeout  = 2 * time.Minute
)

// commandMedia picks the media a slash command works on: the image or video
// the command was the caption of, or else the image, video or sticker it
// replied to. It returns that one media message, without its context info,
// serialized for the command store; nil when there is none.
func commandMedia(text string, message *waE2E.Message, contextInfo *waE2E.ContextInfo) []byte {
	if !strings.HasPrefix(strings.TrimSpace(text), "/") {
		return nil
	}
	media := mediaOnly(message)
	if media == nil && contextInfo != nil {
		media = mediaOnly(contextInfo.GetQuotedMessage())
	}
	if media == nil {
		return nil
	}
	encoded, err := protojson.Marshal(media)
	if err != nil || len(encoded) > conversation.MaxRawQuotedMessageBytes {
		return nil
	}
	return encoded
}

// mediaOnly returns a message holding only message's image, video or sticker,
// with view-once and ephemeral wrappers removed.
func mediaOnly(message *waE2E.Message) *waE2E.Message {
	for range 3 {
		inner := message.GetViewOnceMessage().GetMessage()
		if inner == nil {
			inner = message.GetViewOnceMessageV2().GetMessage()
		}
		if inner == nil {
			inner = message.GetEphemeralMessage().GetMessage()
		}
		if inner == nil {
			break
		}
		message = inner
	}
	switch {
	case message.GetImageMessage() != nil:
		image := proto.Clone(message.GetImageMessage()).(*waE2E.ImageMessage)
		image.ContextInfo = nil
		return &waE2E.Message{ImageMessage: image}
	case message.GetVideoMessage() != nil:
		video := proto.Clone(message.GetVideoMessage()).(*waE2E.VideoMessage)
		video.ContextInfo = nil
		return &waE2E.Message{VideoMessage: video}
	case message.GetStickerMessage() != nil:
		sticker := proto.Clone(message.GetStickerMessage()).(*waE2E.StickerMessage)
		sticker.ContextInfo = nil
		return &waE2E.Message{StickerMessage: sticker}
	case message.GetLottieStickerMessage().GetMessage().GetStickerMessage() != nil:
		sticker := proto.Clone(message.GetLottieStickerMessage().GetMessage().GetStickerMessage()).(*waE2E.StickerMessage)
		sticker.ContextInfo = nil
		sticker.IsLottie = proto.Bool(true)
		return &waE2E.Message{StickerMessage: sticker}
	}
	return nil
}

func isLottie(sticker *waE2E.StickerMessage) bool {
	return sticker.GetIsLottie() || sticker.GetMimetype() == "application/was"
}

// DownloadMedia fetches the media captured by commandMedia. A Lottie sticker
// is not downloaded: its payload is returned so it can be resent unchanged.
func (adapter *Adapter) DownloadMedia(ctx context.Context, payload []byte) (command.Media, error) {
	message := &waE2E.Message{}
	if err := protojson.Unmarshal(payload, message); err != nil {
		return command.Media{}, agent.NewError(agent.ErrorIntegrityFailure, "decode command media", err)
	}
	var (
		media        command.Media
		downloadable whatsmeow.DownloadableMessage
		length       uint64
	)
	switch {
	case message.GetImageMessage() != nil:
		media.Kind, downloadable, length = command.MediaImage, message.GetImageMessage(), message.GetImageMessage().GetFileLength()
	case message.GetVideoMessage() != nil:
		video := message.GetVideoMessage()
		media.Kind, media.Animated, downloadable, length = command.MediaVideo, video.GetGifPlayback(), video, video.GetFileLength()
	case message.GetStickerMessage() != nil:
		value := message.GetStickerMessage()
		media.Kind, media.Animated = command.MediaSticker, value.GetIsAnimated() || isLottie(value)
		if isLottie(value) {
			encoded, err := protojson.Marshal(value)
			if err != nil {
				return command.Media{}, agent.NewError(agent.ErrorInternal, "encode Lottie sticker", err)
			}
			media.Lottie = encoded
			return media, nil
		}
		downloadable, length = value, value.GetFileLength()
	default:
		return command.Media{}, agent.NewError(agent.ErrorNotFound, "decode command media", errors.New("payload holds no image, video or sticker"))
	}
	if length > maxMediaDownloadBytes {
		return command.Media{}, agent.NewError(agent.ErrorInvalidArgument, "download WhatsApp media", errors.New("media is too large"))
	}
	if !adapter.Ready() {
		return command.Media{}, agent.NewError(agent.ErrorNotReady, "download WhatsApp media", errors.New("account is not connected"))
	}
	downloadCtx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
	defer cancel()
	data, err := adapter.downloadBounded(downloadCtx, downloadable)
	if errors.Is(err, errMediaTooLarge) {
		return command.Media{}, agent.NewError(agent.ErrorInvalidArgument, "download WhatsApp media", err)
	}
	if err != nil {
		return command.Media{}, nativeEffectError(downloadCtx, "download WhatsApp media", err)
	}
	media.Data = data
	return media, nil
}

var errMediaTooLarge = errors.New("media is too large")

// downloadBounded downloads through a temp file that refuses writes past
// maxMediaDownloadBytes, so a message that understates its file length
// cannot make the download take unbounded memory or disk.
func (adapter *Adapter) downloadBounded(ctx context.Context, downloadable whatsmeow.DownloadableMessage) ([]byte, error) {
	temp, err := os.CreateTemp("", "wazzapagent-media-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := adapter.client.DownloadToFile(ctx, downloadable, &boundedFile{file: temp, limit: maxMediaDownloadBytes}); err != nil {
		return nil, err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(temp, maxMediaDownloadBytes))
}

// boundedFile is a whatsmeow.File that fails any write reaching past limit.
// It does not embed *os.File, whose ReadFrom would bypass Write.
type boundedFile struct {
	file  *os.File
	limit int64
}

func (bounded *boundedFile) Write(data []byte) (int, error) {
	offset, err := bounded.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if offset+int64(len(data)) > bounded.limit {
		return 0, errMediaTooLarge
	}
	return bounded.file.Write(data)
}

func (bounded *boundedFile) WriteAt(data []byte, offset int64) (int, error) {
	if offset+int64(len(data)) > bounded.limit {
		return 0, errMediaTooLarge
	}
	return bounded.file.WriteAt(data, offset)
}

func (bounded *boundedFile) Read(data []byte) (int, error) { return bounded.file.Read(data) }
func (bounded *boundedFile) ReadAt(data []byte, offset int64) (int, error) {
	return bounded.file.ReadAt(data, offset)
}
func (bounded *boundedFile) Seek(offset int64, whence int) (int64, error) {
	return bounded.file.Seek(offset, whence)
}
func (bounded *boundedFile) Truncate(size int64) error  { return bounded.file.Truncate(size) }
func (bounded *boundedFile) Stat() (os.FileInfo, error) { return bounded.file.Stat() }

var _ whatsmeow.File = (*boundedFile)(nil)

// SendSticker sends a catalog or freshly made sticker, quoting quoted when set.
func (adapter *Adapter) SendSticker(ctx context.Context, key agent.Key, value sticker.Sticker, quoted identity.MessageID) error {
	_, err := adapter.sendSticker(ctx, key, value, quoted)
	return err
}

func (adapter *Adapter) sendSticker(ctx context.Context, key agent.Key, value sticker.Sticker, quoted identity.MessageID) (string, error) {
	if len(value.WebP) == 0 && len(value.Lottie) == 0 {
		return "", agent.NewError(agent.ErrorInvalidArgument, "send WhatsApp sticker", errors.New("sticker has no content"))
	}
	result, err := adapter.send(ctx, key, "send WhatsApp sticker", func(sendCtx context.Context, address string, target types.JID) (*waE2E.Message, error) {
		contextInfo, err := adapter.quoteContext(sendCtx, key, address, target, quoted)
		if err != nil {
			return nil, err
		}
		if len(value.Lottie) > 0 {
			stored := &waE2E.StickerMessage{}
			if err := protojson.Unmarshal(value.Lottie, stored); err != nil {
				return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode Lottie sticker", err)
			}
			stored.ContextInfo = contextInfo
			return &waE2E.Message{LottieStickerMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{StickerMessage: stored}}}, nil
		}
		uploaded, err := adapter.client.Upload(sendCtx, value.WebP, whatsmeow.MediaImage)
		if err != nil {
			return nil, nativeEffectError(sendCtx, "upload WhatsApp sticker", err)
		}
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			Mimetype:      proto.String("image/webp"),
			Width:         proto.Uint32(sticker.Size),
			Height:        proto.Uint32(sticker.Size),
			IsAnimated:    proto.Bool(value.Animated),
			ContextInfo:   contextInfo,
		}}, nil
	})
	return result.ProviderReceipt, err
}
