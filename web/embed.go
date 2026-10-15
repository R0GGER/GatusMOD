// Package web holds the static assets and admin templates embedded in the GatusMOD binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static templates
var files embed.FS

func Static() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}

func Templates() fs.FS {
	sub, _ := fs.Sub(files, "templates")
	return sub
}
