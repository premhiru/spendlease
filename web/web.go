// Package web holds the dashboard's embedded templates.
//
// They live here rather than under internal/ because the repository layout
// puts the dashboard's markup where somebody looking for markup would expect
// it, and because Go's embed directive cannot reach outside its own package
// directory.
package web

import (
	"embed"
	"io/fs"
)

// Templates holds the dashboard's HTML, compiled into the binary so the
// container has no assets to mount.
//
//go:embed templates/*.html
var Templates embed.FS

// staticFiles holds the stylesheet and htmx.
//
// Both are served from the binary rather than a CDN. A self-hosted gateway
// whose dashboard goes blank without internet access is not really
// self-hosted, and an air-gapped deployment is exactly the kind that cares
// most about where its money is going.
//
// htmx is vendored under its Zero-Clause BSD licence.
//
//go:embed static/*.css static/*.js
var staticFiles embed.FS

// Static returns the dashboard's assets, rooted so paths are "dashboard.css"
// rather than "static/dashboard.css".
func Static() fs.FS {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// Unreachable: the directory is embedded above and this only fails on
		// a malformed path.
		panic("web: embedded static assets are missing: " + err.Error())
	}
	return sub
}
