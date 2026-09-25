package hypermeow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// NormalizeBroadcastPayload accepts a WhatsApp message, a WebMessageInfo-like
// envelope, or a fenced JSON block and returns canonical ProtoJSON for the
// waE2E.Message accepted by BroadcastGroups.
func NormalizeBroadcastPayload(input string) (string, error) {
	if len(input) > 2*maxBroadcastPayloadSize {
		return "", errors.New("Input is too large to normalize. Keep the raw message below 512 KiB.")
	}
	candidate, err := extractBroadcastJSON(input)
	if err != nil {
		return "", err
	}

	message := &waE2E.Message{}
	canonical, err := normalizeProtoJSON(candidate, message.ProtoReflect().Descriptor(), 0)
	if err != nil {
		return "", err
	}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(canonical, message); err != nil {
		return "", fmt.Errorf("JSON is valid, but its fields do not match a WhatsApp message: %w", err)
	}
	if proto.Size(message) == 0 {
		return "", errors.New("The JSON does not contain a WhatsApp message field.")
	}
	formatted, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(message)
	if err != nil {
		return "", fmt.Errorf("could not format the WhatsApp message: %w", err)
	}
	if len(formatted) > maxBroadcastPayloadSize {
		return "", errors.New("The normalized message is larger than the 256 KiB broadcast limit.")
	}
	return string(formatted), nil
}

func extractBroadcastJSON(input string) ([]byte, error) {
	if !utf8.ValidString(input) {
		return nil, errors.New("Input contains invalid UTF-8 text.")
	}
	candidate, err := candidateFromText(input)
	if err != nil {
		return nil, err
	}
	for depth := 0; depth < 5; depth++ {
		candidate, err = repairJSONFormatting(candidate)
		if err != nil {
			return nil, err
		}
		if err := rejectDuplicateJSONKeys(candidate); err != nil {
			return nil, err
		}

		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(candidate, &envelope); err != nil {
			var encoded string
			if stringErr := json.Unmarshal(candidate, &encoded); stringErr == nil {
				candidate, err = candidateFromText(encoded)
				if err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("Invalid JSON: %w", err)
		}
		if message, exists := envelope["message"]; exists {
			message = bytes.TrimSpace(message)
			if len(message) == 0 || bytes.Equal(message, []byte("null")) {
				return nil, errors.New("The raw-message envelope has no message object.")
			}
			if message[0] == '"' {
				var encoded string
				if err := json.Unmarshal(message, &encoded); err != nil {
					return nil, fmt.Errorf("The message field is not valid JSON text: %w", err)
				}
				candidate, err = candidateFromText(encoded)
				if err != nil {
					return nil, err
				}
			} else if message[0] == '{' {
				candidate = message
			} else {
				return nil, errors.New("The raw-message envelope's message field must contain a JSON object.")
			}
			continue
		}
		return candidate, nil
	}
	return nil, errors.New("The JSON contains too many nested message wrappers.")
}

func candidateFromText(input string) ([]byte, error) {
	input = strings.TrimSpace(strings.TrimPrefix(input, "\uFEFF"))
	if fenced, ok := fencedJSONBlock(input); ok {
		input = strings.TrimSpace(fenced)
	}
	if len(input) > 0 && input[0] == '"' {
		var encoded string
		if err := json.Unmarshal([]byte(input), &encoded); err == nil {
			input = strings.TrimSpace(encoded)
			if fenced, ok := fencedJSONBlock(input); ok {
				input = strings.TrimSpace(fenced)
			}
		}
	}
	if strings.HasPrefix(input, "{") {
		return []byte(input), nil
	}
	object, ok := firstJSONObject(input)
	if !ok {
		return nil, errors.New("Could not find a complete JSON object in the pasted text.")
	}
	return []byte(object), nil
}

func fencedJSONBlock(input string) (string, bool) {
	lines := strings.Split(input, "\n")
	for start, line := range lines {
		fenceSize, info := openingFence(strings.TrimSpace(strings.TrimSuffix(line, "\r")))
		if fenceSize < 3 || len(strings.Fields(info)) == 0 {
			continue
		}
		language := strings.ToLower(strings.Fields(info)[0])
		if language != "json" && language != "jsonc" {
			continue
		}
		for end := start + 1; end < len(lines); end++ {
			if closingFence(strings.TrimSpace(strings.TrimSuffix(lines[end], "\r")), fenceSize) {
				return strings.Join(lines[start+1:end], "\n"), true
			}
		}
	}
	return "", false
}

func openingFence(line string) (int, string) {
	count := 0
	for count < len(line) && line[count] == '`' {
		count++
	}
	if count < 3 {
		return 0, ""
	}
	return count, strings.TrimSpace(line[count:])
}

func closingFence(line string, minimum int) bool {
	count, remainder := openingFence(line)
	return count >= minimum && strings.TrimSpace(remainder) == ""
}

func firstJSONObject(input string) (string, bool) {
	start, depth := -1, 0
	inString, escaped := false, false
	for index := 0; index < len(input); index++ {
		current := input[index]
		if inString {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			continue
		}
		if current == '{' {
			if depth == 0 {
				start = index
			}
			depth++
		} else if current == '}' && depth > 0 {
			depth--
			if depth == 0 {
				return input[start : index+1], true
			}
		}
	}
	return "", false
}

func repairJSONFormatting(input []byte) ([]byte, error) {
	var commentsRemoved bytes.Buffer
	inString, escaped := false, false
	for index := 0; index < len(input); index++ {
		current := input[index]
		if inString {
			commentsRemoved.WriteByte(current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			commentsRemoved.WriteByte(current)
			continue
		}
		if current == '/' && index+1 < len(input) && input[index+1] == '/' {
			index += 2
			for index < len(input) && input[index] != '\n' {
				index++
			}
			if index < len(input) {
				commentsRemoved.WriteByte('\n')
			}
			continue
		}
		if current == '/' && index+1 < len(input) && input[index+1] == '*' {
			index += 2
			closed := false
			for index < len(input) {
				if input[index] == '\n' {
					commentsRemoved.WriteByte('\n')
				}
				if input[index] == '*' && index+1 < len(input) && input[index+1] == '/' {
					index++
					closed = true
					break
				}
				index++
			}
			if !closed {
				return nil, errors.New("JSON has an unfinished block comment.")
			}
			continue
		}
		commentsRemoved.WriteByte(current)
	}

	clean := commentsRemoved.Bytes()
	var trailingCommasRemoved bytes.Buffer
	inString, escaped = false, false
	for index := 0; index < len(clean); index++ {
		current := clean[index]
		if inString {
			trailingCommasRemoved.WriteByte(current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			trailingCommasRemoved.WriteByte(current)
			continue
		}
		if current == ',' {
			next := index + 1
			for next < len(clean) && (clean[next] == ' ' || clean[next] == '\t' || clean[next] == '\r' || clean[next] == '\n') {
				next++
			}
			if next < len(clean) && (clean[next] == '}' || clean[next] == ']') {
				continue
			}
		}
		trailingCommasRemoved.WriteByte(current)
	}
	return trailingCommasRemoved.Bytes(), nil
}

func rejectDuplicateJSONKeys(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("Invalid JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains extra content after the object.")
		}
		return fmt.Errorf("Invalid JSON: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON repeats the %q field; keep one value", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func normalizeProtoJSON(input []byte, descriptor protoreflect.MessageDescriptor, depth int) ([]byte, error) {
	if depth > 64 {
		return nil, errors.New("JSON message nesting is too deep to normalize safely.")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return input, nil
	}
	normalized := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		field := resolveProtoField(descriptor, name)
		if field == nil {
			normalized[name] = value
			continue
		}
		canonicalName := string(field.JSONName())
		if canonicalName == "" {
			canonicalName = string(field.Name())
		}
		if _, exists := normalized[canonicalName]; exists {
			return nil, fmt.Errorf("JSON contains more than one spelling of the %q field; keep one", canonicalName)
		}
		value, err := normalizeProtoFieldValue(value, field, depth+1)
		if err != nil {
			return nil, err
		}
		normalized[canonicalName] = value
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("could not normalize WhatsApp message fields: %w", err)
	}
	return encoded, nil
}

func resolveProtoField(descriptor protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	needle := fieldKeyIdentity(name)
	var match protoreflect.FieldDescriptor
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if needle != fieldKeyIdentity(string(field.Name())) && needle != fieldKeyIdentity(string(field.JSONName())) {
			continue
		}
		if match != nil && match.Number() != field.Number() {
			return nil
		}
		match = field
	}
	return match
}

func fieldKeyIdentity(value string) string {
	var normalized strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(unicode.ToLower(character))
		}
	}
	return normalized.String()
}

