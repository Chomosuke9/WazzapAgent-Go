package action

import (
	"reflect"
	"testing"
)

func TestFirstCodeBlock(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{name: "none", text: "just `inline` text", want: ""},
		{name: "language tag", text: "Here:\n```go\nfmt.Println(1)\n```\nDone", want: "fmt.Println(1)"},
		{name: "first of two", text: "```\na\n```\n```\nb\n```", want: "a"},
		{name: "tildes", text: "~~~\nsome text\n~~~", want: "some text"},
		{name: "longer fence keeps inner fence", text: "````md\n```\nx\n```\n````", want: "```\nx\n```"},
		{name: "keeps first-line indentation", text: "```yaml\n\n  nested: true\n  other: 1\n\n```", want: "  nested: true\n  other: 1"},
		{name: "fence with a tag does not close", text: "```\n```javascript\nx()\n```\n", want: "```javascript\nx()"},
		{name: "longer closing fence", text: "```\na\n````", want: "a"},
		{name: "empty block", text: "```\n```", want: ""},
		{name: "info string with symbols", text: "```c#\nvar x = 1;\n```\n```\nlater\n```", want: "var x = 1;"},
		{name: "indented in a list", text: "1. Run:\n   ```sh\n   make\n   ```\n2. Done", want: "make"},
		{name: "indented block keeps inner indentation", text: "  ```py\n  if x:\n      y()\n  ```", want: "if x:\n    y()"},
		{name: "unclosed", text: "```\nnever closed", want: ""},
		{name: "inline triple backticks", text: "use ``` to fence", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FirstCodeBlock(test.text); got != test.want {
				t.Fatalf("FirstCodeBlock = %q, want %q", got, test.want)
			}
		})
	}
}

func TestQuizRoundTrip(t *testing.T) {
	stored := WithQuiz("Capital of Indonesia?\n", Quiz{
		Title: " Quick | quiz ", Subtitle: "Geography", Footer: "Tap\nan answer",
		Choices: []string{" Jakarta ", "Band|ung", "Jakarta", "", "A choice that is far too long to fit"},
	})
	want := "Capital of Indonesia?\n\n【quiz】 Quick / quiz | Geography | Tap an answer\n【choices】 Jakarta | Band/ung | A choice that is far"
	if stored != want {
		t.Fatalf("WithQuiz = %q, want %q", stored, want)
	}
	text, quiz := SplitQuiz(stored)
	wantQuiz := &Quiz{Title: "Quick / quiz", Subtitle: "Geography", Footer: "Tap an answer", Choices: []string{"Jakarta", "Band/ung", "A choice that is far"}}
	if text != "Capital of Indonesia?" || !reflect.DeepEqual(quiz, wantQuiz) {
		t.Fatalf("SplitQuiz = %q %+v", text, quiz)
	}
}

// Rows stored before quizzes had a header carry only the choices line.
func TestQuizWithoutHeaderLine(t *testing.T) {
	text, quiz := SplitQuiz("Capital?\n\n【choices】 Jakarta | Bandung")
	if text != "Capital?" || !reflect.DeepEqual(quiz, &Quiz{Choices: []string{"Jakarta", "Bandung"}}) {
		t.Fatalf("SplitQuiz = %q %+v", text, quiz)
	}
}

func TestPlainRepliesKeepTheirText(t *testing.T) {
	if got := WithQuiz("hi", Quiz{Title: "T", Choices: []string{"only one"}}); got != "hi" {
		t.Fatalf("one choice must not make a quiz: %q", got)
	}
	for _, text := range []string{"hi", "【choices】 a | b", "x\n【choices】 a | b\nmore", "x\n【choices】 only"} {
		if got, quiz := SplitQuiz(text); got != text || quiz != nil {
			t.Fatalf("SplitQuiz(%q) = %q %+v", text, got, quiz)
		}
	}
}
