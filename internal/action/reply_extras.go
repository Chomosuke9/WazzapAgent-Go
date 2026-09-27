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

// FirstCodeBlock returns the body of the first fenced code block in text, or
// "" when there is none. Fences follow Markdown: a line of 3 or more backticks
// or tildes (indentation allowed) opens a block, with any info string after
// it ("c#", "js title"); the block ends at a line that is only the same
// character repeated at least as many times. So a ```` block may contain ```
// lines and a ``` block may contain a ```js line.
func FirstCodeBlock(text string) string {
	lines := strings.Split(text, "\n")
	for open := 0; open < len(lines); open++ {
		fence, info := fenceRun(lines[open])
		if len(fence) < 3 || fence[0] == '`' && strings.Contains(info, "`") {
			continue
		}
		for end := open + 1; end < len(lines); end++ {
			closing, rest := fenceRun(lines[end])
			if len(closing) < len(fence) || closing[0] != fence[0] || strings.TrimSpace(rest) != "" {
				continue
			}
			// Drop only blank lines next to the fences: leading spaces on
			// the first line are part of the code.
			// Body lines lose the opening fence's indentation, as in Markdown.
			indent := len(lines[open]) - len(strings.TrimLeft(lines[open], " "))
			body := make([]string, 0, end-open-1)
			for _, line := range lines[open+1 : end] {
				body = append(body, line[min(indent, len(line)-len(strings.TrimLeft(line, " "))):])
			}
			code := strings.TrimRight(strings.TrimLeft(strings.Join(body, "\n"), "\r\n"), " \t\r\n")
			if strings.TrimSpace(code) == "" {
				return ""
			}
			return code
		}
		// An unclosed fence runs to the end of the text; there is no block.
		return ""
	}
	return ""
}

// fenceRun splits an indented line into its leading run of backticks or
// tildes and the rest of the line.
func fenceRun(line string) (string, string) {
	line = strings.TrimLeft(line, " \t")
	if line == "" || line[0] != '`' && line[0] != '~' {
		return "", line
	}
	run := len(line) - len(strings.TrimLeft(line, line[:1]))
	return line[:run], line[run:]
}
