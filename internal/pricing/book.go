// Package pricing loads the versioned price book and turns token counts into
// calculated costs.
//
// The price book is data, not code: plain YAML files under /pricing, each
// stamped with the date its prices take effect. Vendor prices change
// constantly and nobody else maintains a normalised cost table across every
// vendor an agent might call, which makes this the most valuable and the most
// contributable part of the project.
//
// Nothing here reads a float. Prices arrive as decimal text and are parsed
// exactly into money.Nanos, because a budget system that disagrees with an
// invoice about the third decimal place is worse than no budget system. See
// ADR-0003.
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/premhiru/spendlease/internal/money"
)

// SupportedVersion is the newest price-book schema this code understands.
// Older supported versions remain readable so dated price history does not
// need to be rewritten.
const SupportedVersion = 2

// Errors returned by this package.
var (
	// ErrNoPrices means the price book contained no usable files.
	ErrNoPrices = errors.New("pricing: no price files loaded")
	// ErrUnknownModel means neither the price book nor a fallback could
	// price a model.
	ErrUnknownModel = errors.New("pricing: unknown model and no fallback configured")
)

// Rate is a price in USD per one million tokens.
//
// It exists as a distinct type solely to control YAML decoding: the default
// decoder would turn `2.50` into a float64, silently reintroducing the
// imprecision this project spends real effort avoiding.
type Rate money.Nanos

// UnmarshalYAML decodes a rate from the raw scalar text, never through a
// float. `2.50`, `"2.50"` and `$2.50` all decode identically and exactly.
func (r *Rate) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("pricing: expected a number for a rate, got %s at line %d",
			kindName(node.Kind), node.Line)
	}
	n, err := money.ParseUSD(node.Value)
	if err != nil {
		return fmt.Errorf("pricing: rate %q at line %d: %w", node.Value, node.Line, err)
	}
	if n < 0 {
		return fmt.Errorf("pricing: rate %q at line %d is negative", node.Value, node.Line)
	}
	*r = Rate(n)
	return nil
}

// Nanos returns the rate as a money amount per one million tokens.
func (r Rate) Nanos() money.Nanos { return money.Nanos(r) }

// Model is one model's entry in the price book.
type Model struct {
	// InputPer1M is the price of one million input tokens.
	InputPer1M Rate `yaml:"input_per_1m"`
	// OutputPer1M is the price of one million output tokens.
	OutputPer1M Rate `yaml:"output_per_1m"`
	// CachedInputPer1M is the discounted cache-hit rate. Nil means cached
	// tokens are billed at the ordinary input rate.
	CachedInputPer1M *Rate `yaml:"cached_input_per_1m"`
	// CacheWrite5mPer1M and CacheWrite1hPer1M are cache creation rates. The
	// 5-minute field is also used by vendors that expose one undifferentiated
	// cache-write category.
	CacheWrite5mPer1M *Rate `yaml:"cache_write_5m_per_1m"`
	CacheWrite1hPer1M *Rate `yaml:"cache_write_1h_per_1m"`
	// LongContextThreshold selects the long-context rates for the entire
	// request once total input tokens exceed this value.
	LongContextThreshold int64 `yaml:"long_context_threshold"`
	LongInputPer1M       *Rate `yaml:"long_input_per_1m"`
	LongCachedInputPer1M *Rate `yaml:"long_cached_input_per_1m"`
	LongCacheWritePer1M  *Rate `yaml:"long_cache_write_per_1m"`
	LongOutputPer1M      *Rate `yaml:"long_output_per_1m"`
	// Free explicitly marks a zero-priced model. Without it, missing or zero
	// base rates are rejected as a likely price-book mistake.
	Free bool `yaml:"free"`
	// DefaultMaxTokens is the output ceiling assumed when a request does not
	// specify one.
	//
	// This is a reservation default, not the model's capability. Reserving a
	// model's full output window would hold most of a budget for a request
	// that will almost certainly use a fraction of it, and rejecting every
	// subsequent call is worse than reserving slightly too little.
	DefaultMaxTokens int64 `yaml:"default_max_tokens"`
	// Aliases are other identifiers that resolve to this entry, for vendors
	// that publish both a dated id and a convenience alias.
	Aliases []string `yaml:"aliases"`
	// Note records anything a reviewer should know, such as a deprecation.
	Note string `yaml:"note"`
}

