package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// MetaAIChat sends to Meta AI from the bot's own WhatsApp account. Meta AI
// answers in the same chat; its messages come back through
// Coordinator.ReceiveMetaAI.
type MetaAIChat interface {
	SendMetaAIText(ctx context.Context, text string) error
	// SendMetaAIImage sends one image, with caption when it is not empty.
	SendMetaAIImage(ctx context.Context, data []byte, mimeType, caption string) error
}

// MetaAIEvent is one message or edit from Meta AI. A reply streams as a
// first message and edits of it; a generated image comes as a message of its
// own.
type MetaAIEvent struct {
	// ReplyID names the reply the event belongs to: the ID of its first
	// message, shared by that message's edits.
	ReplyID string
	// Final is set when the reply is complete.
	Final bool
	// ImagePending announces an image that is still downloading; the event
	// with the image follows.
	ImagePending bool
	// Text is the reply's whole text so far.
	Text string
	// Image is a generated image and ImageMime its type.
	Image     []byte
	ImageMime string
}

const (
	// metaAIMaxImages bounds the images one message to Meta AI carries.
	metaAIMaxImages = 4
	// metaAIMaxImageBytes bounds one input image.
	metaAIMaxImageBytes = 16 * 1024 * 1024
	// metaAIResetCommand clears Meta AI's memory of the conversation.
	metaAIResetCommand = "/reset-ai"
)

// The waits of one exchange. Variables only so tests can shorten them.
var (
	// metaAIFirstReply is how long Meta AI may take to start answering.
	metaAIFirstReply = 2 * time.Minute
	// metaAISettle is the quiet that ends an answer whose replies are all
	// final: a generated image arrives just after the text announcing it.
	metaAISettle = 6 * time.Second
	// metaAIStall ends an answer that has content but went quiet with a
	// reply still unfinished: Meta AI does not always finish every message.
	metaAIStall = 30 * time.Second
	// metaAIMaxAnswer bounds one answer, however long it keeps streaming.
	metaAIMaxAnswer = 5 * time.Minute
	// metaAIResetWait bounds the wait for the reset's acknowledgement.
	metaAIResetWait = 30 * time.Second
	// metaAIResetSettle is the quiet that ends the reset's acknowledgement.
	metaAIResetSettle = 2 * time.Second
)

// userMessageFile matches the text files resolveInputs writes for a message
// that holds text; Meta AI gets their text in the prompt instead.
var userMessageFile = regexp.MustCompile(`^user_message\d+\.txt$`)

// metaAI runs tasks through Meta AI. Meta AI keeps one conversation per
// account, so tasks run one at a time, whichever chat they come from: each
// starts by resetting the conversation unless it corrects the task that
// used it last.
type metaAI struct {
	chat   MetaAIChat
	logger *slog.Logger

	// turn is held for a whole exchange.
	turn sync.Mutex

	mu sync.Mutex
	// events is where Meta AI's messages go while an exchange listens; nil
	// otherwise, and they are dropped.
	events chan MetaAIEvent
	// running is the session of the exchange in progress, and followUps the
	// steering it has yet to send.
	running   string
	followUps []metaAIMessage
	// lastSession is the task whose conversation Meta AI still holds.
	lastSession string
}

// metaAIMessage is one message to Meta AI: text, with the images before it.
type metaAIMessage struct {
	text   string
	images []metaAIImage
}

type metaAIImage struct {
	data []byte
	mime string
}

// metaAIAnswer is what Meta AI sent back for one message.
type metaAIAnswer struct {
	texts  []string
	images []metaAIImage
	// unfinished counts the replies that never said they were complete.
	unfinished int
}

func (answer *metaAIAnswer) empty() bool { return len(answer.texts) == 0 && len(answer.images) == 0 }

// errMetaAINotRunning is steering for a task whose exchange already ended.
var errMetaAINotRunning = errors.New("the Meta AI task already finished")

func newMetaAI(chat MetaAIChat, logger *slog.Logger) *metaAI {
	return &metaAI{chat: chat, logger: logger}
}

// receive hands one of Meta AI's messages to the exchange listening, if any.
// It never blocks: it runs on the WhatsApp event goroutine.
func (backend *metaAI) receive(event MetaAIEvent) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.events == nil {
		return
	}
	select {
	case backend.events <- event:
	default:
		backend.logger.Warn("Meta AI event dropped: the exchange is not keeping up")
	}
}

// metaAIUnsupportedError names input files Meta AI cannot take.
type metaAIUnsupportedError struct{ names []string }

func (err *metaAIUnsupportedError) Error() string {
	return "Meta AI can only take images. These inputs are not images and were not sent: " + strings.Join(err.names, ", ") +
		". Tell the user this kind of file can't be handled here."
}

