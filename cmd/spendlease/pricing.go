package main

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	spendlease "github.com/premhiru/spendlease"
	"github.com/premhiru/spendlease/internal/pricing"
)

// loadPriceBook loads prices, from a directory if one was given and from the
// copy embedded in this binary otherwise.
//
// Importing the embedded book here is load-bearing rather than cosmetic: Go's
// linker discards packages nothing references, so a binary that never
// mentioned it would ship with no prices at all and silently estimate
// everything. There is a test asserting the shipped binary contains the book.
func loadPriceBook(dir string, logger *slog.Logger) (*pricing.Book, error) {
	opts := pricing.Options{
		Warn: func(provider, model string) {
			// Loud, once per unknown model. An unpriced model still costs
			// money; it must not be invisible.
			logger.Warn("no price for model, using the fallback rate",
				"provider", provider, "model", model,
				"hint", "add it to the price book: https://premhiru.github.io/spendlease/pricing-book/")
		},
	}

	var (
		fsys   fs.FS
		root   string
		source string
	)
	if dir != "" {
		fsys, root, source = os.DirFS(dir), ".", dir
	} else {
		fsys, root, source = spendlease.PriceBookFS(), spendlease.PriceBookDir, "embedded"
	}

	book, err := pricing.Load(fsys, root, opts)
	if err != nil {
		return nil, fmt.Errorf("loading the price book from %s: %w", source, err)
	}

	logger.Info("price book loaded",
		"source", source,
		"providers", len(book.Providers()),
		"models", countModels(book))
	return book, nil
}

// countModels totals the models priced right now, across every provider.
func countModels(b *pricing.Book) int {
	now := time.Now()
	total := 0
	for _, p := range b.Providers() {
		total += len(b.Models(p, now))
	}
	return total
}

// modelBreakdown explains the dashboard's model-ID count without turning the
// compact header into another table.
func modelBreakdown(b *pricing.Book) string {
	now := time.Now()
	parts := make([]string, 0, len(b.Providers()))
	for _, provider := range b.Providers() {
		parts = append(parts, fmt.Sprintf("%s %d", provider, len(b.Models(provider, now))))
	}
	return strings.Join(parts, " · ")
}

// summarisePriceBook prints what the gateway knows how to price, so an
// operator can see at a glance whether the model they care about is covered.
func summarisePriceBook(w io.Writer, b *pricing.Book) {
	now := time.Now()
	fmt.Fprintf(w, "Pricing %d models:", countModels(b))
	for _, p := range b.Providers() {
		fmt.Fprintf(w, " %s (%d)", p, len(b.Models(p, now)))
	}
	fmt.Fprintln(w)
}
