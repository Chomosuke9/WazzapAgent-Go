package commands

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
)

func init() {
	register(command.Command{
		Name:    "memory",
		Aliases: []string{"memo"},
		// The bot keeps its own notes; people who may change this chat's
		// settings can manage them by hand too.
		Permission: "fromMe or owner or (admin and group) or private",
		Description: "Long-term memory: notes the AI sees on every turn in this chat. " +
			"/memory lists them with their IDs; /memory add <text> saves one; " +
			"/memory delete <ID>[,<ID>...] deletes one or more.",
		DeniedReply: "In a group, only an admin or the owner can change my memory. Just tell me what to remember.",
		Run:         runMemory,
	})
}

func runMemory(ctx context.Context, c *command.Context) error {
	store, err := c.Memory()
	if err != nil {
		return err
	}
	action, rest := parseMemoryArgs(c.Args)
	switch action {
	case "list":
		return replyMemories(ctx, c, store)
	case "add":
		return addMemory(ctx, c, store, memoryMentions(rest, c.Message.Mentions, c.AssistantName()))
	case "delete":
		return deleteMemories(ctx, c, store, rest)
	}
	return c.Reply(ctx, memoryUsage())
}

// parseMemoryArgs returns "list", "add" with the text, "delete" with the
// IDs, or "" for unknown arguments.
func parseMemoryArgs(args string) (action, rest string) {
	args = strings.TrimSpace(args)
	verb, rest := args, ""
	if index := strings.IndexFunc(args, unicode.IsSpace); index >= 0 {
		verb, rest = args[:index], strings.TrimSpace(args[index:])
	}
	switch strings.ToLower(verb) {
	case "", "list", "show":
		if rest == "" {
			return "list", ""
		}
	case "add", "save", "remember":
		if rest != "" {
			return "add", rest
		}
	case "delete", "del", "remove", "rm", "forget":
		if rest != "" {
			return "delete", rest
		}
	}
	return "", ""
}

// parseMemoryCodes splits "ab12, cd34 ef56" into IDs.
func parseMemoryCodes(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

func replyMemories(ctx context.Context, c *command.Context, store command.MemoryStore) error {
	memories, err := store.Memories(ctx, c.Key())
	if err != nil {
		return err
	}
	if len(memories) == 0 {
		return c.Reply(ctx, "🧠 *Long-term memory*: nothing saved for this chat yet.\n\nSave a note with /memory add <text>, or just ask me to remember something.")
	}
	lines := []string{fmt.Sprintf("🧠 *Long-term memory* (%d/%d):", len(memories), agent.MaxMemories)}
	for _, memory := range memories {
		lines = append(lines, "• "+memory.Code+": "+memory.Text)
	}
	lines = append(lines, "", "Delete with /memory delete <ID>[,<ID>...].")
	return c.Reply(ctx, strings.Join(lines, "\n"))
}

func addMemory(ctx context.Context, c *command.Context, store command.MemoryStore, text string) error {
	if err := agent.ValidateMemoryText(text); err != nil {
		return c.Reply(ctx, fmt.Sprintf("A memory is 1 to %d characters. Keep it short: one fact or preference.", agent.MaxMemoryRunes))
	}
	memory, added, err := store.AddMemory(ctx, c.Key(), text)
	switch {
	case agent.IsCode(err, agent.ErrorResourceExhausted):
		return c.Reply(ctx, fmt.Sprintf("Memory is full (%d notes). Delete some first with /memory delete <ID>.", agent.MaxMemories))
	case err != nil:
		return err
	case !added:
		return c.Reply(ctx, "🧠 Already saved as "+memory.Code+".")
	}
	return c.Reply(ctx, "🧠 Saved as "+memory.Code+": "+memory.Text)
}

func deleteMemories(ctx context.Context, c *command.Context, store command.MemoryStore, text string) error {
	codes := parseMemoryCodes(text)
	deleted, err := store.DeleteMemories(ctx, c.Key(), codes)
	if err != nil {
		return err
	}
	if len(deleted) == 0 {
		return c.Reply(ctx, "No memory has the ID "+strings.Join(codes, ", ")+". /memory lists the IDs.")
	}
	lines := []string{fmt.Sprintf("🗑️ Deleted %d memory note(s):", len(deleted))}
	for _, memory := range deleted {
		lines = append(lines, "• "+memory.Code+": "+memory.Text)
	}
	return c.Reply(ctx, strings.Join(lines, "\n"))
}

// memoryMentions rewrites the raw WhatsApp mentions a person typed into the
// @Name (senderRef) form the model uses to tag someone. The bot writes that
// form itself.
func memoryMentions(text string, bindings []conversation.MentionBinding, assistantName string) string {
	replacements := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Bot {
			replacements[binding.Token] = "@" + assistantName + " (Bot)"
		} else {
			replacements[binding.Token] = "@member (" + binding.SenderRef.String() + ")"
		}
	}
	return mention.Rewrite(text, replacements)
}

func memoryUsage() string {
	return "🧠 *Long-term memory*: notes I see on every turn in this chat.\n" +
		"/memory: list them\n" +
		"/memory add <text>: save one\n" +
		"/memory delete <ID>[,<ID>...]: delete one or more"
}
