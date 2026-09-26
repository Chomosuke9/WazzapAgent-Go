package app

import (
	_ "embed"
	"strings"
	"time"
)

//go:embed systemprompt.txt
var systemPolicySource string

// RenderSystemPolicy renders assistant and date placeholders for one
// invocation. The additional prompt placeholder is reserved for per-request
// substitution by the model adapter; literal braces in examples remain unchanged.
func RenderSystemPolicy(assistantName string, now time.Time) string {
	return renderSystemPolicy(systemPolicySource, assistantName, now)
}

func renderSystemPolicy(source, assistantName string, now time.Time) string {
	return strings.NewReplacer(
		"{{assistant_name}}", assistantName,
		"{{current_date}}", now.Format("02 Jan 2006"),
	).Replace(source)
}
