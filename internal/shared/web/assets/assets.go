// Package assets serves the compiled Tailwind stylesheet and vendored
// HTMX script from the binary via go:embed, so the app has no runtime
// filesystem dependency for its static files.
package assets

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed css/output.css js/htmx.min.js
var files embed.FS

// Handler serves the embedded assets. Mount it at /assets.
func Handler() http.Handler {
	sub, err := fs.Sub(files, ".")
	if err != nil {
		panic(err)
	}

	return http.StripPrefix(
		"/assets/",
		http.FileServer(http.FS(sub)),
	)
}
