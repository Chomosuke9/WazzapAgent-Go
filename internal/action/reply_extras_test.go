package action

import (
	"reflect"
	"testing"
)

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
