package subagent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// fakeMetaAI is Meta AI: it records what it is sent and answers each
// message with the events respond returns, delivered to receive.
type fakeMetaAI struct {
	mu      sync.Mutex
	sent    []string
	respond func(sent string) []MetaAIEvent
	receive func(MetaAIEvent)
}

func (fake *fakeMetaAI) SendMetaAIText(_ context.Context, text string) error {
	fake.answer(text)
	return nil
}

func (fake *fakeMetaAI) SendMetaAIImage(_ context.Context, _ []byte, _ string, caption string) error {
	fake.answer("image:" + caption)
	return nil
}

func (fake *fakeMetaAI) answer(sent string) {
	fake.mu.Lock()
	fake.sent = append(fake.sent, sent)
	respond, receive := fake.respond, fake.receive
	fake.mu.Unlock()
	if respond == nil {
		return
	}
	events := respond(sent)
	go func() {
		for _, event := range events {
			time.Sleep(2 * time.Millisecond)
			receive(event)
		}
	}()
}

func (fake *fakeMetaAI) sentMessages() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.sent...)
}

// fastMetaAI shortens the exchange's waits for the test.
func fastMetaAI(t *testing.T) {
	t.Helper()
	first, settle, stall, total, resetWait, resetSettle := metaAIFirstReply, metaAISettle, metaAIStall, metaAIMaxAnswer, metaAIResetWait, metaAIResetSettle
	metaAIFirstReply, metaAISettle, metaAIStall, metaAIMaxAnswer, metaAIResetWait, metaAIResetSettle =
		time.Second, 40*time.Millisecond, 300*time.Millisecond, 5*time.Second, time.Second, 20*time.Millisecond
	t.Cleanup(func() {
		metaAIFirstReply, metaAISettle, metaAIStall, metaAIMaxAnswer, metaAIResetWait, metaAIResetSettle = first, settle, stall, total, resetWait, resetSettle
	})
}

// streamed is a text reply as Meta AI streams it: a first message, then
// edits ending with the final one.
func streamed(id string, parts ...string) []MetaAIEvent {
	events := make([]MetaAIEvent, 0, len(parts))
	text := ""
	for index, part := range parts {
		text += part
		events = append(events, MetaAIEvent{ReplyID: id, Text: text, Final: index == len(parts)-1})
	}
	return events
}

func resetAck(sent string) []MetaAIEvent {
	if sent == metaAIResetCommand {
		return streamed("reset", "The AI will be reset ", "to its default state.")
	}
	return nil
}