// Provider groups one vendor's models.
type Provider struct {
	// Source is the vendor's public pricing page. Required: a price without a
	// source cannot be reviewed, which is the one hard rule for price book
	// contributions.
	Source string           `yaml:"source"`
	Models map[string]Model `yaml:"models"`
}

// File is one price book document.
type File struct {
	// Version must be between 1 and SupportedVersion.
	Version int `yaml:"version"`
	// Effective is the date these prices take effect. Prices are never
	// overwritten; a change is a new file with a later effective date.
	Effective time.Time `yaml:"effective"`
	// Providers maps a provider name to its models.
	Providers map[string]Provider `yaml:"providers"`

	// name is the file it was read from, for error messages.
	name string
	// digest is the SHA-256 of the source document, used to identify the exact
	// active price-book revision without exposing file contents.
	digest string
}

// Metadata identifies the active price-book snapshot for operators.
type Metadata struct {
	Revision        string
	LoadedAt        time.Time
	LatestEffective time.Time
	Providers       int
	Models          int
}

// Price is a resolved price for one model at one moment.
type Price struct {
	// Provider and Model identify what was priced.
	Provider string
	Model    string
	// InputPer1M and OutputPer1M are prices per one million tokens.
	InputPer1M           money.Nanos
	OutputPer1M          money.Nanos
	CachedInputPer1M     money.Nanos
	CacheWrite5mPer1M    money.Nanos
	CacheWrite1hPer1M    money.Nanos
	LongContextThreshold int64
	LongInputPer1M       money.Nanos
	LongCachedInputPer1M money.Nanos
	LongCacheWritePer1M  money.Nanos
	LongOutputPer1M      money.Nanos
	// Free is true only for a model explicitly marked as zero-priced.
	Free bool
	// DefaultMaxTokens is the output ceiling to assume when a request does
	// not specify one.
	DefaultMaxTokens int64
	// Effective is the date this price took effect.
	Effective time.Time
	// Source is the vendor pricing page this came from.
	Source string
	// Estimated is true when this price did not come from the book and a
	// fallback rate was applied instead. Ledger entries built from an
	// estimated price must carry the same flag: an unknown model must never
	// look like a confidently priced one.
	Estimated bool
}

// Fallback is the rate applied to a model the price book does not know.
//
// Unknown models must never silently cost zero. A retry loop against an
// unrecognised model would otherwise be invisible in exactly the situation
// this product exists to catch.
type Fallback struct {
	// InputPer1M and OutputPer1M are the assumed rates per million tokens.
	InputPer1M  money.Nanos
	OutputPer1M money.Nanos
	// DefaultMaxTokens is the assumed output ceiling.
	DefaultMaxTokens int64
}

// DefaultFallback is a fixed conservative rate for unknown models. It avoids
// treating an unrecognised model as free, but it is not guaranteed to exceed
// every vendor price. Callers can identify the estimate on the ledger entry
// and the gateway logs a warning when the fallback is selected.
var DefaultFallback = Fallback{
	InputPer1M:       money.MustParseUSD("15.00"),
	OutputPer1M:      money.MustParseUSD("75.00"),
	DefaultMaxTokens: 4096,
}

// Options configures loading.
type Options struct {
	// Fallback prices models the book does not contain. If zero,
	// DefaultFallback is used. Set DisableFallback to refuse instead.
	Fallback Fallback
	// DisableFallback makes unknown models an error rather than an estimate.
	DisableFallback bool
	// Warn is called once per unknown model, so an operator finds out that
	// something is being guessed at. Optional.
	Warn func(provider, model string)
}

// Book is a loaded price book. It is safe for concurrent use, and Reload
// swaps the contents atomically so lookups never see a half-loaded book.
type Book struct {
	snapshot atomic.Pointer[snapshot]
	opts     Options

	// loadedFrom remembers where to reload from.
	fsys fs.FS
	dir  string

	// warned tracks which unknown models have already been reported, so a
	// retry loop against a bad model name does not flood the log.
	warned atomic.Pointer[map[string]bool]
}

