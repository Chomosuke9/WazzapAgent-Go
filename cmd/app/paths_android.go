//go:build gui && android

package main

import (
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func resolveAppPaths() (platform.Paths, error) {
	storage := application.Android.StoragePath()
	if storage == "" {
		return platform.Paths{}, errors.New("Android private storage is unavailable")
	}
	return platform.ResolvePathsInSandbox(storage)
}
