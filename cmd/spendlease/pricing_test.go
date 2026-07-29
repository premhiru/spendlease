package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
)

func TestLoadPriceBookEmbedded(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	book, err := loadPriceBook("", logger)
	if err != nil {
		t.Fatalf("loadPriceBook: %v", err)
	}

	if got := countModels(book); got < 20 {
		t.Errorf("the embedded book prices %d models, which looks truncated", got)
	}

	p, known := book.Lookup("openai", "gpt-4o", time.Now())
	if !known {
		t.Fatal("gpt-4o is not priced by the embedded book")
	}
	if p.InputPer1M != money.MustParseUSD("2.50") {
		t.Errorf("gpt-4o input = %s, want 2.50", p.InputPer1M)
	}
}

func TestLoadPriceBookFromDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	body := `
version: 1
effective: 2026-01-01
providers:
  openai:
    source: https://example.invalid
    models:
      only-model:
        input_per_1m: 1.00
        output_per_1m: 2.00
        default_max_tokens: 100
`
	if err := os.WriteFile(filepath.Join(dir, "prices.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	book, err := loadPriceBook(dir, logger)
	if err != nil {
		t.Fatalf("loadPriceBook: %v", err)
	}

	// The directory replaces the embedded book rather than merging with it.
	if got := countModels(book); got != 1 {
		t.Errorf("an overriding directory produced %d models, want only its own 1", got)
	}
	if _, known := book.Lookup("openai", "only-model", time.Now()); !known {
		t.Error("the overriding book's model is missing")
	}
}

func TestLoadPriceBookRejectsBadDirectory(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if _, err := loadPriceBook(filepath.Join(t.TempDir(), "nope"), logger); err == nil {
		t.Error("loadPriceBook accepted a directory that does not exist")
	}
}

// TestUnknownModelWarningIsActionable checks the log line an operator sees
// when a model is being guessed at. It has to name the model and say what to
// do, or it is just noise.
func TestUnknownModelWarningIsActionable(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	book, err := loadPriceBook("", logger)
	if err != nil {
		t.Fatalf("loadPriceBook: %v", err)
	}
	book.Lookup("openai", "gpt-99-imaginary", time.Now())

	got := logs.String()
	for _, want := range []string{"gpt-99-imaginary", "fallback", "pricing-book"} {
		if !strings.Contains(got, want) {
			t.Errorf("the unknown-model warning %q does not contain %q", got, want)
		}
	}
}

// TestShippedBinaryContainsThePriceBook is the regression test for a real
// defect: the embed compiled to nothing.
//
// Go's linker discards packages nothing references. cmd/spendlease did not
// import the embedded book, so the shipped binary contained no prices at all
// and would have estimated every request — while the package-level test that
// loaded the embed directly passed the whole time.
//
// This test builds the actual command and looks for price data in the
// resulting bytes, which is the only way to catch that class of mistake.
func TestShippedBinaryContainsThePriceBook(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	t.Parallel()

	exe := filepath.Join(t.TempDir(), "spendlease")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}

	// Built the way a release is: stripped, trimmed, no cgo.
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", exe, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the command: %v\n%s", err, out)
	}

	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("reading the built binary: %v", err)
	}

	// Distinctive strings that can only come from the embedded YAML.
	for _, want := range []string{
		"input_per_1m",
		"claude-fable-5",
		"developers.openai.com/api/docs/pricing",
	} {
		if !bytes.Contains(binary, []byte(want)) {
			t.Errorf("the built binary does not contain %q; the price book is not embedded in the shipped artefact", want)
		}
	}
}
