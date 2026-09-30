package discord

import (
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

func TestRenderInboundTextTurnsMarkupIntoTokensAndNames(t *testing.T) {
	resolver := markupResolver{
		role: func(id string) (string, bool) {
			switch id {
			case "700":
				return "Moderators", false
			case "701":
				return "Vivy", true
			}
			return "", false
		},
		channel: func(id string) string {
			if id == "800" {
				return "general"
			}
			return ""
		},
	}
	got := renderInboundText("hi <@123> and <@!456>, ask <@&700> or <@&701> in <#800> <#801> <:wave:900> <a:dance:901> <@&702>", "999", resolver)
	want := "hi @123 and @456, ask @Moderators or @999 in #general #channel :wave: :dance: @role"
	if got != want {
		t.Fatalf("rendered = %q\nwant       %q", got, want)
	}
}

func TestMessageTextAddsMediaPlaceholders(t *testing.T) {
	tests := []struct {
		name    string
		message discordgo.Message
		text    string
		want    string
	}{
		{name: "plain", text: "hello", want: "hello"},
		{name: "image caption", message: discordgo.Message{Attachments: []*discordgo.MessageAttachment{{ContentType: "image/png"}}}, text: "look", want: conversation.PlaceholderImage + " look"},
		{name: "gif by name", message: discordgo.Message{Attachments: []*discordgo.MessageAttachment{{Filename: "fun.GIF"}}}, want: conversation.PlaceholderGIF},
		{name: "voice note", message: discordgo.Message{Flags: discordgo.MessageFlagsIsVoiceMessage, Attachments: []*discordgo.MessageAttachment{{ContentType: "audio/ogg"}}}, want: conversation.PlaceholderVoiceNote},
		{name: "document", message: discordgo.Message{Attachments: []*discordgo.MessageAttachment{{ContentType: "application/pdf"}}}, text: "notes", want: conversation.PlaceholderDocument + " notes"},
		{name: "sticker", message: discordgo.Message{StickerItems: []*discordgo.StickerItem{{ID: "1", Name: "wave"}}}, want: conversation.PlaceholderSticker},
		{name: "command caption stays bare", message: discordgo.Message{Attachments: []*discordgo.MessageAttachment{{ContentType: "image/png"}}}, text: "/catch", want: "/catch"},
		{name: "empty", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := messageText(&test.message, test.text); got != test.want {
				t.Fatalf("messageText = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderOutboundMentions(t *testing.T) {
	ref, _ := identity.ParseSenderRef("a1b2c3")
	resolve := func(value identity.SenderRef) (string, bool) {
		if value == ref {
			return "123456789012345678", true
		}
		return "", false
	}
	adminRoles := func() []string { return []string{"700", "701"} }

	rendered := renderOutboundMentions("hi @Budi (a1b2c3), @Budi (A1B2C3) @Rina (zzzzzz) @everyone (all) @admin (admin) @Vivy (Bot)", true, "999", resolve, adminRoles)
	want := "hi <@123456789012345678>, <@123456789012345678> @Rina @everyone <@&700> <@&701> <@999>"
	if rendered.text != want {
		t.Fatalf("text = %q\nwant   %q", rendered.text, want)
	}
	if !slices.Equal(rendered.users, []string{"123456789012345678", "999"}) || !slices.Equal(rendered.roles, []string{"700", "701"}) || !rendered.everyone {
		t.Fatalf("pings = users %v roles %v everyone %v", rendered.users, rendered.roles, rendered.everyone)
	}

	direct := renderOutboundMentions("@x (all) and @y (admin)", false, "999", resolve, adminRoles)
	if direct.text != "@all and @admin" || direct.everyone || len(direct.roles) != 0 {
		t.Fatalf("direct chat mentions = %#v", direct)
	}
	plain := renderOutboundMentions("no mentions here, email me@example.com", true, "999", resolve, adminRoles)
	if plain.text != "no mentions here, email me@example.com" || plain.users != nil {
		t.Fatalf("plain text changed: %#v", plain)
	}
}

func TestAllowedMentionsNeverParseTextOnItsOwn(t *testing.T) {
	allowed := renderedMentions{users: []string{"1"}}.allowed(true)
	if allowed.Parse == nil || len(allowed.Parse) != 0 || !allowed.RepliedUser || !slices.Equal(allowed.Users, []string{"1"}) {
		t.Fatalf("allowed = %#v", allowed)
	}
	everyone := renderedMentions{everyone: true}.allowed(false)
	if !slices.Equal(everyone.Parse, []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeEveryone}) || everyone.RepliedUser {
		t.Fatalf("everyone allowed = %#v", everyone)
	}
}

func TestUTF16UnitsCountsLikeDiscord(t *testing.T) {
	if got := utf16Units("aé😀"); got != 4 {
		t.Fatalf("utf16Units = %d, want 4", got)
	}
}

func TestSplitMessageKeepsShortTextWhole(t *testing.T) {
	if parts := splitMessage("hello", 2000); !slices.Equal(parts, []string{"hello"}) {
		t.Fatalf("parts = %q", parts)
	}
}

func TestSplitMessageCutsAtLinesAndKeepsEveryPartWithinTheLimit(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < 300; index++ {
		builder.WriteString("line number ")
		builder.WriteString(strings.Repeat("x", index%40))
		builder.WriteString("\n")
	}
	text := strings.TrimRight(builder.String(), "\n")
	parts := splitMessage(text, 500)
	if len(parts) < 2 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	for index, part := range parts {
		if units := utf16Units(part); units > 500 {
			t.Fatalf("part %d has %d units", index, units)
		}
	}
	if strings.Join(parts, "\n") != text {
		t.Fatal("joined parts do not rebuild the text")
	}
}

func TestSplitMessageCutsAnOverlongLine(t *testing.T) {
	text := strings.Repeat("word ", 300)
	parts := splitMessage(text, 200)
	for index, part := range parts {
		if units := utf16Units(part); units > 200 {
			t.Fatalf("part %d has %d units", index, units)
		}
	}
	if strings.ReplaceAll(strings.Join(parts, ""), " ", "") != strings.ReplaceAll(text, " ", "") {
		t.Fatal("cutting the line lost text")
	}
}

func TestSplitMessageReopensACodeBlockItCuts(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"hello world\")\n", 30) + "```\nafter"
	parts := splitMessage(code, 300)
	if len(parts) < 2 {
		t.Fatalf("expected the block to be cut, got %d parts", len(parts))
	}
	for index, part := range parts {
		if strings.Count(part, "```")%2 != 0 {
			t.Fatalf("part %d leaves a code fence open:\n%s", index, part)
		}
		if utf16Units(part) > 300 {
			t.Fatalf("part %d is too long", index)
		}
		if index > 0 && index < len(parts)-1 && !strings.HasPrefix(part, "```go\n") {
			t.Fatalf("part %d does not reopen the block:\n%s", index, part)
		}
	}
	if !strings.HasSuffix(parts[len(parts)-1], "after") {
		t.Fatalf("text after the block was lost: %q", parts[len(parts)-1])
	}
}
