// Package mention contains the provider-neutral rules for matching raw
// WhatsApp mention tokens. The durable text remains untouched; these helpers
// are only used to validate metadata and build the model-facing view.
package mention

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxTokenDigits = 32
	MaxBindings    = 128
)

// ValidToken accepts the raw form produced by WhatsApp message text: an at
// sign followed by the numeric user part of a PN or LID JID.
func ValidToken(token string) bool {
	if len(token) < 2 || len(token) > MaxTokenDigits+1 || token[0] != '@' {
		return false
	}
	for index := 1; index < len(token); index++ {
		if token[index] < '0' || token[index] > '9' {
			return false
		}
	}
	return true
}

// Contains reports whether token occurs as a complete mention rather than as
// a prefix of a longer identifier or inside an email-like word.
func Contains(text, token string) bool {
	if !ValidToken(token) {
		return false
	}
	for index := 0; index+len(token) <= len(text); {
		relative := strings.IndexByte(text[index:], '@')
		if relative < 0 {
			return false
		}
		start := index + relative
		end := start + len(token)
		if text[start:end] == token && boundaryBefore(text, start) && boundaryAfter(text, end) {
			return true
		}
		index = start + 1
	}
	return false
}

// Rewrite returns a model-facing copy of text. Replacement is one-pass, so a
// display name containing another raw token cannot trigger cascading rewrites.
func Rewrite(text string, replacements map[string]string) string {
	if len(replacements) == 0 || text == "" {
		return text
	}
	tokens := make([]string, 0, len(replacements))
	for token := range replacements {
		if ValidToken(token) {
			tokens = append(tokens, token)
		}
	}
	sort.Slice(tokens, func(left, right int) bool {
		if len(tokens[left]) == len(tokens[right]) {
			return tokens[left] < tokens[right]
		}
		return len(tokens[left]) > len(tokens[right])
	})
	if len(tokens) == 0 {
		return text
	}

	result := make([]byte, 0, len(text))
	written := 0
	for index := 0; index < len(text); {
		relative := strings.IndexByte(text[index:], '@')
		if relative < 0 {
			break
		}
		start := index + relative
		matched := ""
		if boundaryBefore(text, start) {
			for _, token := range tokens {
				end := start + len(token)
				if end <= len(text) && text[start:end] == token && boundaryAfter(text, end) {
					matched = token
					break
				}
			}
		}
		if matched == "" {
			index = start + 1
			continue
		}
		result = append(result, text[written:start]...)
		result = append(result, replacements[matched]...)
		written = start + len(matched)
		index = written
	}
	if written == 0 {
		return text
	}
	result = append(result, text[written:]...)
	return string(result)
}

func boundaryBefore(text string, index int) bool {
	if index == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	return !mentionWordRune(r) && r != '@'
}

func boundaryAfter(text string, index int) bool {
	if index == len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	return !mentionWordRune(r)
}

func mentionWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}
