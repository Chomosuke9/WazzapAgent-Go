package control

import (
	"errors"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
)

// Controller owns settings mutations. Concurrent saves are resolved by the
// repository's revision compare-and-swap, so the controller holds no lock.
type Controller struct {
	repository SettingsRepository
}

// NewController creates the settings controller. It does not open a database
// or perform any other I/O.
func NewController(repository SettingsRepository) (*Controller, error) {
	if repository == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create settings controller", errors.New("settings repository is required"))
	}
	return &Controller{repository: repository}, nil
}
