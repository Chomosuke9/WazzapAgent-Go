package app

import (
	_ "embed"
	"strings"
)

//go:embed systemprompt.txt
var systemPolicySource string

// RenderSystemPolicy renders the assistant name once per runtime. The date and
// additional prompt placeholders change per request, so the model adapter fills
// them in; literal braces in examples remain unchanged.
func RenderSystemPolicy(assistantName string) string {
	return renderSystemPolicy(systemPolicySource, assistantName)
}

func renderSystemPolicy(source, assistantName string) string {
	return strings.ReplaceAll(source, "{{assistant_name}}", assistantName)
}