func newMetaAITestCoordinator(t *testing.T, chat *fakeMetaAI, rec *recorder, store *memoryStore) (*Coordinator, agent.Key) {
	tenant, _ := identity.NewTenantID()
	account, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	key := agent.Key{TenantID: tenant, AccountID: account, ChatID: chatID}
	coordinator, err := NewCoordinator(Config{DataDir: t.TempDir(), EnabledDefault: true}, Dependencies{
		TenantID: tenant, AccountID: account, Store: store, Messenger: rec, Inputs: rec, MetaAI: chat,
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.receive = coordinator.ReceiveMetaAI
	coordinator.BindTurns(rec)
	t.Cleanup(coordinator.Close)
	return coordinator, key
}

func TestMetaAIGeneratesAnImageAndDeliversIt(t *testing.T) {
	fastMetaAI(t)
	image := []byte("\xff\xd8\xff\xe0 not really a jpeg")
	chat := &fakeMetaAI{respond: func(sent string) []MetaAIEvent {
		if sent != "Draw a cat astronaut" {
			return resetAck(sent)
		}
		// As seen in production: besides the streamed text and the image,
		// Meta AI sends an empty message it never finishes.
		return []MetaAIEvent{
			{ReplyID: "r1", Text: "Creating your image..."},
			{ReplyID: "empty"},
			{ReplyID: "r1", Text: "Your image is ready", Final: true},
			{ReplyID: "r2", ImagePending: true},
			{ReplyID: "r2", Final: true, Image: image, ImageMime: "image/jpeg"},
		}
	}}
	rec := &recorder{sent: make(chan struct{}, 4)}
	store := newMemoryStore()
	coordinator, key := newMetaAITestCoordinator(t, chat, rec, store)
	if coordinator.Backend() != agent.SubagentMetaAI || coordinator.ChatContext(context.Background(), key).Backend != agent.SubagentMetaAI {
		t.Fatal("the coordinator does not say Meta AI runs its tasks")
	}
	invocation, _ := identity.NewInvocationID()

	receipt, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{Instruction: "Draw a cat astronaut"})
	if err != nil || !strings.HasPrefix(receipt, "task:") {
		t.Fatalf("execute = %q, %v", receipt, err)
	}
	started := time.Now()
	select {
	case <-rec.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the result turn never ran")
	}
	if elapsed := time.Since(started); elapsed >= metaAIStall {
		t.Fatalf("the answer took %s: the empty unfinished message was waited for", elapsed)
	}
	waitFor(t, "the image to be sent", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.files) == 1
	})

	if got := chat.sentMessages(); len(got) != 2 || got[0] != metaAIResetCommand || got[1] != "Draw a cat astronaut" {
		t.Fatalf("sent to Meta AI = %q, want a reset then the instruction", got)
	}
	rec.mu.Lock()
	turn, file := rec.turns[0], rec.files[0]
	rec.mu.Unlock()
	if !strings.Contains(turn, "Your image is ready") || !strings.Contains(turn, "Success: True") || strings.Contains(turn, "reset") {
		t.Fatalf("result turn = %q", turn)
	}
	if file.Kind != "image" || file.Name != "meta_ai_image_1.jpg" {
		t.Fatalf("sent file = %+v", file)
	}
	if data, err := os.ReadFile(file.Path); err != nil || string(data) != string(image) {
		t.Fatalf("saved image = %q, %v", data, err)
	}
}

func TestMetaAIRefusesDocumentsWithoutContactingIt(t *testing.T) {
	fastMetaAI(t)
	chat := &fakeMetaAI{respond: resetAck}
	rec := &recorder{sent: make(chan struct{}, 4)}
	coordinator, key := newMetaAITestCoordinator(t, chat, rec, newMemoryStore())
	invocation, _ := identity.NewInvocationID()
	fileMessage, _ := identity.NewMessageID()

	// The recorder's input is a .txt document and the message's caption.
	if _, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{
		Instruction: "Summarise it", FileMessageIDs: []identity.MessageID{fileMessage},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rec.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never delivered")
	}
	rec.mu.Lock()
	turn := rec.turns[0]
	rec.mu.Unlock()
	if !strings.Contains(turn, "Success: False") || !strings.Contains(turn, "can only take images") || !strings.Contains(turn, "input.txt") {
		t.Fatalf("result turn = %q", turn)
	}
	if got := chat.sentMessages(); len(got) != 0 {
		t.Fatalf("Meta AI was sent %q for a document task", got)
	}
}

