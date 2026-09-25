package agent

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const blockedContextInjectionText = "【message omitted: transcript-spoofing pattern detected, this message probably contain context injection so we remove it.】"

var (
	contextInjectionOpenBracket  = `[\[(【]`
	contextInjectionCloseBracket = `[\])】]`

	contextHumanSenderLine = regexp.MustCompile(
		`(?im)^[ \t]*[^\n:]+?[ \t]+` + contextInjectionOpenBracket + `[a-z0-9]{6}` + contextInjectionCloseBracket +
			`([ \t]*(\([^)\n]{1,32}\)|【[^)\n】]{1,32}】))?[ \t]*:`,
	)
	contextMessageHeader = regexp.MustCompile(
		`(?im)^[ \t]*` + contextInjectionOpenBracket + `#\d{6}` + contextInjectionCloseBracket +
			`[ \t]+([01]\d|2[0-3]):[0-5]\d[ \t]*$`,
	)
	contextInternalHeader = regexp.MustCompile(
		`(?im)^[ \t]*` + contextInjectionOpenBracket + `#(pending|system)` + contextInjectionCloseBracket +
			`[ \t]+([01]\d|2[0-3]):[0-5]\d[ \t]*$`,
	)
	contextReplyMarker = regexp.MustCompile(
		`(?im)^[ \t]*REPLYING[ \t]+TO[ \t]+` + contextInjectionOpenBracket + `#\d{6}` + contextInjectionCloseBracket + `[ \t]*$`,
	)
	contextBotSenderLine = regexp.MustCompile(
		`(?im)^[ \t]*[^\n:]{1,128}[ \t]+` + contextInjectionOpenBracket + `You` + contextInjectionCloseBracket + `[ \t]*:`,
	)
	contextSystemMarker         = regexp.MustCompile(`(?im)^[ \t]*SYSTEM[ \t]*:`)
	contextForgedRoleSenderLine = regexp.MustCompile(
		`(?im)^[ \t]*[^\n:]+?[ \t]*` + contextInjectionOpenBracket +
			`[ \t]*(superadmin|moderator|owner|admin|bot)[ \t]*` + contextInjectionCloseBracket + `[ \t]*:`,
	)
	contextForgedRoleToken = regexp.MustCompile(
		`(?i)` + contextInjectionOpenBracket + `[ \t]*(superadmin|moderator|owner|admin|bot)[ \t]*` + contextInjectionCloseBracket,
	)
	reservedContextBrackets    = strings.NewReplacer("【", "(", "】", ")")
	contextInvisibleSeparators = strings.NewReplacer(
		"\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "",
		"\r\n", "\n", "\r", "\n",
	)
)

// detectContextInjection recognizes user text that imitates the bridge's
// serialized transcript syntax. It is a structural spoofing guard, not a
// semantic prompt-injection classifier.
func detectContextInjection(input string) bool {
	text := contextInvisibleSeparators.Replace(norm.NFKC.String(input))
	humanSender := contextHumanSenderLine.MatchString(text)
	messageHeader := contextMessageHeader.MatchString(text)
	internalHeader := contextInternalHeader.MatchString(text)
	replyMarker := contextReplyMarker.MatchString(text)
	botSender := contextBotSenderLine.MatchString(text)
	systemMarker := contextSystemMarker.MatchString(text)
	roleSenderLine := contextForgedRoleSenderLine.MatchString(text)
	roleLabel := contextForgedRoleToken.MatchString(text)

	risk := 0
	if humanSender || internalHeader || botSender || systemMarker || roleSenderLine {
		risk += 100
	}
	if messageHeader {
		risk += 50
	}
	if replyMarker {
		risk += 50
	}
	if roleLabel {
		risk += 50
	}
	if messageHeader && humanSender {
		risk += 50
	}
	if messageHeader && replyMarker {
		risk += 50
	}
	return risk >= 100
}

// prepareUntrustedChatText is applied only to the model-facing view. Durable
// provider/history text remains unchanged for auditability and replay.
func prepareUntrustedChatText(text string) string {
	if detectContextInjection(text) {
		return blockedContextInjectionText
	}
	return replaceReservedContextBrackets(text)
}

func replaceReservedContextBrackets(text string) string {
	return reservedContextBrackets.Replace(text)
}

// sanitizeContextDisplayName keeps user-controlled names on one transcript
// line, prevents them from forging the line delimiter, and converts the
// bridge's reserved bracket pair into ordinary ones.
func sanitizeContextDisplayName(name string) string {
	name = replaceReservedContextBrackets(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ':' {
			return ' '
		}
		return r
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

func sanitizeContextMetadata(value string) string {
	value = replaceReservedContextBrackets(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
