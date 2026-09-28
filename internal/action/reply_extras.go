package action

import "strings"

// A quiz travels inside the stored reply text as its last two lines,
// "【quiz】 Title | Subtitle | Footer" then "【choices】 A | B | C". The model's
// own history then shows what it offered, and the outbox needs no extra
// column. SplitQuiz takes the lines back out before sending. Rows stored
// before quizzes had a header carry only the choices line.
const (
	quizMarker    = "【quiz】"
	choicesMarker = "【choices】"
)

const (
	MinChoices     = 2
	MaxChoices     = 5
	MaxChoiceRunes = 20
	// MaxQuizHeaderRunes caps the title, subtitle and footer.
	MaxQuizHeaderRunes = 60
)

// Quiz is a question with quick-reply buttons. WhatsApp only renders the
// buttons when the message has a header title, subtitle and footer, so the
// model writes all three; an empty one is filled in when sending.
type Quiz struct {
	Title    string
	Subtitle string
	Footer   string
	Choices  []string
}

// WithQuiz appends the cleaned quiz to text. Fewer than MinChoices usable
// choices leaves the text as a plain reply.
func WithQuiz(text string, quiz Quiz) string {
	cleaned := make([]string, 0, len(quiz.Choices))
	seen := make(map[string]struct{}, len(quiz.Choices))
	for _, choice := range quiz.Choices {
		choice = cleanQuizField(choice, MaxChoiceRunes)
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
	header := []string{cleanQuizField(quiz.Title, MaxQuizHeaderRunes), cleanQuizField(quiz.Subtitle, MaxQuizHeaderRunes), cleanQuizField(quiz.Footer, MaxQuizHeaderRunes)}
	return strings.TrimRight(text, "\n ") + "\n\n" + quizMarker + " " + strings.Join(header, " | ") + "\n" + choicesMarker + " " + strings.Join(cleaned, " | ")
}

// cleanQuizField puts value on one line without "|" and cuts it to limit runes.
func cleanQuizField(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(value, "|", "/")), " ")
	if runes := []rune(value); len(runes) > limit {
		value = strings.TrimSpace(string(runes[:limit]))
	}
	return value
}

// SplitQuiz returns the text without its quiz lines and the quiz, or the text
// unchanged and nil when it carries no valid choices line.
func SplitQuiz(stored string) (string, *Quiz) {
	index := strings.LastIndex(stored, "\n"+choicesMarker)
	if index < 0 {
		return stored, nil
	}
	line := stored[index+1+len(choicesMarker):]
	if strings.Contains(line, "\n") {
		return stored, nil
	}
	quiz := &Quiz{}
	for _, choice := range strings.Split(line, "|") {
		if choice = strings.TrimSpace(choice); choice != "" {
			quiz.Choices = append(quiz.Choices, choice)
		}
	}
	if len(quiz.Choices) < MinChoices || len(quiz.Choices) > MaxChoices {
		return stored, nil
	}
	text := stored[:index]
	if headerIndex := strings.LastIndex(text, "\n"+quizMarker); headerIndex >= 0 && !strings.Contains(text[headerIndex+1:], "\n") {
		fields := strings.SplitN(text[headerIndex+1+len(quizMarker):], "|", 3)
		for len(fields) < 3 {
			fields = append(fields, "")
		}
		quiz.Title, quiz.Subtitle, quiz.Footer = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])
		text = text[:headerIndex]
	}
	return strings.TrimRight(text, "\n "), quiz
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
