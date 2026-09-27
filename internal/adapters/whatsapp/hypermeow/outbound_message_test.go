package hypermeow

import (
	"errors"
	"slices"
	"testing"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestRenderOutboundMentionsResolvesMarkup(t *testing.T) {
	ref, err := identity.NewSenderRef()
	if err != nil {
		t.Fatal(err)
	}
	member := types.NewJID("10000000077", types.HiddenUserServer)
	bot := types.NewJID("15550000099", types.DefaultUserServer)
	group := types.NewJID("120363000000000001", types.GroupServer)
	resolve := func(got identity.SenderRef) (types.JID, bool) { return member, got == ref }

	text := "hi @Budi (" + ref.String() + ") and @Budi (" + ref.String() + "), @me (bot) @everyone (all) @ghost (zzzzzzzz)"
	noAdmins := func() ([]types.JID, error) { return nil, nil }
	result, _ := renderOutboundMentions(text, group, bot, resolve, noAdmins)
	if result.text != "hi @10000000077 and @10000000077, @15550000099 @all @ghost" {
		t.Fatalf("rendered = %q", result.text)
	}
	if !slices.Equal(result.jids, []string{member.String(), bot.String()}) || result.nonJID != 1 || result.admins {
		t.Fatalf("mentions = %q, nonJID = %d, admins = %v", result.jids, result.nonJID, result.admins)
	}

	direct := types.NewJID("10000000001", types.HiddenUserServer)
	if result, _ := renderOutboundMentions("@x (all)", direct, types.EmptyJID, resolve, noAdmins); result.nonJID != 0 {
		t.Fatal("@all outside a group produced a non-JID mention")
	}
	if result, _ := renderOutboundMentions("@me (bot)", direct, types.EmptyJID, resolve, noAdmins); result.text != "@me" || len(result.jids) != 0 {
		t.Fatalf("unpaired bot mention = %q, %q", result.text, result.jids)
	}
}

func TestRenderOutboundMentionsTagsGroupAdmins(t *testing.T) {
	group := types.NewJID("120363000000000001", types.GroupServer)
	first := types.NewJID("10000000011", types.HiddenUserServer)
	second := types.NewJID("10000000012", types.HiddenUserServer)
	resolve := func(identity.SenderRef) (types.JID, bool) { return types.EmptyJID, false }
	lookups := 0
	admins := func() ([]types.JID, error) { lookups++; return []types.JID{first, second}, nil }

	result, err := renderOutboundMentions("please check @admin (admin)", group, types.EmptyJID, resolve, admins)
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "please check @120363000000000001@g.us" || !result.admins {
		t.Fatalf("rendered = %q, admins = %v", result.text, result.admins)
	}
	if !slices.Equal(result.jids, []string{first.String(), second.String()}) {
		t.Fatalf("mentions = %q", result.jids)
	}

	direct := types.NewJID("10000000001", types.HiddenUserServer)
	result, _ = renderOutboundMentions("@admin (admin)", direct, types.EmptyJID, resolve, admins)
	if result.text != "@admin" || result.admins || len(result.jids) != 0 || lookups != 1 {
		t.Fatalf("direct chat = %+v, lookups = %d", result, lookups)
	}
	if result, _ := renderOutboundMentions("hello", group, types.EmptyJID, resolve, admins); lookups != 1 || result.admins {
		t.Fatal("admins were looked up without an @admin mention")
	}
	notReady := func() ([]types.JID, error) { return nil, errors.New("group metadata is synchronizing") }
	if _, err := renderOutboundMentions("@admin (admin)", group, types.EmptyJID, resolve, notReady); err == nil {
		t.Fatal("an unresolved @admin mention was rendered instead of failing the send")
	}
}

func TestRenderGroupMentionsShowsTheSubject(t *testing.T) {
	contextInfo := &waE2E.ContextInfo{GroupMentions: []*waE2E.GroupMention{{
		GroupJID: proto.String("120363000000000001@g.us"), GroupSubject: proto.String("admin"),
	}}}
	if got := renderGroupMentions("tolong @120363000000000001@g.us cek", contextInfo); got != "tolong @admin cek" {
		t.Fatalf("rendered = %q", got)
	}
	if got := renderGroupMentions("plain", nil); got != "plain" {
		t.Fatalf("rendered without context = %q", got)
	}
}
