package discord

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// maxMessageUnits is Discord's cap on a message's content, in the UTF-16
// code units Discord counts.
const maxMessageUnits = 2000

var inboundMarkupPattern = regexp.MustCompile(`<(@!?|@&|#)([0-9]{1,20})>|<a?:([0-9A-Za-z_]{1,32}):[0-9]{1,20}>`)

// markupResolver names the things Discord message markup points at.
type markupResolver struct {
	// role returns a role's name, and whether it is the bot's own managed
	// role, which Discord inserts when someone picks the bot by name.
	role    func(id string) (name string, botRole bool)
	channel func(id string) string
}

// renderInboundText rewrites Discord markup into readable text. A user
// mention becomes "@<user ID>", the raw token the core binds to that user; a
// mention of the bot's managed role becomes the bot's own token. Role,
// channel and custom emoji markup become their names.
func renderInboundText(content, botID string, resolver markupResolver) string {
	return inboundMarkupPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := inboundMarkupPattern.FindStringSubmatch(match)
		switch {
		case parts[3] != "":
			return ":" + parts[3] + ":"
		case parts[1] == "@" || parts[1] == "@!":
			return "@" + parts[2]
		case parts[1] == "@&":
			if resolver.role != nil {
				if name, botRole := resolver.role(parts[2]); botRole && botID != "" {
					return "@" + botID
				} else if name != "" {
					return "@" + name
				}
			}
			return "@role"
		default:
			if resolver.channel != nil {
				if name := resolver.channel(parts[2]); name != "" {
					return "#" + name
				}
			}
			return "#channel"
		}
	})
}

// messageText is what the pipeline sees for a message: its text, or for a
// message with an attachment or sticker a media placeholder followed by the
// text. A caption that starts with "/" stays bare so it routes as a command.
func messageText(message *discordgo.Message, rendered string) string {
	rendered = strings.TrimSpace(rendered)
	placeholder := mediaPlaceholder(message)
	switch {
	case placeholder == "":
		return rendered
	case rendered == "":
		return placeholder
	case strings.HasPrefix(rendered, "/"):
		return rendered
	default:
		return placeholder + " " + rendered
	}
}

func mediaPlaceholder(message *discordgo.Message) string {
	if len(message.Attachments) > 0 && message.Attachments[0] != nil {
		attachment := message.Attachments[0]
		contentType := strings.ToLower(attachment.ContentType)
		switch {
		case message.Flags&discordgo.MessageFlagsIsVoiceMessage != 0:
			return conversation.PlaceholderVoiceNote
		case contentType == "image/gif" || strings.HasSuffix(strings.ToLower(attachment.Filename), ".gif"):
			return conversation.PlaceholderGIF
		case strings.HasPrefix(contentType, "image/"):
			return conversation.PlaceholderImage
		case strings.HasPrefix(contentType, "video/"):
			return conversation.PlaceholderVideo
		case strings.HasPrefix(contentType, "audio/"):
			return conversation.PlaceholderAudio
		default:
			return conversation.PlaceholderDocument
		}
	}
	if len(message.StickerItems) > 0 {
		return conversation.PlaceholderSticker
	}
	return ""
}

var outboundMentionPattern = regexp.MustCompile(`@([^@()\r\n]+?)\s*\(([0-9A-Za-z]{3,16})\)`)

// renderedMentions is a reply's content with the model's mentions turned
// into Discord markup, and the mentions Discord may ping.
type renderedMentions struct {
	text     string
	users    []string
	roles    []string
	everyone bool
}

