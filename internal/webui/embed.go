package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

func Assets() fs.FS {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("embedded web UI is missing: " + err.Error())
	}
	return assets
}
