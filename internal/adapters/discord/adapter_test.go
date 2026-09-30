package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

const testBotID = "999"

type fakeTargets struct {
	chatAddress string
	messages    map[identity.MessageID][2]string // provider message ID, sender
	users       map[identity.SenderRef]identity.UserID
}

func (targets *fakeTargets) ResolveChatAddress(context.Context, agent.Key) (string, error) {
	return targets.chatAddress, nil
}

func (targets *fakeTargets) ResolveMessageTarget(_ context.Context, _ agent.Key, id identity.MessageID) (string, string, string, time.Time, error) {
	target, ok := targets.messages[id]
	if !ok {
		return "", "", "", time.Time{}, agent.NewError(agent.ErrorNotFound, "resolve message target", errors.New("unknown"))
	}
	return targets.chatAddress, target[0], target[1], time.Now(), nil
}

func (*fakeTargets) ReconcileAccountPolicy(context.Context, identity.TenantID, identity.AccountID, string, []string) error {
	return nil
}

func (targets *fakeTargets) ResolveUserID(_ context.Context, _ agent.Key, ref identity.SenderRef) (identity.UserID, error) {
	if user, ok := targets.users[ref]; ok {
		return user, nil
	}
	return identity.UserID{}, agent.NewError(agent.ErrorNotFound, "resolve user", errors.New("unknown"))
}

func (*fakeTargets) SetChatMute(context.Context, agent.Key, identity.SenderRef, uint32, time.Time) error {
	return nil
}

type fakeSent struct {
	mu      sync.Mutex
	primary string
	aliases []string
}

func (*fakeSent) RecordSentSticker(context.Context, agent.Key, string, string) error { return nil }

func (sent *fakeSent) RecordReceiptAliases(_ context.Context, _ agent.Key, primary string, aliases []string) error {
	sent.mu.Lock()
	defer sent.mu.Unlock()
	sent.primary, sent.aliases = primary, append([]string(nil), aliases...)
	return nil
}

// fakeDiscord answers the REST calls the adapter makes and records the
// messages it was asked to send.
type fakeDiscord struct {
	mu   sync.Mutex
	sent []map[string]any
	next int
}

func (fake *fakeDiscord) RoundTrip(request *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	}
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/messages") {
		var body map[string]any
		data, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			return respond(http.StatusBadRequest, `{"message":"bad json"}`)
		}
		fake.mu.Lock()
		fake.sent = append(fake.sent, body)
		fake.next++
		id := 9000 + fake.next
		fake.mu.Unlock()
		return respond(http.StatusOK, `{"id":"`+strconv.Itoa(id)+`","channel_id":"600"}`)
	}
	return respond(http.StatusNotFound, `{"message":"Unknown"}`)
}

