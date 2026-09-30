// Package commands holds the slash commands, one file per command. A command
// file registers itself from init; adding a command means adding a file.
package commands

import "github.com/Chomosuke9/DiscordAgent-Go/internal/command"

var registered []command.Command

func register(cmd command.Command) { registered = append(registered, cmd) }

// All returns every command registered by the files in this package.
func All() []command.Command { return append([]command.Command(nil), registered...) }
