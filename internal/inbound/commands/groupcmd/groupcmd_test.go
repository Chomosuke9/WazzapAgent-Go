package groupcmd

import "testing"

func TestParseAcceptsGroupCommandsWithOrWithoutLeadingSlash(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		kind        Kind
		description string
	}{
		{name: "close with slash", raw: "/group close", kind: Close},
		{name: "close without slash", raw: "group close", kind: Close},
		{name: "open without slash", raw: "group open", kind: Open},
		{name: "description with slash", raw: "/group description Aturan baru", kind: Description, description: "Aturan baru"},
		{name: "description without slash", raw: "group description Aturan baru", kind: Description, description: "Aturan baru"},
		{name: "delete without slash", raw: "group delete", kind: Delete},
		{name: "mute without slash", raw: "group mute @Alice (abc123) 15", kind: Mute},
		{name: "kick without slash", raw: "group kick @Alice Smith (abc123)", kind: Kick},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.raw, err)
			}
			if got.Kind != test.kind || got.Description != test.description {
				t.Fatalf("Parse(%q) = %#v, want kind %q description %q", test.raw, got, test.kind, test.description)
			}
		})
	}
}

func TestParseStillRequiresDescriptionTextWithoutLeadingSlash(t *testing.T) {
	if _, err := Parse("group description"); err == nil {
		t.Fatal("Parse accepted a description command without text")
	}
}