// message builds what is sent to Meta AI from an instruction and its input
// files: the images, then the instruction with the text of any text-only
// messages. Any other file fails the whole message.
func (backend *metaAI) message(instruction string, files []string) (metaAIMessage, error) {
	message := metaAIMessage{text: strings.TrimSpace(instruction)}
	var texts []string
	var unsupported []string
	for _, path := range files {
		name := filepath.Base(path)
		if userMessageFile.MatchString(name) {
			data, err := os.ReadFile(path)
			if err != nil {
				return metaAIMessage{}, err
			}
			if text := strings.TrimSpace(string(data)); text != "" {
				texts = append(texts, text)
			}
			continue
		}
		kind, mimeType := DetectKind(path, name, "")
		if kind != "image" {
			unsupported = append(unsupported, fmt.Sprintf("%s (%s)", name, kind))
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return metaAIMessage{}, err
		}
		if info.Size() > metaAIMaxImageBytes {
			unsupported = append(unsupported, fmt.Sprintf("%s (image larger than %s)", name, formatSize(metaAIMaxImageBytes)))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return metaAIMessage{}, err
		}
		message.images = append(message.images, metaAIImage{data: data, mime: mimeType})
	}
	if len(unsupported) > 0 {
		return metaAIMessage{}, &metaAIUnsupportedError{names: unsupported}
	}
	if len(message.images) > metaAIMaxImages {
		return metaAIMessage{}, fmt.Errorf("Meta AI takes at most %d images at once; this task had %d. Pick the ones that matter and try again", metaAIMaxImages, len(message.images))
	}
	if len(texts) > 0 {
		message.text += "\n\nText from the chat:\n" + strings.Join(texts, "\n\n")
	}
	return message, nil
}

// steer queues instruction and files for the running exchange of task. It
// fails once that exchange ended: the delivering turn can still correct it.
func (backend *metaAI) steer(task Task, instruction string, files []string) error {
	message, err := backend.message(instruction, files)
	if err != nil {
		return err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.running != task.SessionID {
		return errMetaAINotRunning
	}
	backend.followUps = append(backend.followUps, message)
	return nil
}

// run carries out task: it waits its turn, resets Meta AI unless the task
// corrects the one Meta AI last worked on, sends the instruction and its
// images, and collects the answer and any follow-ups. Generated images are
// written to outDir. A task Meta AI did not answer at all is an error.
func (backend *metaAI) run(ctx context.Context, task Task, files []string, outDir string) (Result, string, error) {
	message, err := backend.message(task.Instruction, files)
	if err != nil {
		return Result{}, "", err
	}
	backend.turn.Lock()
	defer backend.turn.Unlock()

	events := make(chan MetaAIEvent, 256)
	backend.mu.Lock()
	backend.events, backend.running, backend.followUps = events, task.SessionID, nil
	continuing := task.PreviousSessionID != "" && task.PreviousSessionID == backend.lastSession
	backend.mu.Unlock()
	defer func() {
		backend.mu.Lock()
		backend.events, backend.running, backend.followUps = nil, "", nil
		backend.mu.Unlock()
	}()

	if !continuing {
		if err := backend.reset(ctx, events); err != nil {
			return Result{}, "", err
		}
	}
	backend.mu.Lock()
	backend.lastSession = task.SessionID
	backend.mu.Unlock()

	var answer metaAIAnswer
	for {
		part, err := backend.exchange(ctx, events, message)
		if err != nil {
			if answer.empty() {
				return Result{}, "", err
			}
			// A follow-up failed: the earlier answer still stands.
			backend.logger.Warn("Meta AI follow-up failed", "session", task.SessionID, "error", err)
			answer.texts = append(answer.texts, "(A follow-up to Meta AI got no answer: "+err.Error()+")")
			break
		}
		if part.unfinished > 0 {
			backend.logger.Warn("Meta AI went quiet with replies unfinished; using what came", "session", task.SessionID,
				"unfinished", part.unfinished, "texts", len(part.texts), "images", len(part.images))
		}
		answer.texts = append(answer.texts, part.texts...)
		answer.images = append(answer.images, part.images...)
		backend.mu.Lock()
		if len(backend.followUps) == 0 {
			// Steering arriving from now on is refused, not lost.
			backend.running = ""
			backend.mu.Unlock()
			break
		}
		message, backend.followUps = backend.followUps[0], backend.followUps[1:]
		backend.mu.Unlock()
	}
	result, err := writeMetaAIImages(outDir, answer.images)
	if err != nil {
		return Result{}, "", err
	}
	report := strings.Join(answer.texts, "\n\n")
	if report == "" {
		report = "(Meta AI answered with images only.)"
	}
	return result, report, nil
}

// reset clears Meta AI's memory and waits for it to acknowledge, so the
// acknowledgement is not taken for the answer. A reset that is never
// acknowledged is only logged.
func (backend *metaAI) reset(ctx context.Context, events chan MetaAIEvent) error {
	if err := backend.chat.SendMetaAIText(ctx, metaAIResetCommand); err != nil {
		return fmt.Errorf("could not reach Meta AI: %w", err)
	}
	if _, err := collectMetaAI(ctx, events, metaAIResetWait, metaAIResetSettle, metaAIResetWait); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		backend.logger.Warn("Meta AI did not acknowledge the reset", "error", err)
	}
	return nil
}

