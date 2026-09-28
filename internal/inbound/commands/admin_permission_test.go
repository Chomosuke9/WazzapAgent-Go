package commands

import (
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func TestChatConfigCommandsAllowGroupAdminsAndTheBotOnlyForThem(t *testing.T) {
	registry := builtinRegistry(t)
	owner := command.PermissionFacts{IsOwner: true, IsGroup: true}
	admin := command.PermissionFacts{IsGroup: true, IsAdmin: true}
	member := command.PermissionFacts{IsGroup: true}
	private := command.PermissionFacts{IsPrivate: true}
	bot := command.PermissionFacts{IsGroup: true, IsAdmin: true, IsOwner: true, FromMe: true}
	botForAdmin := command.PermissionFacts{IsGroup: true, FromMe: true, RequesterIsAdmin: true}
	botForOwner := command.PermissionFacts{IsPrivate: true, FromMe: true, RequesterIsOwner: true}
	botForAdminInPrivate := command.PermissionFacts{IsPrivate: true, FromMe: true, RequesterIsAdmin: true}
	tests := []struct {
		command string
		facts   command.PermissionFacts
		want    bool
	}{
		{"/prompt", owner, true}, {"/prompt", admin, true}, {"/prompt", member, false}, {"/prompt", private, false}, {"/prompt", bot, false},
		{"/reset", owner, true}, {"/reset", admin, true}, {"/reset", member, false}, {"/reset", private, false}, {"/reset", bot, false},
		{"/permission", owner, true}, {"/permission", admin, true}, {"/permission", member, false}, {"/permission", private, false}, {"/permission", bot, false},
		{"/prompt", botForAdmin, true}, {"/prompt", botForOwner, true}, {"/prompt", botForAdminInPrivate, false},
		{"/permission", botForAdmin, true}, {"/permission", botForOwner, true},
		{"/reset", botForAdmin, false}, {"/reset", botForOwner, false},
		{"/trigger", botForAdmin, true}, {"/trigger", botForOwner, false}, {"/trigger", bot, false},
		{"/trigger", command.PermissionFacts{IsGroup: true, FromMe: true, RequesterIsOwner: true}, true},
	}
	for _, test := range tests {
		_, cmd, recognized := registry.Parse(test.command)
		if !recognized {
			t.Fatalf("%s is not registered", test.command)
		}
		got, err := command.EvaluatePermission(cmd.Permission, test.facts)
		if err != nil || got != test.want {
			t.Errorf("%s with %+v = %v, err=%v; want %v", test.command, test.facts, got, err, test.want)
		}
	}
}
