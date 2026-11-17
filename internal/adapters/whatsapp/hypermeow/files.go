package hypermeow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNG for image thumbnails
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode WebP for image thumbnails
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

const (
	// maxFileBytes bounds a file downloaded for, or sent from, the sub-agent.
	maxFileBytes = 200 << 20
	// fileTransferTimeout bounds one such download or upload.
	fileTransferTimeout = 10 * time.Minute
	// maxThumbnailSourceBytes is the largest image a preview is made for.
	maxThumbnailSourceBytes = 20 << 20
	thumbnailSize           = 96
)

// fileOnly returns a message holding only message's document or audio, with
// ephemeral and caption wrappers and the preview removed. mediaOnly covers
// images, videos and stickers; this covers the other files a sub-agent task
// may be given.
func fileOnly(message *waE2E.Message) *waE2E.Message {
	for range 3 {
		inner := message.GetEphemeralMessage().GetMessage()
		if inner == nil {
			inner = message.GetDocumentWithCaptionMessage().GetMessage()
		}
		if inner == nil {
			break
		}
		message = inner
	}
	switch {
	case message.GetDocumentMessage() != nil:
		document := proto.Clone(message.GetDocumentMessage()).(*waE2E.DocumentMessage)
		document.ContextInfo, document.JPEGThumbnail = nil, nil
		return &waE2E.Message{DocumentMessage: document}
	case message.GetAudioMessage() != nil:
		audio := proto.Clone(message.GetAudioMessage()).(*waE2E.AudioMessage)
		audio.ContextInfo, audio.Waveform = nil, nil
		return &waE2E.Message{AudioMessage: audio}
	}
	return nil
}

