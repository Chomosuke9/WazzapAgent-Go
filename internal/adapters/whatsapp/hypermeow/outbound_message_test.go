package hypermeow

import (
	"slices"
	"testing"

	"github.com/polymorfa/hypermeow/types"

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
	rendered, jids, nonJID := renderOutboundMentions(text, group, bot, resolve)
	if rendered != "hi @10000000077 and @10000000077, @15550000099 @all @ghost" {
		t.Fatalf("rendered = %q", rendered)
	}
	if !slices.Equal(jids, []string{member.String(), bot.String()}) || nonJID != 1 {
		t.Fatalf("mentions = %q, nonJID = %d", jids, nonJID)
	}

	direct := types.NewJID("10000000001", types.HiddenUserServer)
	if _, _, nonJID := renderOutboundMentions("@x (all)", direct, types.EmptyJID, resolve); nonJID != 0 {
		t.Fatal("@all outside a group produced a non-JID mention")
	}
	if rendered, jids, _ := renderOutboundMentions("@me (bot)", direct, types.EmptyJID, resolve); rendered != "@me" || len(jids) != 0 {
		t.Fatalf("unpaired bot mention = %q, %q", rendered, jids)
	}
}
