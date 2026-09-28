package hypermeow

import (
	"context"
	"regexp"
	"strings"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

// broadcastMentionPattern matches the mentions a person can type in a text
// broadcast: "@all", "@admin" and "@<phone number>" (optionally "+"-prefixed).
// The "@" must start a word, so e-mail addresses are left alone.
var broadcastMentionPattern = regexp.MustCompile(`(?:^|[\s(])@(all|admin|\+?[0-9]{6,16})\b`)

// broadcastForGroup is the message sent to one group. A text broadcast has its
// mentions resolved against that group's members; payload broadcasts are sent
// exactly as written.
func (adapter *Adapter) broadcastForGroup(ctx context.Context, format string, message *waE2E.Message, group types.JID) (*waE2E.Message, error) {
	text := message.GetExtendedTextMessage().GetText()
	if format != "text" || !broadcastMentionPattern.MatchString(text) {
		return proto.Clone(message).(*waE2E.Message), nil
	}
	info, err := adapter.readGroupInfo(ctx, group)
	return renderBroadcastMentions(text, group, info, err)
}

// renderBroadcastMentions turns typed mentions into WhatsApp wire mentions.
// "@all" is WhatsApp's own everyone mention, "@admin" tags every admin, and a
// phone number tags that member by LID when the group knows one. Only
// "@admin" needs the member list; a number that can't be looked up is still
// mentioned by phone number.
func renderBroadcastMentions(text string, group types.JID, info types.GroupInfo, infoErr error) (*waE2E.Message, error) {
	contextInfo := &waE2E.ContextInfo{}
	seen := map[string]struct{}{}
	addMention := func(jid types.JID) {
		value := jid.ToNonAD().String()
		if _, exists := seen[value]; !exists && !jid.IsEmpty() {
			seen[value] = struct{}{}
			contextInfo.MentionedJID = append(contextInfo.MentionedJID, value)
		}
	}
	var rendered strings.Builder
	cursor := 0
	for _, match := range broadcastMentionPattern.FindAllStringSubmatchIndex(text, -1) {
		token := text[match[2]:match[3]]
		replacement := "@" + token
		switch token {
		case "all":
			contextInfo.NonJIDMentions = proto.Uint32(1)
		case "admin":
			if infoErr != nil {
				return nil, infoErr
			}
			// The text carries the full group JID; WhatsApp replaces it with
			// the group mention's subject.
			replacement = "@" + group.ToNonAD().String()
			if len(contextInfo.GroupMentions) == 0 {
				contextInfo.GroupMentions = []*waE2E.GroupMention{{
					GroupJID: proto.String(group.ToNonAD().String()), GroupSubject: proto.String("admin"),
				}}
			}
			for _, participant := range info.Participants {
				if participant.IsAdmin || participant.IsSuperAdmin {
					addMention(mentionAddress(participant))
				}
			}
		default:
			member := types.NewJID(strings.TrimPrefix(token, "+"), types.DefaultUserServer)
			for _, participant := range info.Participants {
				if participant.PhoneNumber.User == member.User || participant.JID.User == member.User {
					member = mentionAddress(participant)
					break
				}
			}
			addMention(member)
			replacement = "@" + member.User
		}
		rendered.WriteString(text[cursor : match[2]-1])
		rendered.WriteString(replacement)
		cursor = match[3]
	}
	rendered.WriteString(text[cursor:])
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String(rendered.String()), ContextInfo: contextInfo,
	}}, nil
}

// mentionAddress is the JID a mention of participant must carry: its LID
// when known, as in groupAdmins, else its primary JID.
func mentionAddress(participant types.GroupParticipant) types.JID {
	if !participant.LID.IsEmpty() {
		return participant.LID
	}
	return participant.JID
}
