package hypermeow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
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
	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
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
	candidate, ok := adapter.normalizeMessage(context.Background(), event)
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

func TestNormalizeUsesEventPushNameBeforeContactPushName(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	adapter.client.Store.Contacts = &testContactStore{pushName: "cached push name"}
	chat := types.NewJID("15550000002", types.DefaultUserServer)
	sender := types.NewJID("15550000001", types.DefaultUserServer)
	event := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000001", types.HiddenUserServer),
			},
			ID: types.MessageID("provider-message-id"), Timestamp: time.Now().UTC(), PushName: "event push name",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}

	candidate, ok := adapter.normalizeMessage(context.Background(), event)
	if !ok || candidate.SenderName != "event push name" {
		t.Fatalf("event push name was not preferred: %#v, ok=%v", candidate, ok)
	}

	event.Info.PushName = ""
	candidate, ok = adapter.normalizeMessage(context.Background(), event)
	if !ok || candidate.SenderName != "cached push name" {
		t.Fatalf("contact push name was not used as fallback: %#v, ok=%v", candidate, ok)
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
	withoutMention, ok := adapter.normalizeMessage(context.Background(), message([]string{"15550000999@s.whatsapp.net"}))
	if !ok || withoutMention.MentionsBot {
		t.Fatalf("non-mention candidate = %#v, ok=%v", withoutMention, ok)
	}
	withMention, ok := adapter.normalizeMessage(context.Background(), message([]string{ownJID.String()}))
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
	candidate, ok := adapter.normalizeMessage(context.Background(), event)
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
	candidate, ok := adapter.normalizeMessage(context.Background(), event)
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
	if _, ok := adapter.normalizeMessage(context.Background(), &events.Message{Info: info, Message: &waE2E.Message{}}); ok {
		t.Fatal("accepted non-text message")
	}
	if _, ok := adapter.normalizeMessage(context.Background(), &events.Message{Info: info, Message: &waE2E.Message{Conversation: proto.String("edit")}, IsEdit: true}); ok {
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
	if _, ok := adapter.normalizeMessage(context.Background(), event); ok {
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
	candidate, ok := adapter.normalizeMessage(context.Background(), event)
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
	candidate, ok := adapter.normalizeMessage(context.Background(), event)
	if !ok || !candidate.Owner || candidate.SenderLID.String() != lid.String() || candidate.ProviderSenderPhone != phone.String() {
		t.Fatalf("group alternate identity candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestReadDirectChatAuthorityDoesNotTrustMessageFlags(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	chatID, _ := identity.NewChatID()
	adapter.targets = staticTargets{chatAddress: "15550000077@s.whatsapp.net"}
	adapter.ready.Store(true)
	principal, err := policy.SystemPrincipal(agent.Key{TenantID: adapter.tenantID, AccountID: adapter.accountID, ChatID: chatID})
	if err != nil {
		t.Fatalf("create system principal: %v", err)
	}
	authority, err := adapter.ReadChatAuthority(context.Background(), principal)
	if err != nil || authority.ChatKind != conversation.ChatDirect || authority.ActorIsAdmin || authority.BotIsAdmin || authority.ObservedAt <= 0 {
		t.Fatalf("direct authority = %#v, %v", authority, err)
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

type staticTargets struct{ chatAddress string }

type quotedTargets struct {
	staticTargets
	quotedChat   string
	quotedID     string
	quotedSender string
}

type mentionTargets struct {
	staticTargets
	lids map[string]string
}

func (targets mentionTargets) ResolveLID(_ context.Context, _ agent.Key, ref identity.SenderRef) (identity.LID, error) {
	value, ok := targets.lids[ref.String()]
	if !ok {
		return identity.LID{}, agent.NewError(agent.ErrorNotFound, "resolve mention", errors.New("unknown senderRef"))
	}
	return identity.ParseLID(value)
}

func TestTextMessageRendersNativeMentions(t *testing.T) {
	const chat = "123456789@g.us"
	target, err := types.ParseJID(chat)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{targets: mentionTargets{staticTargets: staticTargets{chatAddress: chat}, lids: map[string]string{
		"abc123": "10000000001@lid",
		"def456": "10000000002@lid",
	}}}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{
		Text: "Halo @Alice Smith (abc123) dan @🌺 (def456), cc @all (all)",
	}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	extended := message.GetExtendedTextMessage()
	if got := extended.GetText(); got != "Halo @10000000001 dan @10000000002, cc @all" {
		t.Fatalf("rendered mention text = %q", got)
	}
	contextInfo := extended.GetContextInfo()
	want := []string{"10000000001@lid", "10000000002@lid"}
	if !slices.Equal(contextInfo.GetMentionedJID(), want) {
		t.Fatalf("native mentioned JIDs = %#v, want %#v", contextInfo.GetMentionedJID(), want)
	}
	if contextInfo.GetNonJIDMentions() != 1 {
		t.Fatalf("non-JID mention count = %d, want 1", contextInfo.GetNonJIDMentions())
	}
}

func TestTextMessageRendersBotMentionLikeLIDMention(t *testing.T) {
	adapter, ownJID := normalizationAdapter(t)
	const chat = "123456789@g.us"
	target, err := types.ParseJID(chat)
	if err != nil {
		t.Fatal(err)
	}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{
		Text: "Halo @Wazzap (bot)",
	}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	extended := message.GetExtendedTextMessage()
	if got := extended.GetText(); got != "Halo @"+ownJID.User {
		t.Fatalf("rendered bot mention text = %q", got)
	}
	if got := extended.GetContextInfo().GetMentionedJID(); !slices.Equal(got, []string{ownJID.ToNonAD().String()}) {
		t.Fatalf("bot mentioned JIDs = %#v", got)
	}
}

func TestTextMessageDeduplicatesAndFailsClosedForUnknownMention(t *testing.T) {
	const chat = "123456789@g.us"
	target, _ := types.ParseJID(chat)
	adapter := &Adapter{targets: mentionTargets{staticTargets: staticTargets{chatAddress: chat}, lids: map[string]string{
		"abc123": "10000000001@lid",
	}}}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{
		Text: "@Alice (abc123) @Alice (abc123) @Unknown (zzz999)",
	}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	contextInfo := message.GetExtendedTextMessage().GetContextInfo()
	if got := contextInfo.GetMentionedJID(); len(got) != 1 || got[0] != "10000000001@lid" {
		t.Fatalf("deduplicated mentions = %#v", got)
	}
	if got := message.GetExtendedTextMessage().GetText(); got != "@10000000001 @10000000001 @Unknown" {
		t.Fatalf("fail-closed mention text = %q", got)
	}
}

func (targets quotedTargets) ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (string, string, string, time.Time, error) {
	if targets.quotedID == "" {
		return "", "", "", time.Time{}, errors.New("missing quote")
	}
	return targets.quotedChat, targets.quotedID, targets.quotedSender, time.Now(), nil
}

func TestTextMessageQuotesSelectedWhatsAppMessage(t *testing.T) {
	const chat = "15550000002@s.whatsapp.net"
	target, err := types.ParseJID(chat)
	if err != nil {
		t.Fatal(err)
	}
	quoteID, _ := identity.NewMessageID()
	adapter := &Adapter{targets: quotedTargets{staticTargets: staticTargets{chatAddress: chat}, quotedChat: chat, quotedID: "provider-quoted-354", quotedSender: "10000000001@lid"}}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{Text: "Halo", QuotedMessageID: quoteID}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	contextInfo := message.GetExtendedTextMessage().GetContextInfo()
	if contextInfo.GetStanzaID() != "provider-quoted-354" || contextInfo.GetRemoteJID() != chat || contextInfo.GetParticipant() != "10000000001@lid" {
		t.Fatalf("wrong native reply context: %v", contextInfo)
	}
	adapter.targets = quotedTargets{staticTargets: staticTargets{chatAddress: chat}}
	if _, err := adapter.textMessage(context.Background(), action.SendTextRequest{Text: "Halo", QuotedMessageID: quoteID}, chat, target); err == nil {
		t.Fatal("missing quote silently became a plain message")
	}
}

func TestTextMessageCombinesQuoteAndMentionContext(t *testing.T) {
	const chat = "123456789@g.us"
	target, _ := types.ParseJID(chat)
	quoteID, _ := identity.NewMessageID()
	adapter := &Adapter{targets: mentionAndQuoteTargets{
		quotedTargets: quotedTargets{staticTargets: staticTargets{chatAddress: chat}, quotedChat: chat, quotedID: "quoted-provider", quotedSender: "10000000009@lid"},
		lids:          map[string]string{"abc123": "10000000001@lid"},
	}}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{
		Text: "Halo @Alice (abc123)", QuotedMessageID: quoteID,
	}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	contextInfo := message.GetExtendedTextMessage().GetContextInfo()
	if contextInfo.GetStanzaID() != "quoted-provider" || contextInfo.GetParticipant() != "10000000009@lid" {
		t.Fatalf("quote context was lost: %v", contextInfo)
	}
	if got := contextInfo.GetMentionedJID(); len(got) != 1 || got[0] != "10000000001@lid" {
		t.Fatalf("mention context was lost: %#v", got)
	}
}

type mentionAndQuoteTargets struct {
	quotedTargets
	lids map[string]string
}

func (targets mentionAndQuoteTargets) ResolveLID(_ context.Context, _ agent.Key, ref identity.SenderRef) (identity.LID, error) {
	value, ok := targets.lids[ref.String()]
	if !ok {
		return identity.LID{}, agent.NewError(agent.ErrorNotFound, "resolve mention", errors.New("unknown senderRef"))
	}
	return identity.ParseLID(value)
}

func TestTextMessageQuotesBotsOwnGroupMessage(t *testing.T) {
	adapter, ownJID := normalizationAdapter(t)
	const chat = "123456789@g.us"
	target, err := types.ParseJID(chat)
	if err != nil {
		t.Fatal(err)
	}
	quoteID, _ := identity.NewMessageID()
	adapter.targets = quotedTargets{staticTargets: staticTargets{chatAddress: chat}, quotedChat: chat, quotedID: "bot-provider-receipt"}
	message, err := adapter.textMessage(context.Background(), action.SendTextRequest{Text: "Balas pesan bot", QuotedMessageID: quoteID}, chat, target)
	if err != nil {
		t.Fatal(err)
	}
	quote := message.GetExtendedTextMessage().GetContextInfo()
	if quote.GetStanzaID() != "bot-provider-receipt" || quote.GetRemoteJID() != chat || quote.GetParticipant() != ownJID.ToNonAD().String() {
		t.Fatalf("own-message reply context = %v", quote)
	}
}

func (targets staticTargets) ResolveChatAddress(context.Context, agent.Key) (string, error) {
	return targets.chatAddress, nil
}

func (staticTargets) ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (string, string, string, time.Time, error) {
	return "", "", "", time.Time{}, io.EOF
}

func (staticTargets) ReconcileAccountPolicy(context.Context, identity.TenantID, identity.AccountID, string, []string) error {
	return nil
}

func (staticTargets) ResolveLID(context.Context, agent.Key, identity.SenderRef) (identity.LID, error) {
	return identity.ParseLID("10000000000@lid")
}

func (staticTargets) SetChatMute(context.Context, agent.Key, identity.SenderRef, uint32, time.Time) error {
	return nil
}

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

type testContactStore struct {
	pushName string
}

func (contactStore *testContactStore) PutPushName(context.Context, types.JID, string) (bool, string, error) {
	return false, "", nil
}

func (contactStore *testContactStore) PutBusinessName(context.Context, types.JID, string) (bool, string, error) {
	return false, "", nil
}

func (contactStore *testContactStore) PutContactName(context.Context, types.JID, string, string) error {
	return nil
}

func (contactStore *testContactStore) PutAllContactNames(context.Context, []store.ContactEntry) error {
	return nil
}

func (contactStore *testContactStore) PutManyRedactedPhones(context.Context, []store.RedactedPhoneEntry) error {
	return nil
}

func (contactStore *testContactStore) GetContact(context.Context, types.JID) (types.ContactInfo, error) {
	return types.ContactInfo{Found: contactStore.pushName != "", PushName: contactStore.pushName}, nil
}

func (contactStore *testContactStore) GetAllContacts(context.Context) (map[types.JID]types.ContactInfo, error) {
	return nil, nil
}
