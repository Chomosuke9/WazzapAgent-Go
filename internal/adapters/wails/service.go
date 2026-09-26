//go:build gui

package wails

import (
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/ui"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const AppName = ui.AppName

// AppService exposes the stable application and settings bindings. Runtime and
// session operations are added only when their controller owns the lifecycle.
type AppService struct {
	// Operations live in ui.AppService and are promoted through embedding, so
	// the generated bindings keep this type's name and method IDs. Only Ping
	// differs: Wails delivers its payload as an app:ping event.
	*ui.AppService
	app *application.App
}

func NewAppService(app *application.App, service *ui.AppService) *AppService {
	return &AppService{AppService: service, app: app}
}

// Ping provides an explicit UI action for checking the Go-to-frontend event path.
func (s *AppService) Ping() error {
	if s.app == nil {
		return errors.New("wails application is not initialized")
	}
	event, err := s.AppService.Ping()
	if err != nil {
		return err
	}
	s.app.Event.Emit("app:ping", event)
	return nil
}
