package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDiscoversDescriptorCompositeLiteralsDeterministically(t *testing.T) {
	directory := t.TempDir()
	for name, source := range map[string]string{
		"zeta.go": `package commands

import "example.test/internal/command"

var Zeta = command.Descriptor{Permission: "public and !fromMe"}
`,
		"alpha.go": `package commands

import "example.test/internal/command"

var Alpha = command.Descriptor{Permission: "public"}
`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	if err := generate(directory, "registry_gen.go"); err != nil {
		t.Fatalf("generate registry: %v", err)
	}
	outputPath := filepath.Join(directory, "registry_gen.go")
	first, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated registry: %v", err)
	}
	text := string(first)
	alphaIndex := strings.Index(text, "\tAlpha,")
	zetaIndex := strings.Index(text, "\tZeta,")
	if alphaIndex < 0 || zetaIndex < 0 || alphaIndex > zetaIndex {
		t.Fatalf("generated descriptor order = %q", text)
	}
	if err := generate(directory, "registry_gen.go"); err != nil {
		t.Fatalf("regenerate registry: %v", err)
	}
	second, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reread generated registry: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("generator output is not deterministic")
	}
}
