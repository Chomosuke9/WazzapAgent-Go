//go:build web

package frontend

import "embed"

// WebAssets is the production browser bundle built with npm run build:web.
//
//go:embed all:web-dist
var WebAssets embed.FS
