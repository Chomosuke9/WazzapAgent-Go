package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// memoryFake keeps one chat's memories in order.
type memoryFake struct {
	memories []agent.Memory
	next     int
}

func (fake *memoryFake) AddMemory(_ context.Context, _ agent.Key, text string) (agent.Memory, bool, error) {
	for _, memory := range fake.memories {
		if memory.Text == text {
			return memory, false, nil
		}
	}
	if len(fake.memories) >= agent.MaxMemories {
		return agent.Memory{}, false, agent.NewError(agent.ErrorResourceExhausted, "add memory", errors.New("full"))
	}
	fake.next++
	memory := agent.Memory{Code: string(rune('a'+fake.next-1)) + "aaa", Text: text}
	fake.memories = append(fake.memories, memory)
	return memory, true, nil
}

func (fake *memoryFake) Memories(context.Context, agent.Key) ([]agent.Memory, error) {
	return fake.memories, nil
}

func (fake *memoryFake) DeleteMemories(_ context.Context, _ agent.Key, codes []string) ([]agent.Memory, error) {
	var deleted []agent.Memory
	for _, code := range codes {
		for index, memory := range fake.memories {
			if memory.Code == code {
				deleted = append(deleted, memory)
				fake.memories = append(fake.memories[:index], fake.memories[index+1:]...)
				break
			}
		}
	}
	return deleted, nil
}

func runMemoryCommand(t *testing.T, text string, facts command.PermissionFacts, store *memoryFake, mentions ...conversation.MentionBinding) (*recordingText, error) {
	t.Helper()
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatalf("%q is not a registered command", text)
	}
	key := testChatKey(t)
	replies := &recordingText{}
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message: conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID,
			Text: text, FromMe: facts.FromMe, Mentions: mentions},
		Facts:    facts,
		Platform: command.Platform{Text: replies, Memory: store, AssistantName: "Wazzap"},
	})
	return replies, err
}

func TestMemoryAddsListsAndDeletes(t *testing.T) {
	store := &memoryFake{}
	bot := command.PermissionFacts{IsGroup: true, FromMe: true}
	replies, err := runMemoryCommand(t, "/memory add @Budi (a1b2c3) likes cats", bot, store)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "Saved as aaaa") {
		t.Fatalf("add: err=%v replies=%q", err, replies.sent)
	}
	replies, _ = runMemoryCommand(t, "/memory add @Budi (a1b2c3) likes cats", bot, store)
	if len(store.memories) != 1 || !strings.Contains(replies.sent[0], "Already saved as aaaa") {
		t.Fatalf("duplicate add: memories=%v replies=%q", store.memories, replies.sent)
	}
	runMemoryCommand(t, "/memory add the group speaks Indonesian", bot, store)
	runMemoryCommand(t, "/memo remember meetings are on Friday", bot, store)
	replies, _ = runMemoryCommand(t, "/memory", bot, store)
	if !strings.Contains(replies.sent[0], "(3/50)") || !strings.Contains(replies.sent[0], "• baaa: the group speaks Indonesian") {
		t.Fatalf("list: %q", replies.sent)
	}
	replies, err = runMemoryCommand(t, "/memory delete aaaa, CAAA zzzz", bot, store)
	if err != nil || len(store.memories) != 1 || store.memories[0].Code != "baaa" || !strings.Contains(replies.sent[0], "Deleted 2") {
		t.Fatalf("delete: err=%v memories=%v replies=%q", err, store.memories, replies.sent)
	}
	replies, _ = runMemoryCommand(t, "/memory delete zzzz", bot, store)
	if !strings.Contains(replies.sent[0], "No memory has the ID zzzz") {
		t.Fatalf("unknown delete: %q", replies.sent)
	}
}

func TestMemoryRejectsBadInputAndFullMemory(t *testing.T) {
	store := &memoryFake{}
	private := command.PermissionFacts{IsPrivate: true}
	replies, _ := runMemoryCommand(t, "/memory add "+strings.Repeat("x", agent.MaxMemoryRunes+1), private, store)
	if len(store.memories) != 0 || !strings.Contains(replies.sent[0], "1 to 500 characters") {
		t.Fatalf("too long: %q", replies.sent)
	}
	replies, _ = runMemoryCommand(t, "/memory frobnicate", private, store)
	if !strings.Contains(replies.sent[0], "/memory add <text>") {
		t.Fatalf("usage: %q", replies.sent)
	}
	for index := range agent.MaxMemories {
		store.memories = append(store.memories, agent.Memory{Code: string(rune('A' + index)), Text: strings.Repeat("y", index+1)})
	}
	replies, _ = runMemoryCommand(t, "/memory add one more", private, store)
	if !strings.Contains(replies.sent[0], "Memory is full") {
		t.Fatalf("full: %q", replies.sent)
	}
}

func TestMemoryRewritesTypedMentions(t *testing.T) {
	store := &memoryFake{}
	ref, err := identity.ParseSenderRef("a1b2c3")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runMemoryCommand(t, "/memory add @6281234 is the treasurer", command.PermissionFacts{IsGroup: true, IsAdmin: true}, store,
		conversation.MentionBinding{Token: "@6281234", SenderRef: ref})
	if err != nil || len(store.memories) != 1 || store.memories[0].Text != "@member (a1b2c3) is the treasurer" {
		t.Fatalf("err=%v memories=%v", err, store.memories)
	}
}

func TestMemoryNeedsAdminForPeopleInGroups(t *testing.T) {
	_, err := runMemoryCommand(t, "/memory add hi", command.PermissionFacts{IsGroup: true}, &memoryFake{})
	if !errors.Is(err, command.ErrDenied) {
		t.Fatalf("member err = %v, want denied", err)
	}
}

func TestParseMemoryArgs(t *testing.T) {
	tests := []struct{ args, action, rest string }{
		{"", "list", ""}, {"list", "list", ""}, {" show ", "list", ""}, {"list x", "", ""},
		{"add  likes cats ", "add", "likes cats"}, {"add", "", ""}, {"ADD x", "add", "x"},
		{"delete ab12,cd34", "delete", "ab12,cd34"}, {"rm ab12", "delete", "ab12"}, {"delete", "", ""},
		{"whatever", "", ""},
	}
	for _, test := range tests {
		if action, rest := parseMemoryArgs(test.args); action != test.action || rest != test.rest {
			t.Errorf("parseMemoryArgs(%q) = %q, %q; want %q, %q", test.args, action, rest, test.action, test.rest)
		}
	}
}

func TestBotMemoryChangesInOneReplyAreOneUpdateNotice(t *testing.T) {
	store := &memoryFake{memories: []agent.Memory{{Code: "k7p2", Text: "@Budi (a1b2c3) lives in Bandung"}}}
	registry := builtinRegistry(t)
	key := testChatKey(t)
	replies := &recordingText{}
	batch := &command.NoticeBatch{}
	for _, text := range []string{"/memory delete k7p2", "/memory add @Budi (a1b2c3) lives in Jakarta"} {
		request, _, _ := registry.Parse(text)
		err := registry.Dispatch(context.Background(), request, command.Invocation{
			Message:  conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, Text: text, FromMe: true},
			Facts:    command.PermissionFacts{IsGroup: true, FromMe: true},
			Platform: command.Platform{Text: replies, Memory: store, AssistantName: "Wazzap"},
			Notices:  batch,
		})
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	if len(replies.sent) != 0 {
		t.Fatalf("batched commands sent %q", replies.sent)
	}
	if got, want := batch.Text(), "🧠 Memory updated: @Budi (a1b2c3) lives in Jakarta"; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}
