// Package spendlease embeds the default price book.
//
// This file exists at the module root for one reason: Go's embed directive
// cannot reach outside its own package directory, and the price book has to
// live at /pricing where contributors expect to find it. Moving the YAML
// under internal/pricing/ would make the single most contributable part of
// the project the hardest one to find.
package spendlease

import (
	"embed"
	"io/fs"
)

// priceBook holds the YAML price files, compiled into the binary so a
// container with nothing mounted still knows what things cost.
//
//go:embed pricing/*.yaml
var priceBook embed.FS

// PriceBookDir is the directory name inside PriceBookFS, and the conventional
// path on disk when overriding the embedded copy.
const PriceBookDir = "pricing"

// PriceBookFS returns the price book compiled into this binary.
//
// It is the default source. An operator who needs a price the shipped book
// does not have can point the gateway at a directory instead and get hot
// reload, without waiting for a release.
func PriceBookFS() fs.FS { return priceBook }
