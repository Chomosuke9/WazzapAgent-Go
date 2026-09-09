package hypermeow

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	waLog "github.com/polymorfa/hypermeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestNormalizeTextMessageAndTrustedPolicyFlags(t *testing.T) {
	adapter, ownJID := normalizationAdapter(t)
	chat := types.NewJID("15550000002", types.DefaultUserServer)
	sender := types.NewADJID("15550000001", 0, 7)
	adapter.owner = sender.ToNonAD().String()
	adapter.allowlist[chat.String()] = struct{}{}
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000001", types.HiddenUserServer)},
			ID:            types.MessageID("provider-message-id"),
			PushName:      "Test User",
			Timestamp:     time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}
	candidate, ok := adapter.normalizeMessage(event)
	if !ok {
		t.Fatal("text event was not normalized")
	}
	if candidate.ChatKind != conversation.ChatDirect || candidate.Text != "hello" || !candidate.Owner || !candidate.Allowlisted {
		t.Fatalf("normalized candidate = %#v", candidate)
	}
	if candidate.SenderLID.String() != "10000000001@lid" {
		t.Fatalf("LID was not canonicalized: %q", candidate.SenderLID.String())
	}
	if ownJID.IsEmpty() {
		t.Fatal("test own JID is empty")
	}
}

func TestGroupRequiresExplicitMentionOfCurrentAccount(t *testing.T) {
	adapter, ownJID := normalizationAdapter(t)
	chat := types.NewJID("120363000000000001", types.GroupServer)
	sender := types.NewJID("15550000003", types.DefaultUserServer)
	adapter.allowlist[chat.String()] = struct{}{}
	message := func(mentions []string) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000003", types.HiddenUserServer), IsGroup: true},
				ID:            types.MessageID("group-message"),
				Timestamp:     time.Now().UTC(),
			},
			Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("hello group"), ContextInfo: &waE2E.ContextInfo{MentionedJID: mentions},
			}},
		}
	}
	withoutMention, ok := adapter.normalizeMessage(message([]string{"15550000999@s.whatsapp.net"}))
	if !ok || withoutMention.MentionsBot {
		t.Fatalf("non-mention candidate = %#v, ok=%v", withoutMention, ok)
	}
	withMention, ok := adapter.normalizeMessage(message([]string{ownJID.String()}))
	if !ok || !withMention.MentionsBot || withMention.ChatKind != conversation.ChatGroup {
		t.Fatalf("mention candidate = %#v, ok=%v", withMention, ok)
	}
}

func TestNormalizeCarriesOnlyQuotedProviderIdentityToDurableBoundary(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	chat := types.NewJID("120363000000000009", types.GroupServer)
	sender := types.NewJID("15550000003", types.DefaultUserServer)
	adapter.allowlist[chat.String()] = struct{}{}
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000003", types.HiddenUserServer), IsGroup: true},
			ID:            types.MessageID("reply-message"), Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("reply"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("quoted-provider-id")},
		}},
	}
	candidate, ok := adapter.normalizeMessage(event)
	if !ok || candidate.ProviderQuotedMessageID != "quoted-provider-id" {
		t.Fatalf("quoted candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestNormalizeStickerAsTranscriptPlaceholder(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	chat := types.NewJID("120363000000000010", types.GroupServer)
	sender := types.NewJID("15550000003", types.DefaultUserServer)
	adapter.allowlist[chat.String()] = struct{}{}
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000003", types.HiddenUserServer), IsGroup: true},
			ID:            types.MessageID("sticker-message"), Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}},
	}
	candidate, ok := adapter.normalizeMessage(event)
	if !ok || candidate.Text != "【sticker】" || !candidate.Allowlisted || candidate.ChatKind != conversation.ChatGroup {
		t.Fatalf("sticker candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestNormalizeRejectsNonTextAndEdits(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID("15550000002", types.DefaultUserServer),
			Sender: types.NewJID("15550000001", types.DefaultUserServer), SenderAlt: types.NewJID("10000000001", types.HiddenUserServer),
		},
		ID: types.MessageID("id"), Timestamp: time.Now().UTC(),
	}
	if _, ok := adapter.normalizeMessage(&events.Message{Info: info, Message: &waE2E.Message{}}); ok {
		t.Fatal("accepted non-text message")
	}
	if _, ok := adapter.normalizeMessage(&events.Message{Info: info, Message: &waE2E.Message{Conversation: proto.String("edit")}, IsEdit: true}); ok {
		t.Fatal("accepted edited message")
	}
}

