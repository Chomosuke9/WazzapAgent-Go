// Package command owns declarative slash-command identity and parsing. It does
// not authorize commands and does not import provider or Agent code.
package command

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

type Name string

var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Descriptor struct {
	Name       Name
	Aliases    []string
	Capability policy.Capability
}

type Request struct {
	Name             Name
	Arguments        string
	ArgumentsPresent bool
}

type Registry struct {
	descriptors map[Name]Descriptor
	tokens      map[string]Name
}

func NewRegistry(descriptors []Descriptor) (*Registry, error) {
	if len(descriptors) == 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("at least one command is required"))
	}
	registry := &Registry{descriptors: make(map[Name]Descriptor, len(descriptors)), tokens: make(map[string]Name, len(descriptors)*2)}
	for _, descriptor := range descriptors {
		if !tokenPattern.MatchString(string(descriptor.Name)) || !descriptor.Capability.Valid() {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("command name and capability are required"))
		}
		if _, exists := registry.descriptors[descriptor.Name]; exists {
			return nil, agent.NewError(agent.ErrorConflict, "create command registry", fmt.Errorf("canonical command is duplicated"))
		}
		aliases := append([]string{string(descriptor.Name)}, descriptor.Aliases...)
		copiedAliases := make([]string, 0, len(descriptor.Aliases))
		for index, token := range aliases {
			token = strings.ToLower(strings.TrimSpace(token))
			if !tokenPattern.MatchString(token) {
				return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("command token is invalid"))
			}
			if _, exists := registry.tokens[token]; exists {
				return nil, agent.NewError(agent.ErrorConflict, "create command registry", fmt.Errorf("command token is duplicated"))
			}
			registry.tokens[token] = descriptor.Name
			if index > 0 {
				copiedAliases = append(copiedAliases, token)
			}
		}
		descriptor.Aliases = copiedAliases
		registry.descriptors[descriptor.Name] = descriptor
	}
	return registry, nil
}

// Parse returns recognized=false for non-slash text and unknown slash tokens.
// A recognized command remains recognized even when its arguments are invalid;
// the command handler owns syntax feedback and must never fall through to AI.
func (registry *Registry) Parse(text string) (request Request, descriptor Descriptor, recognized bool) {
	if registry == nil || !strings.HasPrefix(text, "/") {
		return Request{}, Descriptor{}, false
	}
	remainder := strings.TrimPrefix(text, "/")
	token, arguments, argumentsPresent := strings.Cut(remainder, " ")
	if !argumentsPresent {
		arguments = ""
	}
	canonical, found := registry.tokens[strings.ToLower(strings.TrimSpace(token))]
	if !found {
		return Request{}, Descriptor{}, false
	}
	descriptor = registry.descriptors[canonical]
	return Request{Name: canonical, Arguments: arguments, ArgumentsPresent: argumentsPresent}, descriptor, true
}

func (registry *Registry) Descriptors() []Descriptor {
	if registry == nil {
		return nil
	}
	names := make([]Name, 0, len(registry.descriptors))
	for name := range registry.descriptors {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool { return names[left] < names[right] })
	result := make([]Descriptor, 0, len(names))
	for _, name := range names {
		descriptor := registry.descriptors[name]
		descriptor.Aliases = append([]string(nil), descriptor.Aliases...)
		result = append(result, descriptor)
	}
	return result
}
