// Package discord connects the Agent to Discord through a bot account. It
// turns gateway events into provider-neutral candidates and carries replies,
// reactions and moderation back to Discord. No discordgo type crosses the
// package boundary.
package discord

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// invitePermissions are what the bot uses: read and send messages (also in
// threads), react, mention everyone when asked, delete messages, lock
// channels and threads, set topics, and kick members.
const invitePermissions = discordgo.PermissionViewChannel | discordgo.PermissionSendMessages |
	discordgo.PermissionSendMessagesInThreads | discordgo.PermissionReadMessageHistory | discordgo.PermissionAddReactions |
	discordgo.PermissionMentionEveryone | discordgo.PermissionManageMessages | discordgo.PermissionManageChannels |
	discordgo.PermissionManageRoles | discordgo.PermissionManageThreads | discordgo.PermissionKickMembers

// InviteURL is the link that adds the bot with ID botID to a server, or ""
// without an ID.
func InviteURL(botID string) string {
	if botID == "" {
		return ""
	}
	query := url.Values{}
	query.Set("client_id", botID)
	query.Set("scope", "bot")
	query.Set("permissions", fmt.Sprint(int64(invitePermissions)))
	return "https://discord.com/oauth2/authorize?" + query.Encode()
}

// ReadToken returns the bot token saved at path, or "" when none is saved.
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Discord token file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteToken saves token at path, readable by the owner only. The write goes
// through a temporary file so a crash never leaves a half-written token.
func WriteToken(path, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("bot token is empty")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Discord token directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".discord-token-*")
	if err != nil {
		return fmt.Errorf("create Discord token file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = temporary.Close()
		return fmt.Errorf("protect Discord token file: %w", err)
	}
	if _, err := temporary.WriteString(token + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Discord token file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Discord token file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Discord token file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("save Discord token file: %w", err)
	}
	return nil
}

// DeleteToken removes the saved token. A token that is already gone is not
// an error.
func DeleteToken(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete Discord token file: %w", err)
	}
	return nil
}
