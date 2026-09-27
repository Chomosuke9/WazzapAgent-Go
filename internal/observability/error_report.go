package observability

import (
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

const maxErrorChainDepth = 32

// ErrorReport renders err for a human reading a failure after the fact: the
// full message, then every layer of the wrap chain with its Go type (and the
// app's code and operation where a layer carries them), then any extra detail
// a layer prints with %+v, such as a stack trace. It never replaces the
// underlying library error with the app's own summary.
func ErrorReport(err error) string {
	if err == nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(err.Error())
	builder.WriteString("\n\nchain:")
	writeErrorChain(&builder, err, "\n  ", 0)
	if detail := fullError(err); detail != err.Error() {
		builder.WriteString("\n\ndetail:\n")
		builder.WriteString(detail)
	}
	return builder.String()
}

func writeErrorChain(builder *strings.Builder, err error, indent string, depth int) {
	for ; err != nil; depth++ {
		if depth >= maxErrorChainDepth {
			builder.WriteString(indent + "…")
			return
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			builder.WriteString(indent + "- " + errorLayer(err, nil))
			for _, child := range wrapped.Unwrap() {
				writeErrorChain(builder, child, indent+"  ", depth+1)
			}
			return
		case interface{ Unwrap() error }:
			next := wrapped.Unwrap()
			builder.WriteString(indent + "- " + errorLayer(err, next))
			err = next
		default:
			builder.WriteString(indent + "- " + errorLayer(err, nil))
			return
		}
	}
}

// errorLayer names one layer by its Go type and shows only the text that
// layer adds, so the wrapped cause below it is not repeated.
func errorLayer(err, cause error) string {
	label := fmt.Sprintf("%T", err)
	own := err.Error()
	if cause != nil {
		if inner := cause.Error(); inner != own && strings.HasSuffix(own, inner) {
			own = strings.TrimSuffix(strings.TrimSuffix(own, inner), ": ")
		}
	}
	if typed, ok := err.(*agent.Error); ok {
		label += " code=" + string(typed.Code())
		if typed.Operation() != "" {
			label += " op=" + fmt.Sprintf("%q", typed.Operation())
			if own == typed.Operation() {
				own = ""
			}
		}
	}
	if own == "" {
		return label
	}
	return label + ": " + own
}
