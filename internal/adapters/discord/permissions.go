package discord

import (
	"github.com/bwmarrin/discordgo"
)

// moderatorPermissions are the permissions that make a member a group admin
// to the core: any one of them means the server trusts them to moderate.
const moderatorPermissions = discordgo.PermissionAdministrator | discordgo.PermissionManageGuild |
	discordgo.PermissionManageChannels | discordgo.PermissionManageMessages |
	discordgo.PermissionKickMembers | discordgo.PermissionBanMembers | discordgo.PermissionModerateMembers

// channelPermissions computes a member's permissions in a channel the way
// Discord does: base role permissions, then the channel's @everyone, role and
// member overwrites. A thread takes its parent channel's overwrites, which
// the caller passes as channel.
func channelPermissions(guild *discordgo.Guild, channel *discordgo.Channel, userID string, roles []string) int64 {
	if guild == nil {
		return 0
	}
	if userID != "" && userID == guild.OwnerID {
		return discordgo.PermissionAll
	}
	var permissions int64
	for _, role := range guild.Roles {
		if role != nil && role.ID == guild.ID {
			permissions |= role.Permissions
			break
		}
	}
	memberRoles := make(map[string]struct{}, len(roles))
	for _, id := range roles {
		memberRoles[id] = struct{}{}
	}
	for _, role := range guild.Roles {
		if role == nil {
			continue
		}
		if _, has := memberRoles[role.ID]; has {
			permissions |= role.Permissions
		}
	}
	if permissions&discordgo.PermissionAdministrator != 0 {
		return discordgo.PermissionAll
	}
	if channel == nil {
		return permissions
	}
	for _, overwrite := range channel.PermissionOverwrites {
		if overwrite != nil && overwrite.Type == discordgo.PermissionOverwriteTypeRole && overwrite.ID == guild.ID {
			permissions &^= overwrite.Deny
			permissions |= overwrite.Allow
			break
		}
	}
	var deny, allow int64
	for _, overwrite := range channel.PermissionOverwrites {
		if overwrite == nil || overwrite.Type != discordgo.PermissionOverwriteTypeRole || overwrite.ID == guild.ID {
			continue
		}
		if _, has := memberRoles[overwrite.ID]; has {
			deny |= overwrite.Deny
			allow |= overwrite.Allow
		}
	}
	permissions &^= deny
	permissions |= allow
	for _, overwrite := range channel.PermissionOverwrites {
		if overwrite != nil && overwrite.Type == discordgo.PermissionOverwriteTypeMember && overwrite.ID == userID {
			permissions &^= overwrite.Deny
			permissions |= overwrite.Allow
			break
		}
	}
	return permissions
}

// roleFlags maps Discord permissions onto the core's two group roles: a
// superadmin owns the server or is an administrator, and an admin holds any
// moderator permission.
func roleFlags(permissions int64, owner bool) (isAdmin, isSuperAdmin bool) {
	isSuperAdmin = owner || permissions&discordgo.PermissionAdministrator != 0
	isAdmin = isSuperAdmin || permissions&moderatorPermissions != 0
	return isAdmin, isSuperAdmin
}

// adminRoleIDs are the server's roles whose members moderate it, highest
// first, leaving out @everyone and roles that belong to bots.
func adminRoleIDs(guild *discordgo.Guild) []string {
	if guild == nil {
		return nil
	}
	type ranked struct {
		id       string
		position int
	}
	var found []ranked
	for _, role := range guild.Roles {
		if role == nil || role.ID == guild.ID || role.Managed {
			continue
		}
		if role.Permissions&moderatorPermissions != 0 {
			found = append(found, ranked{role.ID, role.Position})
		}
	}
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].position > found[j-1].position; j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
	ids := make([]string, 0, len(found))
	for _, role := range found {
		ids = append(ids, role.id)
	}
	return ids
}
