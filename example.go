// Package gatusmod embeds the example configuration that is written into an empty config directory on first start.
package gatusmod

import (
	"embed"
	"io/fs"
)

//go:embed config/config.yaml config/favicon
var example embed.FS

// Example returns config.yaml and the favicon directory, rooted at the config directory.
func Example() fs.FS {
	sub, _ := fs.Sub(example, "config")
	return sub
}