// snapshot is one immutable view of the price book.
type snapshot struct {
	// files sorted by effective date, oldest first.
	files []File
	// loadedAt is when this snapshot was read.
	loadedAt time.Time
}

// Load reads every .yaml file in dir and returns a book.
//
// Files are independent documents; a later effective date supersedes an
// earlier one for the models it mentions, and leaves every other model alone.
// That is what lets a scheduled price change ship as a new file rather than an
// edit to an existing one, keeping historical prices explainable.
func Load(fsys fs.FS, dir string, opts Options) (*Book, error) {
	if opts.Fallback == (Fallback{}) {
		opts.Fallback = DefaultFallback
	}

	b := &Book{opts: opts, fsys: fsys, dir: dir}
	if err := b.Reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// Reload re-reads the price book from disk and swaps it in atomically.
//
// A failed reload leaves the previous prices in place: continuing to charge
// yesterday's rates is far better than a gateway that stops pricing because
// somebody committed malformed YAML.
func (b *Book) Reload() error {
	files, err := readDir(b.fsys, b.dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%w: %s", ErrNoPrices, b.dir)
	}

	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Effective.Equal(files[j].Effective) {
			return files[i].name < files[j].name
		}
		return files[i].Effective.Before(files[j].Effective)
	})

	b.snapshot.Store(&snapshot{files: files, loadedAt: time.Now()})
	empty := map[string]bool{}
	b.warned.Store(&empty)
	return nil
}

// readDir parses every .yaml document in dir.
func readDir(fsys fs.FS, dir string) ([]File, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("pricing: reading %s: %w", dir, err)
	}

	var out []File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}

		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("pricing: reading %s: %w", name, err)
		}

		var f File
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("pricing: parsing %s: %w", name, err)
		}
		f.name = name
		digest := sha256.Sum256(raw)
		f.digest = hex.EncodeToString(digest[:])

		if err := f.validate(); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// validate rejects a file that would price things wrongly, rather than
// loading it and producing quietly incorrect costs.
func (f File) validate() error {
	if f.Version < 1 || f.Version > SupportedVersion {
		return fmt.Errorf("pricing: %s declares version %d, but this build understands versions 1 through %d",
			f.name, f.Version, SupportedVersion)
	}
	if f.Effective.IsZero() {
		return fmt.Errorf("pricing: %s has no effective date", f.name)
	}
	if len(f.Providers) == 0 {
		return fmt.Errorf("pricing: %s lists no providers", f.name)
	}

	for provider, p := range f.Providers {
		if strings.TrimSpace(p.Source) == "" {
			return fmt.Errorf("pricing: %s: provider %q has no source URL; "+
				"a price without a link to the vendor's pricing page cannot be reviewed",
				f.name, provider)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("pricing: %s: provider %q lists no models", f.name, provider)
		}
		for name, m := range p.Models {
			if m.Free {
				if m.InputPer1M.Nanos() != 0 || m.OutputPer1M.Nanos() != 0 ||
					nonZeroRate(m.CachedInputPer1M) || nonZeroRate(m.CacheWrite5mPer1M) ||
					nonZeroRate(m.CacheWrite1hPer1M) || m.LongContextThreshold != 0 ||
					nonZeroRate(m.LongInputPer1M) || nonZeroRate(m.LongCachedInputPer1M) ||
					nonZeroRate(m.LongCacheWritePer1M) || nonZeroRate(m.LongOutputPer1M) {
					return fmt.Errorf("pricing: %s: %s/%s is marked free but has a non-zero rate or long-context threshold",
						f.name, provider, name)
				}
			} else if m.InputPer1M.Nanos() <= 0 || m.OutputPer1M.Nanos() <= 0 {
				return fmt.Errorf("pricing: %s: %s/%s has a missing or zero base rate; mark an intentionally zero-priced model free",
					f.name, provider, name)
			}
			if m.DefaultMaxTokens <= 0 {
				return fmt.Errorf("pricing: %s: %s/%s has default_max_tokens %d; "+
					"it must be positive, because a request without max_tokens must never reserve unbounded",
					f.name, provider, name, m.DefaultMaxTokens)
			}
			if f.Version == 1 && (m.Free || m.CachedInputPer1M != nil || m.CacheWrite5mPer1M != nil ||
				m.CacheWrite1hPer1M != nil || m.LongContextThreshold != 0 ||
				m.LongInputPer1M != nil || m.LongCachedInputPer1M != nil ||
				m.LongCacheWritePer1M != nil || m.LongOutputPer1M != nil) {
				return fmt.Errorf("pricing: %s: %s/%s uses cache or long-context fields that require version 2",
					f.name, provider, name)
			}
			longFields := []*Rate{m.LongInputPer1M, m.LongCachedInputPer1M, m.LongOutputPer1M}
			longCount := 0
			for _, rate := range longFields {
				if rate != nil {
					longCount++
				}
			}
			if m.LongContextThreshold > 0 && longCount != len(longFields) {
				return fmt.Errorf("pricing: %s: %s/%s has incomplete long-context rates", f.name, provider, name)
			}
			if m.LongContextThreshold == 0 && (longCount > 0 || m.LongCacheWritePer1M != nil) {
				return fmt.Errorf("pricing: %s: %s/%s has long-context rates without a threshold", f.name, provider, name)
			}
		}
	}
	return nil
}

