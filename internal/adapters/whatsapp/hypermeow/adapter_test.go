package hypermeow

import (
	"bytes"
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
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
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
	if candidate.ProviderSenderAddress != "15550000001@s.whatsapp.net" {
		t.Fatalf("sender was not canonicalized: %q", candidate.ProviderSenderAddress)
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
				MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: true},
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

func TestNormalizeRejectsNonTextAndEdits(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID("15550000002", types.DefaultUserServer),
			Sender: types.NewJID("15550000001", types.DefaultUserServer),
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
				Sender: types.NewJID("15550000021", types.DefaultUserServer),
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

func TestDirectMessageUsesPhoneAlternateForStableIdentityAndAllowlist(t *testing.T) {
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
	if candidate.ProviderChatAddress != phone.String() || candidate.ProviderSenderAddress != phone.String() {
		t.Fatalf("did not prefer stable phone identity: %#v", candidate)
	}
}

func TestGroupSenderUsesPhoneAlternateForStableOwnerIdentity(t *testing.T) {
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
	if !ok || !candidate.Owner || candidate.ProviderSenderAddress != phone.String() {
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
