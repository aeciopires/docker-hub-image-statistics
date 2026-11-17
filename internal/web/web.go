// Package web embeds the self-contained user interface (HTML, CSS, JS, fonts
// and favicon). Nothing is downloaded from the Internet at runtime.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var content embed.FS

// FS returns the static files rooted at the static directory.
func FS() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err) // impossible: the directory is embedded at build time
	}
	return sub
}