// Lookup resolves a model's price as of the given instant.
//
// It searches from the newest applicable file backwards, so a scheduled price
// change takes effect on its date without disturbing anything else. Aliases
// are resolved after exact identifiers.
//
// The bool reports whether the price came from the book. A false result with
// a usable Price means the fallback was applied and Estimated is set.
func (b *Book) Lookup(provider, model string, at time.Time) (Price, bool) {
	snap := b.snapshot.Load()
	if snap == nil {
		return b.fallbackPrice(provider, model), false
	}

	for i := len(snap.files) - 1; i >= 0; i-- {
		f := snap.files[i]
		if f.Effective.After(at) {
			continue
		}
		p, ok := f.Providers[provider]
		if !ok {
			continue
		}
		if m, ok := p.Models[model]; ok {
			return price(provider, model, m, f, p.Source), true
		}
		for name, m := range p.Models {
			for _, alias := range m.Aliases {
				if alias == model {
					return price(provider, name, m, f, p.Source), true
				}
			}
		}
	}

	b.warnUnknown(provider, model)
	return b.fallbackPrice(provider, model), false
}

// price builds a resolved Price from a book entry.
func price(provider, model string, m Model, f File, source string) Price {
	cached := rateOr(m.CachedInputPer1M, m.InputPer1M.Nanos())
	write5m := rateOr(m.CacheWrite5mPer1M, m.InputPer1M.Nanos())
	write1h := rateOr(m.CacheWrite1hPer1M, write5m)
	return Price{
		Provider:             provider,
		Model:                model,
		InputPer1M:           m.InputPer1M.Nanos(),
		OutputPer1M:          m.OutputPer1M.Nanos(),
		CachedInputPer1M:     cached,
		CacheWrite5mPer1M:    write5m,
		CacheWrite1hPer1M:    write1h,
		LongContextThreshold: m.LongContextThreshold,
		LongInputPer1M:       rateOr(m.LongInputPer1M, m.InputPer1M.Nanos()),
		LongCachedInputPer1M: rateOr(m.LongCachedInputPer1M, cached),
		LongCacheWritePer1M:  rateOr(m.LongCacheWritePer1M, write5m),
		LongOutputPer1M:      rateOr(m.LongOutputPer1M, m.OutputPer1M.Nanos()),
		Free:                 m.Free,
		DefaultMaxTokens:     m.DefaultMaxTokens,
		Effective:            f.Effective,
		Source:               source,
	}
}

