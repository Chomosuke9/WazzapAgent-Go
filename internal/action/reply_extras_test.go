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

func TestChoicesRoundTrip(t *testing.T) {
	stored := WithChoices("Capital of Indonesia?\n", []string{" Jakarta ", "Band|ung", "Jakarta", "", "A choice that is far too long to fit"})
	want := "Capital of Indonesia?\n\n【choices】 Jakarta | Band/ung | A choice that is far"
	if stored != want {
		t.Fatalf("WithChoices = %q, want %q", stored, want)
	}
	text, choices := SplitChoices(stored)
	if text != "Capital of Indonesia?" || !reflect.DeepEqual(choices, []string{"Jakarta", "Band/ung", "A choice that is far"}) {
		t.Fatalf("SplitChoices = %q %q", text, choices)
	}
}

func TestPlainRepliesKeepTheirText(t *testing.T) {
	if got := WithChoices("hi", []string{"only one"}); got != "hi" {
		t.Fatalf("one choice must not make a quiz: %q", got)
	}
	for _, text := range []string{"hi", "【choices】 a | b", "x\n【choices】 a | b\nmore", "x\n【choices】 only"} {
		if got, choices := SplitChoices(text); got != text || choices != nil {
			t.Fatalf("SplitChoices(%q) = %q %q", text, got, choices)
		}
	}
}