// exchange sends one message and collects its answer. Images before the
// last go first, each answered and discarded, so the answer collected is
// the one to the instruction, sent as the last image's caption.
func (backend *metaAI) exchange(ctx context.Context, events chan MetaAIEvent, message metaAIMessage) (metaAIAnswer, error) {
	for index, image := range message.images {
		caption := ""
		if index == len(message.images)-1 {
			caption = message.text
		}
		if err := backend.chat.SendMetaAIImage(ctx, image.data, image.mime, caption); err != nil {
			return metaAIAnswer{}, fmt.Errorf("could not send an image to Meta AI: %w", err)
		}
		if caption == "" {
			if _, err := collectMetaAI(ctx, events, metaAIResetWait, metaAIResetSettle, metaAIFirstReply); err != nil && ctx.Err() != nil {
				return metaAIAnswer{}, ctx.Err()
			}
		}
	}
	if len(message.images) == 0 {
		if err := backend.chat.SendMetaAIText(ctx, message.text); err != nil {
			return metaAIAnswer{}, fmt.Errorf("could not send the request to Meta AI: %w", err)
		}
	}
	return collectMetaAI(ctx, events, metaAIFirstReply, metaAISettle, metaAIMaxAnswer)
}

// collectMetaAI gathers one answer: every reply that starts until all are
// final and settle passed with nothing new. An answer with content that
// stays quiet for metaAIStall ends too, unfinished replies and all. It fails
// when nothing came within first; after total it returns what came.
func collectMetaAI(ctx context.Context, events <-chan MetaAIEvent, first, settle, total time.Duration) (metaAIAnswer, error) {
	type reply struct {
		text  string
		final bool
	}
	replies := make(map[string]*reply)
	var order []string
	var answer metaAIAnswer
	firstTimer := time.NewTimer(first)
	defer firstTimer.Stop()
	totalTimer := time.NewTimer(total)
	defer totalTimer.Stop()
	quiet := time.NewTimer(settle)
	quiet.Stop()
	defer quiet.Stop()
	seen := false
	var lastEvent time.Time
	build := func() metaAIAnswer {
		for _, id := range order {
			if text := strings.TrimSpace(replies[id].text); text != "" {
				answer.texts = append(answer.texts, text)
			}
			if !replies[id].final {
				answer.unfinished++
			}
		}
		return answer
	}
	for {
		select {
		case <-ctx.Done():
			return metaAIAnswer{}, ctx.Err()
		case event := <-events:
			if !event.Final && !event.ImagePending && event.Text == "" && len(event.Image) == 0 {
				// An unfinished message with nothing in it: Meta AI may never
				// finish it, and there is nothing to wait for.
				continue
			}
			lastEvent = time.Now()
			if !seen {
				seen = true
				firstTimer.Stop()
			}
			current := replies[event.ReplyID]
			if current == nil {
				current = &reply{}
				replies[event.ReplyID] = current
				order = append(order, event.ReplyID)
			}
			if event.Text != "" {
				current.text = event.Text
			}
			if len(event.Image) > 0 {
				answer.images = append(answer.images, metaAIImage{data: event.Image, mime: event.ImageMime})
			}
			current.final = current.final || event.Final
			quiet.Reset(settle)
		case <-quiet.C:
			done := true
			content := len(answer.images) > 0
			for _, current := range replies {
				done = done && current.final
				content = content || strings.TrimSpace(current.text) != ""
			}
			if done || (content && time.Since(lastEvent) >= metaAIStall) {
				return build(), nil
			}
			quiet.Reset(settle)
		case <-firstTimer.C:
			return metaAIAnswer{}, fmt.Errorf("Meta AI did not answer within %s", first)
		case <-totalTimer.C:
			if !seen {
				return metaAIAnswer{}, fmt.Errorf("Meta AI did not answer within %s", total)
			}
			return build(), nil
		}
	}
}

// writeMetaAIImages saves the generated images as the task's output files.
func writeMetaAIImages(dir string, images []metaAIImage) (Result, error) {
	result := Result{Success: true}
	if len(images) == 0 {
		return result, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, agent.NewError(agent.ErrorStorageFailure, "save Meta AI image", err)
	}
	for index, image := range images {
		mimeType := image.mime
		if mimeType == "" {
			mimeType = sniffMime(image.data)
		}
		name := fmt.Sprintf("meta_ai_image_%d%s", index+1, imageExtension(mimeType))
		path := filepath.Join(dir, name)
		if err := writeFileAtomic(path, image.data); err != nil {
			return Result{}, agent.NewError(agent.ErrorStorageFailure, "save Meta AI image", err)
		}
		sum := sha256.Sum256(image.data)
		result.Files = append(result.Files, OutputFile{
			Name: name, Path: path, Mime: mimeType, Size: int64(len(image.data)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return result, nil
}

func imageExtension(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".jpg"
}
