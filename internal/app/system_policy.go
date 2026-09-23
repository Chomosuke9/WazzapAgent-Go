package app

import (
	_ "embed"
	"strings"
	"time"
)

//go:embed systemprompt.txt
var systemPolicySource string

// RenderSystemPolicy renders the immutable system policy for one invocation.
// Only the supported placeholders are replaced; literal braces in examples
// remain unchanged.
func RenderSystemPolicy(assistantName string, now time.Time) string {
	return renderSystemPolicy(systemPolicySource, assistantName, now)
}

func renderSystemPolicy(source, assistantName string, now time.Time) string {
	return strings.NewReplacer(
		"{{assistant_name}}", assistantName,
		"{{current_date}}", now.Format("02 Jan 2006"),
	).Replace(source)
}