func normalizeProtoFieldValue(input []byte, field protoreflect.FieldDescriptor, depth int) ([]byte, error) {
	if field.IsMap() && field.MapValue().Kind() == protoreflect.MessageKind {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(input, &values); err != nil {
			return input, nil
		}
		for key, value := range values {
			value, err := normalizeProtoJSON(value, field.MapValue().Message(), depth+1)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return json.Marshal(values)
	}
	if field.IsList() && field.Kind() == protoreflect.MessageKind {
		var values []json.RawMessage
		if err := json.Unmarshal(input, &values); err != nil {
			return input, nil
		}
		for index, value := range values {
			value, err := normalizeProtoJSON(value, field.Message(), depth+1)
			if err != nil {
				return nil, err
			}
			values[index] = value
		}
		return json.Marshal(values)
	}
	if field.Kind() == protoreflect.MessageKind {
		return normalizeProtoJSON(input, field.Message(), depth+1)
	}
	if field.Kind() == protoreflect.StringKind && strings.HasSuffix(strings.ToLower(string(field.Name())), "paramsjson") {
		var nested string
		if err := json.Unmarshal(input, &nested); err != nil || strings.TrimSpace(nested) == "" {
			return input, nil
		}
		cleaned, err := repairJSONFormatting([]byte(nested))
		if err != nil {
			return nil, fmt.Errorf("%s contains invalid JSON: %w", field.Name(), err)
		}
		if err := rejectDuplicateJSONKeys(cleaned); err != nil {
			return nil, fmt.Errorf("%s contains invalid JSON: %w", field.Name(), err)
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(cleaned))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s contains invalid JSON: %w", field.Name(), err)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s contains extra content after its JSON value", field.Name())
		}
		formatted, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("could not format %s: %w", field.Name(), err)
		}
		return json.Marshal(string(formatted))
	}
	return input, nil
}