func newTestAdapter(t *testing.T, allowlist []string, targets *fakeTargets, sent *fakeSent) (*Adapter, *fakeDiscord) {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	adapter, err := Open(context.Background(), Config{
		TenantID: tenantID, AccountID: accountID, Token: "OTk5.x.y", OwnerAddress: "1", Allowlist: allowlist,
		QueueCapacity: 4, Workers: 1, ConnectTimeout: time.Second, SendTimeout: time.Second,
		Targets: targets, Sent: sent, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDiscord{}
	adapter.client.Client = &http.Client{Transport: fake}
	adapter.client.State.User = &discordgo.User{ID: testBotID, Username: "Vivy", Bot: true}
	adapter.botID.Store(testBotID)
	adapter.rootCtx, adapter.cancel = context.WithCancel(context.Background())
	t.Cleanup(adapter.cancel)
	adapter.ready.Store(true)

	guild := testGuild()
	guild.Channels = []*discordgo.Channel{{ID: "600", GuildID: "100", Name: "general", Type: discordgo.ChannelTypeGuildText, Topic: "Chat here"}}
	guild.Members = []*discordgo.Member{
		{GuildID: "100", User: &discordgo.User{ID: testBotID, Bot: true}, Roles: []string{"400"}},
		{GuildID: "100", User: &discordgo.User{ID: "7", Username: "rina"}, Nick: "Rina", Roles: []string{"200"}},
		{GuildID: "100", User: &discordgo.User{ID: "8", Username: "budi"}},
	}
	if err := adapter.client.State.GuildAdd(guild); err != nil {
		t.Fatal(err)
	}
	if err := adapter.client.State.ChannelAdd(&discordgo.Channel{ID: "650", Type: discordgo.ChannelTypeDM, Recipients: []*discordgo.User{{ID: "8"}}}); err != nil {
		t.Fatal(err)
	}
	return adapter, fake
}

func testKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}

func TestSendTextRepliesMentionsAndSplitsLongReplies(t *testing.T) {
	quoted, _ := identity.NewMessageID()
	ref, _ := identity.ParseSenderRef("a1b2c3")
	rina, _ := identity.ParseUserID("7")
	targets := &fakeTargets{chatAddress: "600", messages: map[identity.MessageID][2]string{quoted: {"777", "7"}}, users: map[identity.SenderRef]identity.UserID{ref: rina}}
	sent := &fakeSent{}
	adapter, fake := newTestAdapter(t, []string{"*"}, targets, sent)

	long := "hi @Rina (a1b2c3)\n" + strings.Repeat("a line of the answer\n", 150)
	result, err := adapter.SendText(context.Background(), action.SendTextRequest{Key: testKey(t), Text: long, QuotedMessageID: quoted, Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) < 2 {
		t.Fatalf("long reply went out as %d messages", len(fake.sent))
	}
	first, last := fake.sent[0], fake.sent[len(fake.sent)-1]
	if result.ProviderReceipt != "9001" || sent.primary != "9001" || len(sent.aliases) != len(fake.sent)-1 {
		t.Fatalf("receipt = %q, aliases = %q -> %v", result.ProviderReceipt, sent.primary, sent.aliases)
	}
	if !strings.HasPrefix(first["content"].(string), "hi <@7>") {
		t.Fatalf("first part = %q", first["content"])
	}
	if reference, _ := first["message_reference"].(map[string]any); reference["message_id"] != "777" {
		t.Fatalf("first part does not reply to the quoted message: %v", first["message_reference"])
	}
	allowed, _ := first["allowed_mentions"].(map[string]any)
	if users, _ := allowed["users"].([]any); len(users) != 1 || users[0] != "7" || allowed["replied_user"] != true {
		t.Fatalf("allowed mentions = %v", allowed)
	}
	if _, has := fake.sent[1]["message_reference"]; has {
		t.Fatal("a later part replies again")
	}
	components, _ := last["components"].([]any)
	if len(components) != 1 {
		t.Fatalf("quiz buttons missing from the last part: %v", last["components"])
	}
	for _, part := range fake.sent {
		if utf16Units(part["content"].(string)) > maxMessageUnits {
			t.Fatal("a part is longer than Discord allows")
		}
	}
}

func TestSendTextRejectsAQuoteFromAnotherChannel(t *testing.T) {
	quoted, _ := identity.NewMessageID()
	targets := &fakeTargets{chatAddress: "600", messages: map[identity.MessageID][2]string{quoted: {"tap:123", "7"}}}
	adapter, fake := newTestAdapter(t, []string{"*"}, targets, &fakeSent{})
	_, err := adapter.SendText(context.Background(), action.SendTextRequest{Key: testKey(t), Text: "hi", QuotedMessageID: quoted})
	if !agent.IsCode(err, agent.ErrorIntegrityFailure) || len(fake.sent) != 0 {
		t.Fatalf("err = %v, sent = %d", err, len(fake.sent))
	}
}

func TestCandidateFromServerMessage(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"100"}, &fakeTargets{chatAddress: "600"}, &fakeSent{})
	message := &discordgo.Message{
		ID: "5001", ChannelID: "600", GuildID: "100", Type: discordgo.MessageTypeReply,
		Content: "<@999> tolong cek <@8>", Timestamp: time.Now(),
		Author: &discordgo.User{ID: "7", Username: "rina"}, Member: &discordgo.Member{Nick: "Rina", Roles: []string{"200"}},
		Mentions:          []*discordgo.User{{ID: testBotID, Bot: true}, {ID: "8", Username: "budi"}},
		MessageReference:  &discordgo.MessageReference{MessageID: "777", ChannelID: "600"},
		ReferencedMessage: &discordgo.Message{ID: "777", Author: &discordgo.User{ID: testBotID}},
	}
	candidate, ok := adapter.candidateFromMessage(context.Background(), message)
	if !ok {
		t.Fatal("server message was ignored")
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("candidate is invalid: %v", err)
	}
	if candidate.Text != "@999 tolong cek @8" || candidate.ChatKind != conversation.ChatGroup || candidate.ProviderChatAddress != "600" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if !candidate.MentionsBot || len(candidate.Mentions) != 2 || candidate.Mentions[1].TargetUserID.String() != "8" || candidate.Mentions[1].DisplayName != "budi" {
		t.Fatalf("mentions = %#v", candidate.Mentions)
	}
	if !candidate.SenderIsAdmin || candidate.SenderIsSuperAdmin || candidate.SenderName != "Rina" || candidate.Owner {
		t.Fatalf("sender = %q admin=%v super=%v owner=%v", candidate.SenderName, candidate.SenderIsAdmin, candidate.SenderIsSuperAdmin, candidate.Owner)
	}
	if !candidate.Allowlisted || candidate.ProviderGuildAddress != "100" || candidate.ProviderQuotedMessageID != "777" || candidate.ProviderQuotedMessageJSON != nil {
		t.Fatalf("scope/quote = %v %q %q", candidate.Allowlisted, candidate.ProviderGuildAddress, candidate.ProviderQuotedMessageID)
	}

	message.Content = "/catch"
	message.Mentions = nil
	caught, ok := adapter.candidateFromMessage(context.Background(), message)
	if !ok || len(caught.ProviderQuotedMessageJSON) == 0 || caught.ProviderQuotedFromMe == nil || !*caught.ProviderQuotedFromMe {
		t.Fatalf("/catch did not capture the quoted message: %#v", caught)
	}
	if err := caught.Validate(); err != nil {
		t.Fatalf("/catch candidate is invalid: %v", err)
	}
}