func TestNormalizeFailsClosedWithoutSenderLID(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	event := &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{
			Chat: types.NewJID("15550000002", types.DefaultUserServer), Sender: types.NewJID("15550000001", types.DefaultUserServer),
		}, ID: "missing-lid", Timestamp: time.Now().UTC()},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}
	if _, ok := adapter.normalizeMessage(event); ok {
		t.Fatal("message without a trusted LID was accepted")
	}
}

func TestIgnoredNativeEventLogDoesNotExposePayloadOrProviderIdentity(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	var output bytes.Buffer
	adapter.logger = slog.New(slog.NewJSONHandler(&output, nil))
	secretText := "message-body-must-not-be-logged"
	secretID := "provider-id-must-not-be-logged"
	adapter.handleEvent(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("15550000022", types.DefaultUserServer),
				Sender: types.NewJID("15550000021", types.DefaultUserServer), SenderAlt: types.NewJID("10000000021", types.HiddenUserServer),
			},
			ID: types.MessageID(secretID), Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String(secretText)}, IsEdit: true,
	})
	logged := output.String()
	if !strings.Contains(logged, "edited_message") {
		t.Fatalf("ignore reason missing from log: %s", logged)
	}
	for _, sensitive := range []string{secretText, secretID, "15550000021", "15550000022"} {
		if strings.Contains(logged, sensitive) {
			t.Fatalf("normal log exposed sensitive value %q: %s", sensitive, logged)
		}
	}
}

func TestDirectMessageUsesLIDIdentityAndPhoneAliasForAllowlist(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	lid := types.NewJID("10000000001", types.HiddenUserServer)
	phone := types.NewJID("15550000011", types.DefaultUserServer)
	adapter.owner = phone.String()
	adapter.allowlist[phone.String()] = struct{}{}
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid, Sender: lid, SenderAlt: phone, AddressingMode: types.AddressingModeLID},
			ID:            types.MessageID("lid-message"), Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}
	candidate, ok := adapter.normalizeMessage(event)
	if !ok || !candidate.Owner || !candidate.Allowlisted {
		t.Fatalf("alternate identity candidate = %#v, ok=%v", candidate, ok)
	}
	if candidate.ProviderChatAddress != phone.String() || candidate.SenderLID.String() != lid.String() || candidate.ProviderSenderPhone != phone.String() {
		t.Fatalf("did not preserve LID identity and phone alias: %#v", candidate)
	}
}

