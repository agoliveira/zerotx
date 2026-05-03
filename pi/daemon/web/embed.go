// Package web embeds the static web GUI into the daemon binary.
package web

import "embed"

// Embedded files. To add CSS/JS later, append to this directive:
//
//	//go:embed index.html *.css *.js
//
// Patterns must match at least one file; FPC will refuse to compile
// otherwise.
//
//go:embed index.html
//go:embed map/index.html
//go:embed hud/index.html
var FS embed.FS