func TestCandidateFromMessageIgnoresBotsAndSystemMessages(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"*"}, &fakeTargets{chatAddress: "600"}, &fakeSent{})
	for _, message := range []*discordgo.Message{
		{ID: "1", ChannelID: "600", Content: "mine", Timestamp: time.Now(), Author: &discordgo.User{ID: testBotID, Bot: true}},
		{ID: "2", ChannelID: "600", Content: "other bot", Timestamp: time.Now(), Author: &discordgo.User{ID: "55", Bot: true}},
		{ID: "3", ChannelID: "600", Content: "hook", Timestamp: time.Now(), Author: &discordgo.User{ID: "56"}, WebhookID: "1"},
		{ID: "4", ChannelID: "600", Content: "", Timestamp: time.Now(), Author: &discordgo.User{ID: "57"}, Type: discordgo.MessageTypeGuildMemberJoin},
		{ID: "5", ChannelID: "600", Content: "", Timestamp: time.Now(), Author: &discordgo.User{ID: "58"}},
	} {
		if _, ok := adapter.candidateFromMessage(context.Background(), message); ok {
			t.Fatalf("message %s was accepted", message.ID)
		}
	}
}

func TestCandidateFromDirectMessageIsAllowlistedByUser(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"8"}, &fakeTargets{chatAddress: "650"}, &fakeSent{})
	message := &discordgo.Message{ID: "6001", ChannelID: "650", Content: "halo", Timestamp: time.Now(), Author: &discordgo.User{ID: "8", Username: "budi", GlobalName: "Budi"}}
	candidate, ok := adapter.candidateFromMessage(context.Background(), message)
	if !ok || candidate.ChatKind != conversation.ChatDirect || !candidate.Allowlisted || candidate.ProviderAliasAddress != "8" || candidate.SenderName != "Budi" || candidate.SenderIsAdmin {
		t.Fatalf("direct candidate = %#v, %v", candidate, ok)
	}
	other, _ := newTestAdapter(t, []string{"100"}, &fakeTargets{chatAddress: "650"}, &fakeSent{})
	if candidate, _ := other.candidateFromMessage(context.Background(), message); candidate.Allowlisted {
		t.Fatal("a server allowlist admitted a direct chat")
	}
}

