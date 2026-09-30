package discord

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
)

func testGuild() *discordgo.Guild {
	return &discordgo.Guild{
		ID: "100", OwnerID: "1", Name: "Test Server",
		Roles: []*discordgo.Role{
			{ID: "100", Name: "@everyone", Permissions: discordgo.PermissionViewChannel | discordgo.PermissionSendMessages},
			{ID: "200", Name: "Mods", Position: 2, Permissions: discordgo.PermissionManageMessages | discordgo.PermissionKickMembers},
			{ID: "300", Name: "Admins", Position: 3, Permissions: discordgo.PermissionAdministrator},
			{ID: "400", Name: "Bot", Position: 1, Managed: true, Permissions: discordgo.PermissionManageMessages},
			{ID: "500", Name: "Fans", Position: 1},
		},
	}
}

func TestChannelPermissionsFollowDiscordsOrder(t *testing.T) {
	guild := testGuild()
	channel := &discordgo.Channel{ID: "600", GuildID: "100", PermissionOverwrites: []*discordgo.PermissionOverwrite{
		{ID: "100", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionSendMessages},
		{ID: "500", Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionSendMessages},
		{ID: "42", Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel},
	}}
	if got := channelPermissions(guild, channel, "1", nil); got != discordgo.PermissionAll {
		t.Fatalf("owner permissions = %d", got)
	}
	if got := channelPermissions(guild, channel, "2", []string{"300"}); got != discordgo.PermissionAll {
		t.Fatalf("administrator permissions = %d", got)
	}
	if got := channelPermissions(guild, channel, "3", nil); got&discordgo.PermissionSendMessages != 0 || got&discordgo.PermissionViewChannel == 0 {
		t.Fatalf("@everyone overwrite was not applied: %d", got)
	}
	if got := channelPermissions(guild, channel, "4", []string{"500"}); got&discordgo.PermissionSendMessages == 0 {
		t.Fatalf("role overwrite did not allow sending: %d", got)
	}
	if got := channelPermissions(guild, channel, "42", []string{"500"}); got&discordgo.PermissionViewChannel != 0 {
		t.Fatalf("member overwrite did not deny viewing: %d", got)
	}
}

func TestRoleFlagsAndAdminRoles(t *testing.T) {
	if admin, super := roleFlags(discordgo.PermissionManageMessages, false); !admin || super {
		t.Fatalf("moderator flags = %v/%v", admin, super)
	}
	if admin, super := roleFlags(discordgo.PermissionAdministrator, false); !admin || !super {
		t.Fatalf("administrator flags = %v/%v", admin, super)
	}
	if admin, super := roleFlags(0, true); !admin || !super {
		t.Fatalf("owner flags = %v/%v", admin, super)
	}
	if admin, super := roleFlags(discordgo.PermissionSendMessages, false); admin || super {
		t.Fatalf("member flags = %v/%v", admin, super)
	}
	// Highest first, without @everyone and bot roles.
	if got := adminRoleIDs(testGuild()); !slices.Equal(got, []string{"300", "200"}) {
		t.Fatalf("admin roles = %v", got)
	}
}

func TestButtonComponentsLayout(t *testing.T) {
	request := action.SendButtonsRequest{Text: "pick", Buttons: []action.Button{
		{ID: "/a", Label: "A"}, {ID: "/b", Label: "B"}, {ID: "/c", Label: "C"}, {ID: "/d", Label: "D"}, {ID: "/e", Label: "E"}, {ID: "/f", Label: "F"},
	}, Menus: []action.Menu{{Title: "More", Rows: []action.MenuRow{{ID: "/g", Title: "G", Description: "gee"}}}}}
	rows, err := buttonComponents(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want two button rows and one menu row", len(rows))
	}
	if first := rows[0].(discordgo.ActionsRow); len(first.Components) != 5 {
		t.Fatalf("first row holds %d buttons", len(first.Components))
	}
	menu := rows[2].(discordgo.ActionsRow).Components[0].(discordgo.SelectMenu)
	if menu.CustomID != "menu:1" || menu.Options[0].Value != "/g" || menu.Options[0].Description != "gee" {
		t.Fatalf("menu = %#v", menu)
	}

	tooLong := action.SendButtonsRequest{Text: "x", Buttons: []action.Button{{ID: "/" + strings.Repeat("a", 100), Label: "A"}}}
	if _, err := buttonComponents(tooLong); !agent.IsCode(err, agent.ErrorUnsupported) {
		t.Fatalf("overlong custom ID err = %v", err)
	}
	var menus []action.Menu
	for index := 0; index < 6; index++ {
		menus = append(menus, action.Menu{Title: "M", Rows: []action.MenuRow{{ID: "/m", Title: "M"}}})
	}
	if _, err := buttonComponents(action.SendButtonsRequest{Text: "x", Menus: menus}); !agent.IsCode(err, agent.ErrorUnsupported) {
		t.Fatalf("six menus err = %v", err)
	}
	if _, err := buttonComponents(action.SendButtonsRequest{Text: "x"}); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("no buttons err = %v", err)
	}
}

