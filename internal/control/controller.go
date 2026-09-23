package control

import (
	"errors"
	"sync"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// Controller owns settings mutations. The mutex deliberately covers only
// controller mutations; reads do not wait for a slow storage operation.
type Controller struct {
	repository SettingsRepository
	mutate     sync.Mutex
}

// NewController creates the settings controller. It does not open a database
// or perform any other I/O.
func NewController(repository SettingsRepository) (*Controller, error) {
	if repository == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create settings controller", errors.New("settings repository is required"))
	}
	return &Controller{repository: repository}, nil
}

// NewSettingsController is the explicit constructor name for composition code.
func NewSettingsController(repository SettingsRepository) (*Controller, error) {
	return NewController(repository)
}
