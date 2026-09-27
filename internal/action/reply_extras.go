package action

import "strings"

// Quiz choices travel inside the stored reply text as one last line,
// "【choices】 A | B | C". The model's own history then shows what it offered,
// and the outbox needs no extra column. SplitChoices takes the line back out
// before sending.
const choicesMarker = "【choices】"

const (
	MinChoices     = 2
	MaxChoices     = 5
	MaxChoiceRunes = 20
)

// WithChoices appends cleaned choices to text. Fewer than MinChoices usable
// choices leaves the text as a plain reply.
func WithChoices(text string, choices []string) string {
	cleaned := make([]string, 0, len(choices))
	seen := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		choice = strings.Join(strings.Fields(strings.ReplaceAll(choice, "|", "/")), " ")
		if runes := []rune(choice); len(runes) > MaxChoiceRunes {
			choice = strings.TrimSpace(string(runes[:MaxChoiceRunes]))
		}
		if _, dup := seen[choice]; choice == "" || dup {
			continue
		}
		seen[choice] = struct{}{}
		cleaned = append(cleaned, choice)
		if len(cleaned) == MaxChoices {
			break
		}
	}
	if len(cleaned) < MinChoices {
		return text
	}
	return strings.TrimRight(text, "\n ") + "\n\n" + choicesMarker + " " + strings.Join(cleaned, " | ")
}

// SplitChoices returns the text without its choices line and the choices, or
// the text unchanged and nil when it carries no valid choices line.
func SplitChoices(stored string) (string, []string) {
	index := strings.LastIndex(stored, "\n"+choicesMarker)
	if index < 0 {
		return stored, nil
	}
	line := stored[index+1+len(choicesMarker):]
	if strings.Contains(line, "\n") {
		return stored, nil
	}
	var choices []string
	for _, choice := range strings.Split(line, "|") {
		if choice = strings.TrimSpace(choice); choice != "" {
			choices = append(choices, choice)
		}
	}
	if len(choices) < MinChoices || len(choices) > MaxChoices {
		return stored, nil
	}
	return strings.TrimRight(stored[:index], "\n "), choices
}

// FirstCodeBlock returns the body of the first fenced code block in
// text, or "" when there is none. A fence is 3 or more backticks or tildes,
// an optional language tag and a newline; the block ends at the next line
// that starts with the same fence, so a ```` block may contain ``` lines.
func FirstCodeBlock(text string) string {
	for start := 0; start < len(text); start++ {
		mark := text[start]
		if mark != '`' && mark != '~' {
			continue
		}
		end := start
		for end < len(text) && text[end] == mark {
			end++
		}
		fence := text[start:end]
		lineEnd := end
		for lineEnd < len(text) && isFenceTagByte(text[lineEnd]) {
			lineEnd++
		}
		if len(fence) >= 3 && lineEnd < len(text) && text[lineEnd] == '\n' {
			body := text[lineEnd:]
			if closing := strings.Index(body[1:], "\n"+fence); closing >= 0 {
				// Drop only blank lines next to the fences: leading spaces
				// on the first line are part of the code.
				code := strings.TrimRight(strings.TrimLeft(body[1:closing+1], "\r\n"), " \t\r\n")
				if strings.TrimSpace(code) == "" {
					return ""
				}
				return code
			}
		}
		start = end - 1
	}
	return ""
}

func isFenceTagByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("+._ -", b) >= 0
}