func TestCandidateFromQuizTap(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"*"}, &fakeTargets{chatAddress: "600"}, &fakeSent{})
	var botMessage discordgo.Message
	if err := json.Unmarshal([]byte(`{"id":"9001","channel_id":"600","components":[{"type":1,"components":[{"type":2,"style":2,"label":"Yes","custom_id":"quiz:1"}]}]}`), &botMessage); err != nil {
		t.Fatal(err)
	}
	interaction := &discordgo.Interaction{
		ID: "1240000000000000000", Type: discordgo.InteractionMessageComponent, ChannelID: "600", GuildID: "100",
		Member:  &discordgo.Member{User: &discordgo.User{ID: "8", Username: "budi"}, Permissions: discordgo.PermissionSendMessages},
		Message: &botMessage,
		Data:    discordgo.MessageComponentInteractionData{CustomID: "quiz:1", ComponentType: discordgo.ButtonComponent},
	}
	candidate, ok := adapter.candidateFromInteraction(context.Background(), interaction)
	if !ok {
		t.Fatal("tap was ignored")
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("tap candidate is invalid: %v", err)
	}
	if candidate.Text != "Yes" || candidate.ProviderMessageID != "tap:1240000000000000000" || candidate.ProviderQuotedMessageID != "9001" || candidate.SenderIsAdmin {
		t.Fatalf("tap candidate = %#v", candidate)
	}
}

func TestChatContextAndAuthorityComeFromServerPermissions(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"*"}, &fakeTargets{chatAddress: "600"}, &fakeSent{})
	key := testKey(t)
	chat, err := adapter.ReadChatContext(context.Background(), key)
	if err != nil || chat.Kind != "group" || chat.Name != "#general · Test Server" || chat.Description != "Chat here" || !chat.BotIsAdmin {
		t.Fatalf("chat context = %#v, %v", chat, err)
	}
	userID, _ := identity.ParseUserID("7")
	participantID, _ := identity.NewParticipantID()
	principal := policy.Principal{Kind: policy.PrincipalHuman, TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, ParticipantID: participantID, UserID: userID}
	authority, err := adapter.ReadChatAuthority(context.Background(), principal)
	if err != nil || authority.ChatKind != conversation.ChatGroup || !authority.ActorIsAdmin || !authority.BotIsAdmin {
		t.Fatalf("authority = %#v, %v", authority, err)
	}
	principal.UserID, _ = identity.ParseUserID("8")
	if authority, err := adapter.ReadChatAuthority(context.Background(), principal); err != nil || authority.ActorIsAdmin {
		t.Fatalf("member authority = %#v, %v", authority, err)
	}

	direct, _ := newTestAdapter(t, []string{"*"}, &fakeTargets{chatAddress: "650"}, &fakeSent{})
	if chat, err := direct.ReadChatContext(context.Background(), key); err != nil || chat.Kind != "private" {
		t.Fatalf("direct chat context = %#v, %v", chat, err)
	}
}

type recordingNames struct {
	mu    sync.Mutex
	saved map[string]string
	done  chan struct{}
}

func (names *recordingNames) SaveGroupName(_ context.Context, _ identity.TenantID, _ identity.AccountID, address, name string) error {
	names.mu.Lock()
	defer names.mu.Unlock()
	names.saved[address] = name
	select {
	case names.done <- struct{}{}:
	default:
	}
	return nil
}

type handledCandidates struct {
	handled chan conversation.IncomingCandidate
}

func (handler handledCandidates) Handle(_ context.Context, candidate conversation.IncomingCandidate) error {
	handler.handled <- candidate
	return nil
}

func TestWorkerStoresTheChannelNameOnceTheChatIsStored(t *testing.T) {
	adapter, _ := newTestAdapter(t, []string{"*"}, &fakeTargets{chatAddress: "600"}, &fakeSent{})
	names := &recordingNames{saved: map[string]string{}, done: make(chan struct{}, 1)}
	adapter.channelNames = names
	handler := handledCandidates{handled: make(chan conversation.IncomingCandidate, 1)}
	adapter.handler = handler
	adapter.wait.Add(1)
	go adapter.worker()
	t.Cleanup(func() { adapter.cancel(); adapter.wait.Wait() })

	adapter.queue <- conversation.IncomingCandidate{ProviderChatAddress: "600", ChatKind: conversation.ChatGroup}
	select {
	case <-handler.handled:
	case <-time.After(2 * time.Second):
		t.Fatal("the candidate was not handled")
	}
	select {
	case <-names.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the channel name was not stored")
	}
	names.mu.Lock()
	defer names.mu.Unlock()
	if names.saved["600"] != "#general · Test Server" {
		t.Fatalf("saved names = %v", names.saved)
	}
}
