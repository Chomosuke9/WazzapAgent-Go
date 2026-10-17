package command

// The permission grammar is implemented by the policy package so trusted
// policy adapters can resolve the same expression before dispatch. These
// wrappers keep the command-module API concise for descriptor tests and
// command authors.

import "github.com/Chomosuke9/WazzapAgent-Go/internal/policy"

func EvaluatePermission(expression string, facts PermissionFacts) (bool, error) {
	return policy.EvaluatePermission(expression, facts)
}

func ValidatePermission(expression string) error {
	return policy.ValidatePermission(expression)
}
