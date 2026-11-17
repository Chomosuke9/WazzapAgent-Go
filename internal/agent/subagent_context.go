package agent

import (
	"regexp"
	"strings"
)

// SubagentContext is what a chat with the sub-agent switched on adds to the
// model's context, after <chat_state>.
type SubagentContext struct {
	// Off is set for a chat whose sub-agent is switched off: the model is
	// told it cannot act beyond chatting, so it does not pretend to.
	Off bool
	// Status describes the chat's sub-agent task: the one running (with its
	// progress), one that just finished, or that none is running.
	Status string
	// Result is set only on the turn that delivers a finished task: its
	// report and what the model may do with it.
	Result string
	// ResultImages are the finished task's image files, shown to the model
	// on that same turn only.
	ResultImages []ModelImage
}

// subagentRules tells the model when to delegate with execute_subtask and
// what the sub-agent can do.
const subagentRules = `<subagent>
SUB-AGENT is ALLOWED. Use ` + "`execute_subtask`" + ` for tasks needing a real compute environment: file processing, code execution, file analysis, web scraping, producing attachments, or anything you can't answer from knowledge alone. Assume full internet + system access — it can do almost anything.
IT'S THE ONLY WAY TO ACTUALLY DO WHAT AGENTS ARE MEANT TO DO. DO NOT HALLUCINATE AND SAY YOU ALREADY DID SOMETHING WITHOUT USING THIS.
You can't access the internet, manipulate files, or perform any actions that require system-level access. But the sub-agent has a shell and environment that can do anything you tell it to do.
Do not say "Sorry, as an AI assistant, I can't do that." or any similar phrases, unless the sub-agent cannot handle the task.

Do NOT use for: conversational replies, greetings, opinions, knowledge-only answers, moderation, or tasks covered by built-in commands.
Never use ` + "`execute_subtask`" + ` for tasks built-in commands can handle (stickers, tasks, memory, group management, etc.). Sub-agent is sandboxed — no WhatsApp access.

Rules:
- Call ` + "`execute_subtask`" + ` immediately when applicable. Acknowledgement goes in ` + "`confirmation_text`" + `, NOT as a separate ` + "`reply_message`" + `.
- ` + "`instruction`" + `: the sub-agent has NO chat history and NO memory of your previous instructions. Write a self-contained brief that states the full goal — assume it knows nothing about this chat.
- Text-only messages (e.g. a pasted story) are auto-converted to ` + "`.txt`" + ` for the sub-agent.
- ` + "`high_quality=true`" + ` for complex reasoning, image/code gen/editing. ` + "`high_quality=false`" + ` (default) for routine tasks.
- NEVER say "I don't know / I can't / I'm not sure" if a sub-agent could find or compute the answer. Uncertainty without attempting a sub-agent is a failure. It's your knowledge and capability extension — use it.

Choosing ` + "`context_msg_ids`" + `: the ID of the message that HOLDS the file, never the request that refers to it. When any file exists, the ` + "`<files_in_chat>`" + ` list gives you the exact IDs; copy from there, and reuse a file you sent earlier's ID to revise it.

Steering / correction (re-using the sub-agent's live memory):
- If a sub-agent IS currently running, you can steer it mid-task by calling ` + "`execute_subtask`" + ` again — e.g. user changes "draw a dog" to a cat: "Change the dog to a cat, keep everything else the same."
- You can ALSO attach NEW files while steering: pass their ` + "`context_msg_ids`" + ` (same rule as above — the ID that holds the file). The running sub-agent receives them mid-task. e.g. user sends a logo and says "add this logo": call ` + "`execute_subtask`" + ` with the logo's ` + "`context_msg_ids`" + `.
- On the re-invoke AFTER the sub-agent delivered its result (your LAST chance), you can protest/correct using the same memory — e.g. "The zip you sent shows as a compressed file, not an image. Send the image directly as a PNG, not zipped."
</subagent>`