// fallbackPrice returns the estimated price for an unknown model.
func (b *Book) fallbackPrice(provider, model string) Price {
	return Price{
		Provider:          provider,
		Model:             model,
		InputPer1M:        b.opts.Fallback.InputPer1M,
		OutputPer1M:       b.opts.Fallback.OutputPer1M,
		CachedInputPer1M:  b.opts.Fallback.InputPer1M,
		CacheWrite5mPer1M: b.opts.Fallback.InputPer1M,
		CacheWrite1hPer1M: b.opts.Fallback.InputPer1M,
		DefaultMaxTokens:  b.opts.Fallback.DefaultMaxTokens,
		Estimated:         true,
	}
}

func rateOr(r *Rate, fallback money.Nanos) money.Nanos {
	if r == nil {
		return fallback
	}
	return r.Nanos()
}

func nonZeroRate(r *Rate) bool { return r != nil && r.Nanos() != 0 }

// warnUnknown reports an unpriced model once.
func (b *Book) warnUnknown(provider, model string) {
	if b.opts.Warn == nil {
		return
	}
	seen := b.warned.Load()
	if seen == nil {
		return
	}
	key := provider + "/" + model
	if (*seen)[key] {
		return
	}
	// A benign race here can warn twice, which is far better than holding a
	// lock on the request path to guarantee exactly once.
	next := make(map[string]bool, len(*seen)+1)
	for k, v := range *seen {
		next[k] = v
	}
	next[key] = true
	b.warned.Store(&next)

	b.opts.Warn(provider, model)
}

// Price resolves a model's price, returning an error instead of an estimate
// when fallbacks are disabled.
func (b *Book) Price(provider, model string, at time.Time) (Price, error) {
	p, known := b.Lookup(provider, model, at)
	if !known && b.opts.DisableFallback {
		return Price{}, fmt.Errorf("%w: %s/%s", ErrUnknownModel, provider, model)
	}
	return p, nil
}

// Models lists every model priced for a provider as of the given instant,
// sorted. It is what the price book documentation and the contribution
// tooling read.
func (b *Book) Models(provider string, at time.Time) []string {
	snap := b.snapshot.Load()
	if snap == nil {
		return nil
	}

	seen := map[string]bool{}
	for _, f := range snap.files {
		if f.Effective.After(at) {
			continue
		}
		if p, ok := f.Providers[provider]; ok {
			for name := range p.Models {
				seen[name] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Providers lists every provider in the book, sorted.
func (b *Book) Providers() []string {
	snap := b.snapshot.Load()
	if snap == nil {
		return nil
	}

	seen := map[string]bool{}
	for _, f := range snap.files {
		for name := range f.Providers {
			seen[name] = true
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// LoadedAt reports when the current snapshot was read, for the dashboard and
// for confirming a hot reload actually happened.
func (b *Book) LoadedAt() time.Time {
	if snap := b.snapshot.Load(); snap != nil {
		return snap.loadedAt
	}
	return time.Time{}
}

// Metadata returns a stable digest and freshness summary for prices active at
// the supplied instant. Future-dated files do not change today's revision.
func (b *Book) Metadata(at time.Time) Metadata {
	snap := b.snapshot.Load()
	if snap == nil {
		return Metadata{}
	}
	h := sha256.New()
	active := map[string]map[string]bool{}
	for _, file := range snap.files {
		if file.Effective.After(at) {
			continue
		}
		_, _ = h.Write([]byte(file.name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(file.digest))
		_, _ = h.Write([]byte{0})
		for provider, prices := range file.Providers {
			if active[provider] == nil {
				active[provider] = map[string]bool{}
			}
			for model := range prices.Models {
				active[provider][model] = true
			}
		}
	}
	metadata := Metadata{
		Revision: hex.EncodeToString(h.Sum(nil)), LoadedAt: snap.loadedAt,
		Providers: len(active),
	}
	for _, file := range snap.files {
		if !file.Effective.After(at) && file.Effective.After(metadata.LatestEffective) {
			metadata.LatestEffective = file.Effective
		}
	}
	for _, models := range active {
		metadata.Models += len(models)
	}
	return metadata
}

// kindName renders a YAML node kind for an error message.
func kindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a map"
	case yaml.AliasNode:
		return "an alias"
	case yaml.DocumentNode:
		return "a document"
	default:
		return "an unexpected node"
	}
}
