//go:build gui

package main

import (
	"context"
	"log"
	"runtime"

	"github.com/Chomosuke9/WazzapAgent-Go/frontend"
	apphost "github.com/Chomosuke9/WazzapAgent-Go/internal/host"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/ui"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// version is overridden by the release build when a version is available.
var version = "dev"

func main() {
	ctx := context.Background()
	paths, err := resolveAppPaths()
	if err != nil {
		log.Fatal(err)
	}
	host, err := apphost.Open(ctx, paths, version)
	if err != nil {
		log.Fatal(err)
	}
	app := application.New(application.Options{
		Name:        ui.AppName,
		Description: "WazzapAgent desktop application",
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(frontend.Assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	cleanup := func() {
		if err := host.Close(); err != nil {
			log.Printf("stop application: %v", err)
		}
	}
	defer cleanup()
	app.RegisterService(application.NewService(host.Service))
	host.Logger.Info("desktop application started", "platform", runtime.GOOS)
	app.OnShutdown(cleanup)
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     ui.AppName,
		URL:       "/",
		Width:     1160,
		Height:    800,
		MinWidth:  360,
		MinHeight: 500,
	})
	host.StartOnLaunch(ctx)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
