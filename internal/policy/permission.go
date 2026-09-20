package policy

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PermissionFacts are the trusted facts used to evaluate a command's
// declarative permission expression. The policy boundary is responsible for
// producing these facts; command text must never be used to produce them.
type PermissionFacts struct {
	IsOwner   bool
	IsAdmin   bool
	IsGroup   bool
	IsPrivate bool
	FromMe    bool
}

type permissionTokenKind uint8

const (
	permissionEOF permissionTokenKind = iota
	permissionAtom
	permissionAnd
	permissionOr
	permissionNot
	permissionOpen
	permissionClose
)

type permissionToken struct {
	kind permissionTokenKind
	text string
}

// EvaluatePermission evaluates the command permission DSL.
//
// Grammar:
//
//	or  := and ("or" and)*
//	and := unary ("and" unary)*
//	 unary := "!" unary | primary
//	primary := atom | "(" or ")"
//
// Unary negation binds tightest, then and, then or. Atom names are
// case-insensitive and support the old command aliases, including fromMe,
// from_me, and from-me.
func EvaluatePermission(expression string, facts PermissionFacts) (bool, error) {
	tokens, err := tokenizePermission(expression)
	if err != nil {
		return false, err
	}
	parser := permissionParser{tokens: tokens, facts: facts}
	value, err := parser.parseOr()
	if err != nil {
		return false, err
	}
	if token := parser.peek(); token.kind != permissionEOF {
		return false, fmt.Errorf("unexpected token %q", token.text)
	}
	return value, nil
}

// ValidatePermission checks a descriptor expression without evaluating it for
// a particular invocation. NewRegistry calls this at startup so a typo fails
// fast instead of silently denying a command at runtime.
func ValidatePermission(expression string) error {
	if _, err := EvaluatePermission(expression, PermissionFacts{}); err != nil {
		return err
	}
	return nil
}

func tokenizePermission(expression string) ([]permissionToken, error) {
	tokens := make([]permissionToken, 0, 8)
	for offset := 0; offset < len(expression); {
		runeValue, width := utf8.DecodeRuneInString(expression[offset:])
		if runeValue == utf8.RuneError && width == 1 {
			return nil, fmt.Errorf("permission expression contains invalid UTF-8")
		}
		switch {
		case unicode.IsSpace(runeValue):
			offset += width
		case runeValue == '!':
			tokens = append(tokens, permissionToken{kind: permissionNot, text: string(runeValue)})
			offset += width
		case runeValue == '(':
			tokens = append(tokens, permissionToken{kind: permissionOpen, text: string(runeValue)})
			offset += width
		case runeValue == ')':
			tokens = append(tokens, permissionToken{kind: permissionClose, text: string(runeValue)})
			offset += width
		case isPermissionIdentifierStart(runeValue):
			start := offset
			offset += width
			for offset < len(expression) {
				next, nextWidth := utf8.DecodeRuneInString(expression[offset:])
				if next == utf8.RuneError && nextWidth == 1 {
					return nil, fmt.Errorf("permission expression contains invalid UTF-8")
				}
				if !isPermissionIdentifierPart(next) {
					break
				}
				offset += nextWidth
			}
			text := expression[start:offset]
			switch strings.ToLower(text) {
			case "and":
				tokens = append(tokens, permissionToken{kind: permissionAnd, text: text})
			case "or":
				tokens = append(tokens, permissionToken{kind: permissionOr, text: text})
			default:
				tokens = append(tokens, permissionToken{kind: permissionAtom, text: text})
			}
		default:
			return nil, fmt.Errorf("invalid character %q in permission expression", runeValue)
		}
	}
	tokens = append(tokens, permissionToken{kind: permissionEOF})
	return tokens, nil
}

func isPermissionIdentifierStart(value rune) bool {
	return value == '_' || (value < utf8.RuneSelf && ((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')))
}

func isPermissionIdentifierPart(value rune) bool {
	return isPermissionIdentifierStart(value) || (value >= '0' && value <= '9') || value == '-'
}

type permissionParser struct {
	tokens []permissionToken
	index  int
	facts  PermissionFacts
}

func (parser *permissionParser) peek() permissionToken {
	if parser.index >= len(parser.tokens) {
		return permissionToken{kind: permissionEOF}
	}
	return parser.tokens[parser.index]
}

func (parser *permissionParser) take() permissionToken {
	token := parser.peek()
	if parser.index < len(parser.tokens) {
		parser.index++
	}
	return token
}

func (parser *permissionParser) parseOr() (bool, error) {
	value, err := parser.parseAnd()
	if err != nil {
		return false, err
	}
	for parser.peek().kind == permissionOr {
		parser.take()
		right, err := parser.parseAnd()
		if err != nil {
			return false, err
		}
		value = value || right
	}
	return value, nil
}

func (parser *permissionParser) parseAnd() (bool, error) {
	value, err := parser.parseUnary()
	if err != nil {
		return false, err
	}
	for parser.peek().kind == permissionAnd {
		parser.take()
		right, err := parser.parseUnary()
		if err != nil {
			return false, err
		}
		value = value && right
	}
	return value, nil
}

func (parser *permissionParser) parseUnary() (bool, error) {
	if parser.peek().kind == permissionNot {
		parser.take()
		value, err := parser.parseUnary()
		if err != nil {
			return false, err
		}
		return !value, nil
	}
	return parser.parsePrimary()
}

func (parser *permissionParser) parsePrimary() (bool, error) {
	token := parser.take()
	switch token.kind {
	case permissionAtom:
		return resolvePermissionAtom(token.text, parser.facts)
	case permissionOpen:
		value, err := parser.parseOr()
		if err != nil {
			return false, err
		}
		if closing := parser.take(); closing.kind != permissionClose {
			if closing.kind == permissionEOF {
				return false, fmt.Errorf("missing closing parenthesis")
			}
			return false, fmt.Errorf("expected closing parenthesis, got %q", closing.text)
		}
		return value, nil
	case permissionEOF:
		return false, fmt.Errorf("permission expression is empty or incomplete")
	case permissionClose:
		return false, fmt.Errorf("unexpected closing parenthesis")
	case permissionAnd, permissionOr:
		return false, fmt.Errorf("unexpected operator %q", token.text)
	default:
		return false, fmt.Errorf("unexpected permission token %q", token.text)
	}
}

func resolvePermissionAtom(name string, facts PermissionFacts) (bool, error) {
	switch strings.ToLower(name) {
	case "public":
		return true, nil
	case "owner", "isowner":
		return facts.IsOwner, nil
	case "admin", "isadmin", "senderisadmin", "sender_is_admin", "sender-is-admin":
		return facts.IsAdmin, nil
	case "group", "isgroup":
		return facts.IsGroup, nil
	case "private", "isprivate":
		return facts.IsPrivate, nil
	case "from_me", "fromme", "from-me":
		return facts.FromMe, nil
	default:
		return false, fmt.Errorf("unknown permission atom %q", name)
	}
}