// renderOutboundMentions rewrites the model's "@Name (ref)" markup. "all"
// becomes @everyone in a server channel, "admin" mentions the server's
// moderator roles, "bot" the bot itself, and any other ref the member it
// names; an unresolved ref degrades to plain "@Name" text.
func renderOutboundMentions(raw string, inGuild bool, botID string, resolve func(identity.SenderRef) (string, bool), adminRoles func() []string) renderedMentions {
	matches := outboundMentionPattern.FindAllStringSubmatchIndex(raw, -1)
	if len(matches) == 0 {
		return renderedMentions{text: raw}
	}
	var builder strings.Builder
	result := renderedMentions{}
	seenUsers := make(map[string]struct{}, len(matches))
	seenRoles := make(map[string]struct{}, 4)
	addUser := func(id string) string {
		if _, exists := seenUsers[id]; !exists {
			seenUsers[id] = struct{}{}
			result.users = append(result.users, id)
		}
		return "<@" + id + ">"
	}
	cursor := 0
	for _, match := range matches {
		builder.WriteString(raw[cursor:match[0]])
		name := strings.TrimSpace(raw[match[2]:match[3]])
		value := strings.ToLower(strings.TrimSpace(raw[match[4]:match[5]]))
		replacement := "@" + name
		switch value {
		case "all":
			replacement = "@all"
			if inGuild {
				replacement, result.everyone = "@everyone", true
			}
		case "admin":
			replacement = "@admin"
			if inGuild && adminRoles != nil {
				var mentions []string
				for _, role := range adminRoles() {
					mentions = append(mentions, "<@&"+role+">")
					if _, exists := seenRoles[role]; !exists {
						seenRoles[role] = struct{}{}
						result.roles = append(result.roles, role)
					}
				}
				if len(mentions) > 0 {
					replacement = strings.Join(mentions, " ")
				}
			}
		case "bot":
			if botID != "" {
				replacement = addUser(botID)
			}
		default:
			if ref, err := identity.ParseSenderRef(value); err == nil && resolve != nil {
				if id, ok := resolve(ref); ok {
					replacement = addUser(id)
				}
			}
		}
		builder.WriteString(replacement)
		cursor = match[1]
	}
	builder.WriteString(raw[cursor:])
	result.text = builder.String()
	return result
}

// utf16Units is the length Discord measures a message by.
func utf16Units(text string) int {
	units := 0
	for _, r := range text {
		if width := utf16.RuneLen(r); width > 0 {
			units += width
		} else {
			units++
		}
	}
	return units
}

// splitMessage cuts text into parts Discord accepts, at line breaks where it
// can. A code block cut in two is closed at the end of one part and reopened
// with the same fence at the start of the next, so both still render.
func splitMessage(text string, limit int) []string {
	if utf16Units(text) <= limit {
		return []string{text}
	}
	var parts []string
	var current strings.Builder
	currentUnits := 0
	fence := "" // the opening line of the code block current is inside, if any
	flush := func() {
		if currentUnits == 0 {
			return
		}
		part := strings.TrimRight(current.String(), "\n")
		if fence != "" {
			part += "\n```"
		}
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
		current.Reset()
		currentUnits = 0
		if fence != "" {
			current.WriteString(fence + "\n")
			currentUnits = utf16Units(fence) + 1
		}
	}
	// reserve leaves room for the fence that closes a split code block.
	const reserve = 4
	for _, line := range strings.SplitAfter(text, "\n") {
		lineUnits := utf16Units(line)
		if currentUnits+lineUnits+reserve > limit {
			flush()
		}
		for currentUnits+lineUnits+reserve > limit {
			// One line longer than a whole part: cut it at a space if one is
			// close to the end, or else mid-word.
			room := limit - reserve - currentUnits
			head, tail := cutUnits(line, room)
			current.WriteString(head)
			currentUnits += utf16Units(head)
			flush()
			line, lineUnits = tail, utf16Units(tail)
		}
		current.WriteString(line)
		currentUnits += lineUnits
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") {
			if fence == "" {
				fence = trimmed
			} else if trimmed == "```" {
				fence = ""
			}
		}
	}
	flush()
	return parts
}

// cutUnits splits line so head fits in room UTF-16 units, preferring a space
// in the last quarter of head.
func cutUnits(line string, room int) (string, string) {
	if room < 1 {
		room = 1
	}
	units, end := 0, 0
	for index, r := range line {
		width := utf16.RuneLen(r)
		if width < 1 {
			width = 1
		}
		if units+width > room {
			break
		}
		units += width
		end = index + utf8.RuneLen(r)
	}
	if end == 0 {
		_, size := utf8.DecodeRuneInString(line)
		end = size
	}
	if space := strings.LastIndexByte(line[:end], ' '); space > end*3/4 {
		end = space + 1
	}
	return line[:end], line[end:]
}
