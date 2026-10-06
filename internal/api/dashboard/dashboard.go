// Package dashboard embeds the static dashboard UI into the binary so the
// service ships as a single deployable executable with no separate frontend
// build step.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
