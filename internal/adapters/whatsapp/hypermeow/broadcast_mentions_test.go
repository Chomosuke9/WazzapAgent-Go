package hypermeow

import (
	"errors"
	"slices"
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

func TestRenderBroadcastMentions(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	lid := types.NewJID("111222333444555", types.HiddenUserServer)
	info := types.GroupInfo{Participants: []types.GroupParticipant{
		{JID: types.NewJID("628111", types.DefaultUserServer), LID: lid, PhoneNumber: types.NewJID("628111", types.DefaultUserServer), IsAdmin: true},
		{JID: types.NewJID("628222", types.DefaultUserServer)},
	}}

	message, err := renderBroadcastMentions("Hi:@all, ask @admin or @+628111 or @628222 or @62899999. mail a@628111.com @alliance", group, info, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := message.GetExtendedTextMessage()
	want := "Hi:@all, ask @" + group.String() + " or @111222333444555 or @628222 or @62899999. mail a@628111.com @alliance"
	if text.GetText() != want {
		t.Fatalf("text = %q, want %q", text.GetText(), want)
	}
	contextInfo := text.GetContextInfo()
	if contextInfo.GetNonJIDMentions() != 1 {
		t.Fatalf("non-JID mentions = %d, want 1", contextInfo.GetNonJIDMentions())
	}
	if got := contextInfo.GetGroupMentions(); len(got) != 1 || got[0].GetGroupSubject() != "admin" {
		t.Fatalf("group mentions = %v", got)
	}
	wantJIDs := []string{lid.String(), "628222@s.whatsapp.net", "62899999@s.whatsapp.net"}
	if !slices.Equal(contextInfo.GetMentionedJID(), wantJIDs) {
		t.Fatalf("mentioned = %v, want %v", contextInfo.GetMentionedJID(), wantJIDs)
	}
}

func TestRenderBroadcastMentionsWithoutGroupInfo(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	missing := errors.New("group metadata is synchronizing")
	message, err := renderBroadcastMentions("@628111 @all", group, types.GroupInfo{}, missing)
	if err != nil || !slices.Equal(message.GetExtendedTextMessage().GetContextInfo().GetMentionedJID(), []string{"628111@s.whatsapp.net"}) {
		t.Fatalf("number mention without group info: %v, %v", message, err)
	}
	if _, err := renderBroadcastMentions("@admin", group, types.GroupInfo{}, missing); !errors.Is(err, missing) {
		t.Fatalf("admin mention without group info error = %v", err)
	}
}
