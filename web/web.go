// Package web holds the dashboard's embedded templates.
//
// They live here rather than under internal/ because the repository layout
// puts the dashboard's markup where somebody looking for markup would expect
// it, and because Go's embed directive cannot reach outside its own package
// directory.
package web

import "embed"

// Templates holds the dashboard's HTML, compiled into the binary so the
// container has no assets to mount.
//
//go:embed templates/*.html
var Templates embed.FS
