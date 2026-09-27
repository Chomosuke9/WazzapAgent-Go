package command

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ErrDenied is wrapped by Dispatch when a command's Permission rejects the
// sender, so a host can tell that apart from a permission error raised
// while the command ran (for example the bot not being a group admin).
var ErrDenied = errors.New("permission expression denied the command")

// Request is a parsed "/token args" message, resolved to a canonical name.
type Request struct {
	Name             string
	Arguments        string
	ArgumentsPresent bool
}

// Registry maps every command name and alias to its Command.
type Registry struct {
	commands map[string]Command
	tokens   map[string]string
}

func NewRegistry(commands []Command) (*Registry, error) {
	if len(commands) == 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("at least one command is required"))
	}
	registry := &Registry{commands: make(map[string]Command, len(commands)), tokens: make(map[string]string, len(commands)*2)}
	for _, cmd := range commands {
		if !tokenPattern.MatchString(cmd.Name) || cmd.Run == nil || strings.TrimSpace(cmd.Permission) == "" {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("command %q needs a lowercase name, a permission, and Run", cmd.Name))
		}
		cmd.Permission = strings.TrimSpace(cmd.Permission)
		if err := ValidatePermission(cmd.Permission); err != nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("permission for %q is invalid: %w", cmd.Name, err))
		}
		aliases := make([]string, 0, len(cmd.Aliases))
		for index, token := range append([]string{cmd.Name}, cmd.Aliases...) {
			token = strings.ToLower(strings.TrimSpace(token))
			if !tokenPattern.MatchString(token) {
				return nil, agent.NewError(agent.ErrorInvalidArgument, "create command registry", fmt.Errorf("command token %q is invalid", token))
			}
			if owner, exists := registry.tokens[token]; exists {
				return nil, agent.NewError(agent.ErrorConflict, "create command registry", fmt.Errorf("/%s is claimed by both %q and %q", token, owner, cmd.Name))
			}
			registry.tokens[token] = cmd.Name
			if index > 0 {
				aliases = append(aliases, token)
			}
		}
		cmd.Aliases = aliases
		registry.commands[cmd.Name] = cmd
	}
	return registry, nil
}

// Parse returns recognized=false for non-slash text and unknown slash tokens.
// A recognized command stays recognized even when its arguments are invalid:
// the command owns syntax feedback and must never fall through to the AI.
func (registry *Registry) Parse(text string) (request Request, cmd Command, recognized bool) {
	if registry == nil || !strings.HasPrefix(text, "/") {
		return Request{}, Command{}, false
	}
	token, arguments, argumentsPresent := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	name, found := registry.tokens[strings.ToLower(strings.TrimSpace(token))]
	if !found {
		return Request{}, Command{}, false
	}
	return Request{Name: name, Arguments: arguments, ArgumentsPresent: argumentsPresent}, registry.commands[name], true
}

// Allows evaluates a command's permission without running it.
func (registry *Registry) Allows(request Request, facts PermissionFacts) (bool, error) {
	cmd, err := registry.lookup(request.Name, "check command permission")
	if err != nil {
		return false, err
	}
	allowed, err := EvaluatePermission(cmd.Permission, facts)
	if err != nil {
		return false, agent.NewError(agent.ErrorIntegrityFailure, "check command permission", err)
	}
	return allowed, nil
}

// Dispatch checks the command's permission, runs it, and marks the inbox
// message handled once Run succeeds. A denied command returns an
// ErrorPermissionDenied wrapping ErrDenied without running.
func (registry *Registry) Dispatch(ctx context.Context, request Request, invocation Invocation) error {
	cmd, err := registry.lookup(request.Name, "dispatch command")
	if err != nil {
		return err
	}
	allowed, err := registry.Allows(request, invocation.Facts)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "dispatch command", fmt.Errorf("/%s: %w", cmd.Name, ErrDenied))
	}
	c := &Context{
		Name: cmd.Name, Args: request.Arguments, HasArgs: request.ArgumentsPresent,
		Message: invocation.Message, Facts: invocation.Facts, Agent: invocation.Agent, Config: invocation.Config,
		registry: registry, invocation: invocation,
	}
	if err := cmd.Run(ctx, c); err != nil {
		return err
	}
	if invocation.Store == nil {
		return nil
	}
	return invocation.Store.MarkCommandHandled(ctx, invocation.Message)
}

// Commands returns every registered command, sorted by name.
func (registry *Registry) Commands() []Command {
	if registry == nil {
		return nil
	}
	result := make([]Command, 0, len(registry.commands))
	for _, cmd := range registry.commands {
		cmd.Aliases = append([]string(nil), cmd.Aliases...)
		result = append(result, cmd)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

func (registry *Registry) lookup(name, operation string) (Command, error) {
	if registry == nil {
		return Command{}, agent.NewError(agent.ErrorIntegrityFailure, operation, fmt.Errorf("command registry is nil"))
	}
	canonical, ok := registry.tokens[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Command{}, agent.NewError(agent.ErrorIntegrityFailure, operation, fmt.Errorf("/%s is not registered", name))
	}
	return registry.commands[canonical], nil
}
