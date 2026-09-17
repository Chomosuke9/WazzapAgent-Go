// Package command owns declarative slash-command identity, parsing, permission
// evaluation, and the narrow context passed to command handlers. It does not
// import provider code.
package command

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

type Name string

var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Adapter interface {
	SendText(context.Context, action.SendTextRequest) (action.SendTextResult, error)
}

type Handler func(context.Context, Context, Adapter) error

// PermissionFacts is an alias for the policy facts accepted by the permission
// DSL. Keeping the alias here lets command modules remain declarative while
// trusted policy implementations own how facts are obtained.
type PermissionFacts = policy.PermissionFacts

type Descriptor struct {
	Name        Name
	Aliases     []string
	Capability  policy.Capability
	Permission  string
	Description string
	DeniedReply string
	Handler     Handler
}

type Request struct {
	Name             Name
	Arguments        string
	ArgumentsPresent bool
}

// Context contains the trusted application collaborators and permission facts
// a command needs. Facts are produced by the policy boundary and are evaluated
// again by Registry.Dispatch immediately before the handler runs. Command
// handlers must not infer authority from message text or this context.
type Context struct {
	Agent    *agent.Agent
	Snapshot agent.ConfigSnapshot
	Message  conversation.IncomingMessage
	Facts    PermissionFacts
	Registry *Registry
	Store    CommandStore
	Observer Observer
	Adapter  Adapter
}

type CommandStore interface {
	MarkCommandHandled(context.Context, conversation.IncomingMessage) error
	BeginPromptMutation(context.Context, conversation.IncomingMessage, PromptCommand, agent.ConfigVersion) (PromptMutation, error)
	MarkPromptMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error
	BeginPermissionMutation(context.Context, conversation.IncomingMessage, PermissionCommand, agent.ConfigVersion) (PromptMutation, error)
	MarkPermissionMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error
}

type Observer interface {
	ObserveHistoryReset()
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
		if !tokenPattern.MatchString(string(descriptor.Name)) || !descriptor.Capability.Valid() || strings.TrimSpace(descriptor.Permission) == "" {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("command name, capability, and permission are required"))
		}
		descriptor.Permission = strings.TrimSpace(descriptor.Permission)
		if err := ValidatePermission(descriptor.Permission); err != nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("permission for %q is invalid: %w", descriptor.Name, err))
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

// Dispatch invokes the handler attached to a registered request after
// evaluating its declarative permission against the trusted invocation facts.
// Aliases are normalized here as a defensive measure, although Parse already
// returns a canonical request in the normal inbound path.
func (registry *Registry) Dispatch(ctx context.Context, request Request, commandContext Context) error {
	if registry == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch command", fmt.Errorf("command registry is nil"))
	}
	canonical, ok := registry.tokens[strings.ToLower(strings.TrimSpace(string(request.Name)))]
	if !ok {
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch command", fmt.Errorf("command is not registered"))
	}
	request.Name = canonical
	descriptor, ok := registry.descriptors[canonical]
	if !ok || descriptor.Handler == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch command", fmt.Errorf("command handler is not registered"))
	}
	allowed, err := EvaluatePermission(descriptor.Permission, commandContext.Facts)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch command", err)
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "dispatch command", fmt.Errorf("permission expression denied command"))
	}
	return descriptor.Handler(ctx, commandContext, commandContext.Adapter)
}

// Allows evaluates the permission for a recognized request without invoking
// its handler. It is useful when the caller needs to send a command-specific
// denial reply while keeping the final check in Dispatch.
func (registry *Registry) Allows(request Request, facts PermissionFacts) (bool, error) {
	if registry == nil {
		return false, agent.NewError(agent.ErrorIntegrityFailure, "check command permission", fmt.Errorf("command registry is nil"))
	}
	canonical, ok := registry.tokens[strings.ToLower(strings.TrimSpace(string(request.Name)))]
	if !ok {
		return false, agent.NewError(agent.ErrorIntegrityFailure, "check command permission", fmt.Errorf("command is not registered"))
	}
	descriptor, ok := registry.descriptors[canonical]
	if !ok {
		return false, agent.NewError(agent.ErrorIntegrityFailure, "check command permission", fmt.Errorf("command descriptor is not registered"))
	}
	allowed, err := EvaluatePermission(descriptor.Permission, facts)
	if err != nil {
		return false, agent.NewError(agent.ErrorIntegrityFailure, "check command permission", err)
	}
	return allowed, nil
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