func TestGroupSenderUsesLIDIdentityAndPhoneAliasForOwner(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	chat := types.NewJID("120363000000000002", types.GroupServer)
	lid := types.NewJID("10000000002", types.HiddenUserServer)
	phone := types.NewJID("15550000012", types.DefaultUserServer)
	adapter.owner = phone.String()
	adapter.allowlist[chat.String()] = struct{}{}
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: lid, SenderAlt: phone, IsGroup: true, AddressingMode: types.AddressingModeLID},
			ID:            "group-lid-message", Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String("hello group")},
	}
	candidate, ok := adapter.normalizeMessage(event)
	if !ok || !candidate.Owner || candidate.SenderLID.String() != lid.String() || candidate.ProviderSenderPhone != phone.String() {
		t.Fatalf("group alternate identity candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestTerminalPairingSinkIsExplicitOutput(t *testing.T) {
	var output bytes.Buffer
	sink := &TerminalPairingSink{Writer: &output}
	if err := sink.ShowPairingCode("sensitive-payload", 20*time.Second); err != nil {
		t.Fatalf("show code: %v", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("WhatsApp pairing QR")) ||
		!bytes.Contains(output.Bytes(), []byte("Linked devices")) {
		t.Fatal("terminal pairing sink omitted scannable instructions")
	}
	if bytes.Contains(output.Bytes(), []byte("sensitive-payload")) {
		t.Fatal("terminal pairing sink printed the raw payload next to the QR")
	}
}

func TestTerminalPairingSinkRejectsInvalidInputAndWriterFailure(t *testing.T) {
	if err := (&TerminalPairingSink{Writer: &bytes.Buffer{}}).ShowPairingCode("", time.Second); err == nil {
		t.Fatal("empty pairing payload was accepted")
	}
	if err := (&TerminalPairingSink{Writer: failingWriter{}}).ShowPairingCode("payload", time.Second); err == nil {
		t.Fatal("pairing writer failure was ignored")
	}
}

func TestInitialConnectionKeepsRuntimeContextAliveAfterSuccess(t *testing.T) {
	runtimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan account.ConnectionEvent, 1)
	fatal := make(chan error, 1)
	observedContext := make(chan context.Context, 1)

	err := waitForInitialConnection(
		runtimeCtx,
		time.Second,
		func(ctx context.Context) error {
			observedContext <- ctx
			connected <- account.ConnectionEvent{Connected: true}
			return nil
		},
		func() bool { return false },
		connected,
		fatal,
	)
	if err != nil {
		t.Fatalf("wait for initial connection: %v", err)
	}
	connectionCtx := <-observedContext
	select {
	case <-connectionCtx.Done():
		t.Fatalf("successful connection context was cancelled: %v", connectionCtx.Err())
	default:
	}

	cancel()
	select {
	case <-connectionCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("connection context did not follow runtime shutdown")
	}
}

func TestInitialConnectionTimeoutIsBounded(t *testing.T) {
	runtimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})

	err := waitForInitialConnection(
		runtimeCtx,
		10*time.Millisecond,
		func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		func() bool { return false },
		make(chan account.ConnectionEvent),
		make(chan error),
	)
	<-started
	if !agent.IsCode(err, agent.ErrorTimeout) {
		t.Fatalf("initial connection error = %v, want timeout", err)
	}
}

func TestConnectionLifecycleLogsDoNotExposePairingIdentity(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	adapter.events = make(chan account.ConnectionEvent, 2)
	var output bytes.Buffer
	adapter.logger = slog.New(slog.NewJSONHandler(&output, nil))
	secretNumber := "15550000888"
	secretBusiness := "private-business-name"

	adapter.handleEvent(&events.PairSuccess{
		ID:           types.NewJID(secretNumber, types.DefaultUserServer),
		BusinessName: secretBusiness,
	})
	adapter.handleEvent(&events.Connected{})
	if !adapter.Ready() {
		t.Fatal("connected event did not mark adapter ready")
	}
	adapter.handleEvent(&events.Disconnected{})
	if adapter.Ready() {
		t.Fatal("disconnected event left adapter ready")
	}

	logged := output.String()
	for _, expected := range []string{"WhatsApp pairing completed", "WhatsApp account connected", "WhatsApp account disconnected"} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("lifecycle log omitted %q: %s", expected, logged)
		}
	}
	for _, sensitive := range []string{secretNumber, secretBusiness} {
		if strings.Contains(logged, sensitive) {
			t.Fatalf("lifecycle log exposed sensitive value %q: %s", sensitive, logged)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func normalizationAdapter(t *testing.T) (*Adapter, types.JID) {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	ownJID := types.NewJID("15550000099", types.DefaultUserServer)
	device := &store.Device{ID: &ownJID}
	return &Adapter{
		tenantID:  tenantID,
		accountID: accountID,
		allowlist: make(map[string]struct{}),
		client:    whatsmeow.NewClient(device, waLog.Noop),
	}, ownJID
}