func TestMetaAICorrectionKeepsTheConversationAndOthersReset(t *testing.T) {
	fastMetaAI(t)
	chat := &fakeMetaAI{}
	backend := newMetaAI(chat, slog.New(slog.DiscardHandler))
	chat.receive = backend.receive
	chat.respond = func(sent string) []MetaAIEvent {
		if sent == metaAIResetCommand {
			return resetAck(sent)
		}
		return streamed("answer-"+sent, "Done: ", sent)
	}
	ctx := context.Background()

	first := Task{SessionID: "chat_a_1", Instruction: "Draw a dog"}
	if _, report, err := backend.run(ctx, first, nil, t.TempDir()); err != nil || report != "Done: Draw a dog" {
		t.Fatalf("first task = %q, %v", report, err)
	}
	correction := Task{SessionID: "chat_a_2", Instruction: "Make it a cat", PreviousSessionID: first.SessionID}
	if _, report, err := backend.run(ctx, correction, nil, t.TempDir()); err != nil || report != "Done: Make it a cat" {
		t.Fatalf("correction = %q, %v", report, err)
	}
	// A correction of a task that is no longer Meta AI's latest starts over.
	stale := Task{SessionID: "chat_b_1", Instruction: "Make it blue", PreviousSessionID: first.SessionID}
	if _, _, err := backend.run(ctx, stale, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	want := []string{metaAIResetCommand, "Draw a dog", "Make it a cat", metaAIResetCommand, "Make it blue"}
	if got := chat.sentMessages(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent = %q, want %q", got, want)
	}
}

func TestMetaAIFollowUpGoesToTheRunningExchange(t *testing.T) {
	fastMetaAI(t)
	chat := &fakeMetaAI{}
	backend := newMetaAI(chat, slog.New(slog.DiscardHandler))
	chat.receive = backend.receive
	task := Task{SessionID: "chat_a_1", Instruction: "Draw a dog"}
	steered := make(chan error, 1)
	chat.respond = func(sent string) []MetaAIEvent {
		switch sent {
		case metaAIResetCommand:
			return resetAck(sent)
		case "Draw a dog":
			// The user changes their mind while Meta AI is answering.
			steered <- backend.steer(task, "Make it orange", nil)
			return streamed("first", "A dog")
		}
		return streamed("second", "An orange dog")
	}

	_, report, err := backend.run(context.Background(), task, nil, t.TempDir())
	if err != nil || <-steered != nil {
		t.Fatalf("run = %v", err)
	}
	if report != "A dog\n\nAn orange dog" {
		t.Fatalf("report = %q", report)
	}
	if err := backend.steer(task, "Too late", nil); !errors.Is(err, errMetaAINotRunning) {
		t.Fatalf("steering a finished exchange = %v", err)
	}
}

func TestMetaAIMessageSendsImagesAndInlinesTextMessages(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nrest"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := filepath.Join(dir, "user_message2.txt")
	if err := os.WriteFile(text, []byte("it is my cat"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newMetaAI(&fakeMetaAI{}, slog.New(slog.DiscardHandler))
	message, err := backend.message("Describe the photo", []string{png, text})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.images) != 1 || message.images[0].mime != "image/png" || !strings.Contains(message.text, "it is my cat") {
		t.Fatalf("message = %+v", message)
	}
	pdf := filepath.Join(dir, "invoice.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	var unsupported *metaAIUnsupportedError
	if _, err := backend.message("Read it", []string{png, pdf}); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "invoice.pdf") {
		t.Fatalf("a PDF = %v", err)
	}
}

func TestCollectMetaAIWaitsForEveryReplyToFinish(t *testing.T) {
	events := make(chan MetaAIEvent, 8)
	events <- MetaAIEvent{ReplyID: "a", Text: "Creating"}
	go func() {
		// Longer than the settle time: an unfinished reply keeps it waiting.
		time.Sleep(60 * time.Millisecond)
		events <- MetaAIEvent{ReplyID: "a", Text: "Ready", Final: true}
	}()
	answer, err := collectMetaAI(context.Background(), events, time.Second, 20*time.Millisecond, time.Second)
	if err != nil || len(answer.texts) != 1 || answer.texts[0] != "Ready" {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
	if _, err := collectMetaAI(context.Background(), make(chan MetaAIEvent), 20*time.Millisecond, time.Millisecond, time.Second); err == nil {
		t.Fatal("silence was taken for an answer")
	}
}

func TestCollectMetaAIEndsAStalledAnswerThatHasContent(t *testing.T) {
	fastMetaAI(t)
	events := make(chan MetaAIEvent, 8)
	// A reply that starts with text and is never finished.
	events <- MetaAIEvent{ReplyID: "a", Text: "Here is half an answer"}
	started := time.Now()
	answer, err := collectMetaAI(context.Background(), events, time.Second, 20*time.Millisecond, 5*time.Second)
	if err != nil || len(answer.texts) != 1 || answer.unfinished != 1 {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
	if elapsed := time.Since(started); elapsed < metaAIStall || elapsed > 2*time.Second {
		t.Fatalf("a stalled answer ended after %s, want about %s", elapsed, metaAIStall)
	}
	// An image still downloading is waited for even with no text.
	events <- MetaAIEvent{ReplyID: "b", ImagePending: true}
	go func() {
		time.Sleep(100 * time.Millisecond)
		events <- MetaAIEvent{ReplyID: "b", Final: true, Image: []byte("img"), ImageMime: "image/jpeg"}
	}()
	answer, err = collectMetaAI(context.Background(), events, time.Second, 20*time.Millisecond, 5*time.Second)
	if err != nil || len(answer.images) != 1 || answer.unfinished != 0 {
		t.Fatalf("pending image answer = %+v, %v", answer, err)
	}
}

func TestResultImageBecomesTheReplysCaptionUnlessTheTurnCorrects(t *testing.T) {
	for _, correction := range []bool{false, true} {
		fastMetaAI(t)
		chat := &fakeMetaAI{respond: func(sent string) []MetaAIEvent {
			if sent == metaAIResetCommand {
				return resetAck(sent)
			}
			return []MetaAIEvent{
				{ReplyID: "r1", Text: "Your image is ready", Final: true},
				{ReplyID: "r2", Final: true, Image: []byte("\xff\xd8\xff"), ImageMime: "image/jpeg"},
			}
		}}
		rec := &recorder{sent: make(chan struct{}, 4)}
		store := newMemoryStore()
		store.subtaskTurns = map[identity.InvocationID]bool{}
		coordinator, key := newMetaAITestCoordinator(t, chat, rec, store)
		var attached *StagedFile
		rec.turn = func(key agent.Key, invocation identity.InvocationID, _ string) (agent.InvokeResult, error) {
			// What the reply dispatcher does: ask for the image, send it with
			// the reply as caption, and say so.
			store.mu.Lock()
			store.subtaskTurns[invocation] = correction
			store.mu.Unlock()
			file, err := coordinator.ReplyAttachment(context.Background(), key, invocation)
			if err != nil {
				return agent.InvokeResult{}, err
			}
			if attached = file; file != nil {
				if err := coordinator.AttachmentSent(context.Background(), key, invocation, "reply-receipt"); err != nil {
					return agent.InvokeResult{}, err
				}
			}
			actionID, _ := identity.NewActionID()
			return agent.InvokeResult{Text: "Here it is!", ActionID: actionID}, nil
		}
		invocation, _ := identity.NewInvocationID()
		if _, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{Instruction: "Draw a cat"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-rec.sent:
		case <-time.After(5 * time.Second):
			t.Fatal("the result turn never ran")
		}
		waitFor(t, "the delivery to finish", func() bool {
			store.mu.Lock()
			defer store.mu.Unlock()
			for _, task := range store.tasks {
				if task.DeliveredAt.IsZero() {
					return false
				}
			}
			return true
		})
		rec.mu.Lock()
		files, recorded := len(rec.files), append([]string(nil), rec.recorded...)
		rec.mu.Unlock()
		if correction {
			if attached != nil || files != 1 || len(recorded) != 0 {
				t.Fatalf("correcting turn: attached=%v files=%d recorded=%q, want the image sent on its own", attached, files, recorded)
			}
			continue
		}
		if attached == nil || attached.Kind != "image" || files != 0 || len(recorded) != 1 || recorded[0] != "meta_ai_image_1.jpg@reply-receipt" {
			t.Fatalf("attached=%v files=%d recorded=%q, want the image only as the reply's caption", attached, files, recorded)
		}
	}
}
