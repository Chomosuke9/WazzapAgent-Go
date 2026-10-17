//go:build gui && !android

package main

import "github.com/Chomosuke9/WazzapAgent-Go/internal/platform"

func resolveAppPaths() (platform.Paths, error) { return platform.ResolvePaths() }