// DownloadFile downloads the file captured with a message (see commandMedia)
// into dir and returns its path. The file keeps a document's own name; other
// media are named after their kind. It returns an agent.ErrorNotFound error
// when payload holds no file.
func (adapter *Adapter) DownloadFile(ctx context.Context, payload []byte, dir string) (string, error) {
	message := &waE2E.Message{}
	if err := protojson.Unmarshal(payload, message); err != nil {
		return "", agent.NewError(agent.ErrorIntegrityFailure, "decode message file", err)
	}
	var (
		downloadable whatsmeow.DownloadableMessage
		length       uint64
		name         string
	)
	switch {
	case message.GetImageMessage() != nil:
		value := message.GetImageMessage()
		downloadable, length, name = value, value.GetFileLength(), "image"+extensionFor(value.GetMimetype(), ".jpg")
	case message.GetVideoMessage() != nil:
		value := message.GetVideoMessage()
		downloadable, length, name = value, value.GetFileLength(), "video"+extensionFor(value.GetMimetype(), ".mp4")
	case message.GetStickerMessage() != nil:
		value := message.GetStickerMessage()
		fallback := ".webp"
		if isLottie(value) {
			fallback = ".was"
		}
		downloadable, length, name = value, value.GetFileLength(), "sticker"+fallback
	case message.GetDocumentMessage() != nil:
		value := message.GetDocumentMessage()
		name = cleanFileName(value.GetFileName())
		if name == "" {
			name = cleanFileName(value.GetTitle())
		}
		if name == "" {
			name = "document" + extensionFor(value.GetMimetype(), ".bin")
		} else if filepath.Ext(name) == "" {
			name += extensionFor(value.GetMimetype(), "")
		}
		downloadable, length = value, value.GetFileLength()
	case message.GetAudioMessage() != nil:
		value := message.GetAudioMessage()
		downloadable, length, name = value, value.GetFileLength(), "audio"+extensionFor(value.GetMimetype(), ".ogg")
	default:
		return "", agent.NewError(agent.ErrorNotFound, "decode message file", errors.New("payload holds no file"))
	}
	if length > maxFileBytes {
		return "", agent.NewError(agent.ErrorInvalidArgument, "download WhatsApp file", errMediaTooLarge)
	}
	if !adapter.Ready() {
		return "", agent.NewError(agent.ErrorNotReady, "download WhatsApp file", errors.New("account is not connected"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", agent.NewError(agent.ErrorStorageFailure, "download WhatsApp file", err)
	}
	file, err := createUnique(dir, name)
	if err != nil {
		return "", agent.NewError(agent.ErrorStorageFailure, "download WhatsApp file", err)
	}
	downloadCtx, cancel := context.WithTimeout(ctx, fileTransferTimeout)
	defer cancel()
	err = adapter.client.DownloadToFile(downloadCtx, downloadable, &boundedFile{file: file, limit: maxFileBytes})
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		if errors.Is(err, errMediaTooLarge) {
			return "", agent.NewError(agent.ErrorInvalidArgument, "download WhatsApp file", err)
		}
		return "", nativeEffectError(downloadCtx, "download WhatsApp file", errors.Join(err, closeErr))
	}
	return file.Name(), nil
}

// createUnique creates dir/name, or dir/name_1, dir/name_2, ... when taken.
func createUnique(dir, name string) (*os.File, error) {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for counter := 0; counter < 1000; counter++ {
		candidate := name
		if counter > 0 {
			candidate = fmt.Sprintf("%s_%d%s", stem, counter, extension)
		}
		file, err := os.OpenFile(filepath.Join(dir, candidate), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("no free file name")
}

// cleanFileName keeps the base name of a name a sender chose, without
// control characters, so it cannot leave the directory it is written to.
func cleanFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	if len(name) > 160 {
		extension := filepath.Ext(name)
		if len(extension) > 16 {
			extension = ""
		}
		name = strings.ToValidUTF8(name[:160-len(extension)], "") + extension
	}
	return name
}

// extensionFor is the usual file extension of mimeType, or fallback.
func extensionFor(mimeType, fallback string) string {
	parsed, _, _ := mime.ParseMediaType(mimeType)
	switch parsed {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "application/pdf":
		return ".pdf"
	}
	if extensions, err := mime.ExtensionsByType(parsed); err == nil && len(extensions) > 0 {
		return extensions[0]
	}
	return fallback
}

// OutboundFile is a local file to send as one message. Kind is image,
// video, audio or document; anything else is sent as a document.
type OutboundFile struct {
	Path string
	Name string
	Mime string
	Kind string
}

// SendFile uploads file and sends it to the chat as its own message, without
// a caption. It returns the provider receipt. The upload has its own, longer
// time limit; only the final send is bound by the send timeout.
func (adapter *Adapter) SendFile(ctx context.Context, key agent.Key, file OutboundFile) (string, error) {
	const operation = "send WhatsApp file"
	if !adapter.Ready() {
		return "", agent.NewError(agent.ErrorNotReady, operation, errors.New("account is not connected"))
	}
	info, err := os.Stat(file.Path)
	if err != nil || !info.Mode().IsRegular() {
		return "", agent.NewError(agent.ErrorNotFound, operation, errors.New("file does not exist"))
	}
	if info.Size() > maxFileBytes {
		return "", agent.NewError(agent.ErrorInvalidArgument, operation, errMediaTooLarge)
	}
	mediaType := whatsmeow.MediaDocument
	switch file.Kind {
	case "image":
		mediaType = whatsmeow.MediaImage
	case "video":
		mediaType = whatsmeow.MediaVideo
	case "audio":
		mediaType = whatsmeow.MediaAudio
	}
	source, err := os.Open(file.Path)
	if err != nil {
		return "", agent.NewError(agent.ErrorStorageFailure, operation, err)
	}
	defer source.Close()
	uploadCtx, cancel := context.WithTimeout(ctx, fileTransferTimeout)
	defer cancel()
	uploaded, err := adapter.client.UploadReader(uploadCtx, source, nil, mediaType)
	if err != nil {
		return "", nativeEffectError(uploadCtx, "upload WhatsApp file", err)
	}
	mimeType := file.Mime
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var message *waE2E.Message
	switch file.Kind {
	case "image":
		value := &waE2E.ImageMessage{
			URL: proto.String(uploaded.URL), DirectPath: proto.String(uploaded.DirectPath), MediaKey: uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: proto.Uint64(uploaded.FileLength),
			Mimetype: proto.String(mimeType),
		}
		if thumbnail, width, height, ok := imageThumbnail(file.Path, info.Size()); ok {
			value.JPEGThumbnail, value.Width, value.Height = thumbnail, proto.Uint32(width), proto.Uint32(height)
		}
		message = &waE2E.Message{ImageMessage: value}
	case "video":
		message = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: proto.String(uploaded.URL), DirectPath: proto.String(uploaded.DirectPath), MediaKey: uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: proto.Uint64(uploaded.FileLength),
			Mimetype: proto.String(mimeType),
		}}
	case "audio":
		if mimeType == "audio/ogg" {
			mimeType = "audio/ogg; codecs=opus"
		}
		message = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(uploaded.URL), DirectPath: proto.String(uploaded.DirectPath), MediaKey: uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: proto.Uint64(uploaded.FileLength),
			Mimetype: proto.String(mimeType),
		}}
	default:
		name := cleanFileName(file.Name)
		if name == "" {
			name = "file"
		}
		message = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: proto.String(uploaded.URL), DirectPath: proto.String(uploaded.DirectPath), MediaKey: uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: proto.Uint64(uploaded.FileLength),
			Mimetype: proto.String(mimeType), FileName: proto.String(name), Title: proto.String(name),
		}}
	}
	result, err := adapter.send(ctx, key, operation, func(context.Context, string, types.JID) (*waE2E.Message, error) {
		return message, nil
	})
	return result.ProviderReceipt, err
}

// imageThumbnail makes the small JPEG preview WhatsApp shows while an image
// loads, and returns the image's size. ok is false when the image is too
// large or cannot be decoded; the image is then sent without a preview.
func imageThumbnail(path string, size int64) (thumbnail []byte, width, height uint32, ok bool) {
	if size > maxThumbnailSourceBytes {
		return nil, 0, 0, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, false
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > 64_000_000 {
		return nil, 0, 0, false
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, 0, 0, false
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, 0, 0, false
	}
	scaledWidth, scaledHeight := thumbnailSize, thumbnailSize
	if config.Width >= config.Height {
		scaledHeight = max(1, config.Height*thumbnailSize/config.Width)
	} else {
		scaledWidth = max(1, config.Width*thumbnailSize/config.Height)
	}
	scaled := image.NewRGBA(image.Rect(0, 0, scaledWidth, scaledHeight))
	draw.Draw(scaled, scaled.Bounds(), image.White, image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), decoded, decoded.Bounds(), draw.Over, nil)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, scaled, &jpeg.Options{Quality: 60}); err != nil {
		return nil, 0, 0, false
	}
	return encoded.Bytes(), uint32(config.Width), uint32(config.Height), true
}