// subagentOffRules is the block for a chat whose sub-agent is off.
const subagentOffRules = `<subagent>
Sub-agent isn't allowed.
You don't have:
    - Access to the internet, files, or any external resources.
    - Any capability beyond a regular chatbot.
    - Ability to send files, images, audio, or any media.
DO NOT hallucinate and say "I'm gonna do that" (that means you're telling the user you will start your work, but in reality you're actually just saying some bullshit and not actually doing anything).
</subagent>`

// fileKinds maps the media placeholder a message's text starts with (see
// conversation.Placeholder*) to the kind shown in <files_in_chat>.
var fileKinds = []struct{ placeholder, kind string }{
	{"【image】", "image"}, {"【video】", "video"}, {"【gif】", "video"}, {"【document】", "document"},
	{"【audio】", "audio"}, {"【voice note】", "audio"}, {"【sticker】", "sticker"},
}

// FileKind reports whether text is a media message and, if so, its kind and
// its caption (for a document the bot sent, the file name).
func FileKind(text string) (kind, caption string, ok bool) {
	for _, entry := range fileKinds {
		if rest, found := strings.CutPrefix(text, entry.placeholder); found && (rest == "" || rest[0] == ' ') {
			return entry.kind, strings.TrimSpace(rest), true
		}
	}
	return "", "", false
}

// subagentBoundary matches the block's own tags, which a sub-agent report or
// a task instruction must not be able to close early.
var subagentBoundary = regexp.MustCompile(`(?i)</?subagent\s*>`)

func escapeSubagentBoundary(text string) string {
	return subagentBoundary.ReplaceAllStringFunc(text, escapeTagDelimiters.Replace)
}

func formatSubagentContext(context SubagentContext, history []HistoryEntry, assistantName string) string {
	if context.Off {
		return subagentOffRules
	}
	blocks := []string{subagentRules}
	if status := strings.TrimSpace(context.Status); status != "" {
		blocks = append(blocks, escapeSubagentBoundary(status))
	}
	if files := formatFilesInChat(history, assistantName); files != "" {
		blocks = append(blocks, files)
	}
	if result := strings.TrimSpace(context.Result); result != "" {
		blocks = append(blocks, escapeSubagentBoundary(result))
	}
	return strings.Join(blocks, "\n\n")
}

// formatFilesInChat lists every message in history that carries a file with
// its context ID. The model knows when to delegate but tends to pass the ID
// of the message that asks about a file instead of the one that holds it;
// the list lets it copy the right ID.
func formatFilesInChat(history []HistoryEntry, assistantName string) string {
	var entries []string
	seen := make(map[string]struct{})
	for _, entry := range history {
		if entry.Role == HistorySystem || entry.Sequence == 0 ||
			(entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded) {
			continue
		}
		kind, caption, ok := FileKind(flattenContent(entry.Content))
		if !ok {
			continue
		}
		id := formatCompactContextID(entry.Sequence)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		sender := assistantName
		if entry.Role == HistoryUser {
			sender = "unknown"
			if entry.Sender != nil {
				if name := sanitizeContextDisplayName(entry.Sender.DisplayName); name != "" {
					sender = name
				}
			}
		}
		label := kind
		if caption = sanitizeContextMetadata(caption); caption != "" {
			if len(caption) > 120 {
				caption = strings.ToValidUTF8(caption[:120], "") + "…"
			}
			label += ` "` + caption + `"`
		}
		entries = append(entries, "- 【#"+id+"】 "+label+" 【from "+sender+"】")
	}
	if len(entries) == 0 {
		return ""
	}
	return "<files_in_chat>\n" +
		"The ONLY messages in this chat that carry a file. When `execute_subtask` " +
		`needs a file the user referred to ("the document earlier", "send it ` +
		"back\"), `context_msg_ids` MUST be an ID from THIS list:\n" +
		strings.Join(entries, "\n") +
		"\nNever use the request/mention message's ID or an ID from a `REPLYING " +
		"TO` line. If the file isn't listed, re-read the chat — don't invent an ID.\n" +
		"Stickers are included because they may be the exact media the user wants processed.\n" +
		"</files_in_chat>"
}