func TestTappedTextReadsWhatTheUserChose(t *testing.T) {
	var message discordgo.Message
	raw := `{"components":[{"type":1,"components":[{"type":2,"style":2,"label":"Yes please","custom_id":"quiz:1"},{"type":2,"style":2,"label":"Mute","custom_id":"/trigger mention off"}]},` +
		`{"type":1,"components":[{"type":3,"custom_id":"menu:1","options":[{"label":"Level 2","value":"/permission 2"},{"label":"Blue","value":"blue"}]}]}]}`
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		data discordgo.MessageComponentInteractionData
		want string
	}{
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.ButtonComponent, CustomID: "quiz:1"}, "Yes please"},
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.ButtonComponent, CustomID: "/trigger mention off"}, "/trigger mention off"},
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.ButtonComponent, CustomID: "quiz:9"}, ""},
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.SelectMenuComponent, CustomID: "menu:1", Values: []string{"/permission 2"}}, "/permission 2"},
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.SelectMenuComponent, CustomID: "menu:1", Values: []string{"blue"}}, "Blue"},
		{discordgo.MessageComponentInteractionData{ComponentType: discordgo.SelectMenuComponent, CustomID: "menu:1"}, ""},
	}
	for _, test := range tests {
		if got := tappedText(test.data, &message); got != test.want {
			t.Fatalf("tappedText(%#v) = %q, want %q", test.data, got, test.want)
		}
	}
	quiz := quizComponents([]string{"a", "b", "c", "d", "e", "f"})
	if len(quiz) != 2 || quiz[0].(discordgo.ActionsRow).Components[0].(discordgo.Button).CustomID != "quiz:1" {
		t.Fatalf("quiz components = %#v", quiz)
	}
}

func TestTokenFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant", "discord.token")
	if token, err := ReadToken(path); err != nil || token != "" {
		t.Fatalf("missing token = %q, %v", token, err)
	}
	if err := WriteToken(path, "  abc.def.ghi \n"); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadToken(path); err != nil || token != "abc.def.ghi" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode = %v, %v", info.Mode(), err)
		}
	}
	if err := DeleteToken(path); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken(path); err != nil {
		t.Fatalf("second delete = %v", err)
	}
	if err := WriteToken(path, " "); err == nil {
		t.Fatal("an empty token was written")
	}
}

func TestInviteURL(t *testing.T) {
	if InviteURL("") != "" {
		t.Fatal("an invite URL was built without a bot ID")
	}
	parsed, err := url.Parse(InviteURL("123"))
	if err != nil || parsed.Host != "discord.com" || parsed.Query().Get("client_id") != "123" || parsed.Query().Get("scope") != "bot" || parsed.Query().Get("permissions") == "" {
		t.Fatalf("invite URL = %v, %v", parsed, err)
	}
}

func TestBroadcastMessages(t *testing.T) {
	messages, err := broadcastMessages("text", strings.Repeat("hello world\n", 300))
	if err != nil || len(messages) < 2 {
		t.Fatalf("long text = %d messages, %v", len(messages), err)
	}
	for _, message := range messages {
		if message.AllowedMentions == nil || len(message.AllowedMentions.Parse) != 0 {
			t.Fatalf("text broadcast may ping: %#v", message.AllowedMentions)
		}
	}
	payload := `{"content":"Hi","embeds":[{"title":"News"}],"components":[{"type":1,"components":[{"type":2,"style":5,"label":"Open","url":"https://example.com"}]}]}`
	messages, err = broadcastMessages("payload", payload)
	if err != nil || len(messages) != 1 {
		t.Fatalf("payload = %v, %v", messages, err)
	}
	if message := messages[0]; message.Content != "Hi" || len(message.Embeds) != 1 || len(message.Components) != 1 {
		t.Fatalf("decoded payload = %#v", message)
	}
	for _, bad := range []string{`{"content":"x","unknown":1}`, `{"tts":true}`, `{"content":"` + strings.Repeat("x", 2001) + `"}`, `not json`} {
		if _, err := broadcastMessages("payload", bad); !agent.IsCode(err, agent.ErrorInvalidArgument) {
			t.Fatalf("payload %.40q err = %v", bad, err)
		}
	}
	if _, err := broadcastMessages("protobuf", "x"); err == nil {
		t.Fatal("unknown format was accepted")
	}
}

func TestNormalizeBroadcastPayload(t *testing.T) {
	got, err := NormalizeBroadcastPayload("```json\n{\"content\":\"Hi\"}\n```")
	if err != nil || got != "{\n  \"content\": \"Hi\"\n}" {
		t.Fatalf("normalized = %q, %v", got, err)
	}
	if _, err := NormalizeBroadcastPayload(`{"conversation":"Hi"}`); err == nil {
		t.Fatal("a payload that is not a Discord message was accepted")
	}
}

func TestStickerCatalogNames(t *testing.T) {
	catalog := stickerCatalog([]*discordgo.Sticker{
		{ID: "1", Name: "Pepe Laugh", Available: true},
		{ID: "2", Name: "pepe-laugh", Available: true},
		{ID: "3", Name: "Gone", Available: false},
		{ID: "4", Name: "🙂", Available: true},
		{ID: "5", Name: "  Hello   World!! ", Available: true},
	})
	want := map[string]string{"pepe-laugh": "1", "pepe-laugh-2": "2", "hello-world": "5"}
	if len(catalog) != len(want) {
		t.Fatalf("catalog = %v", catalog)
	}
	for name, id := range want {
		if catalog[name] != id {
			t.Fatalf("catalog = %v, want %v", catalog, want)
		}
	}
}
