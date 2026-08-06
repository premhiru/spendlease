package providers

import (
	"encoding/json"
	"fmt"
)

// RequestInfo is what the gateway needs to know about a request before it is
// forwarded: which model, how much output was asked for, whether the response
// will stream, and roughly how large the prompt is.
type RequestInfo struct {
	// Model is the requested model identifier, empty if the request did not
	// name one.
	Model string
	// MaxTokens is the caller's output ceiling, zero if unspecified. Strict
	// enforcement rejects zero; observe and best-effort policy use the price
	// book's per-model default.
	MaxTokens int64
	// Stream is true when the caller asked for a streamed response.
	Stream bool
	// PromptChars is the total length of the text fields in the request, used
	// to estimate input tokens. Characters rather than bytes, so multibyte
	// text is not over-counted.
	PromptChars int64
	// RequestBytes is the inspected JSON body size. Reservations use this as
	// a conservative tokenizer-independent input ceiling: a byte-level
	// tokenizer cannot produce more content tokens than input bytes.
	RequestBytes int64
	// NoOutput is true for token-billed calls such as embeddings.
	NoOutput bool
	// UnsupportedBilling explains a request feature whose separate vendor fee
	// is not represented by the token price book.
	UnsupportedBilling string
	// WantsUsage is true when the caller explicitly asked the vendor to
	// report usage. Relevant only for OpenAI-compatible streaming, where
	// usage is opt-in.
	WantsUsage bool
}

// Usage is a token count reported by a vendor.
type Usage struct {
	// InputTokens is the uncached prompt size the vendor charged at the base
	// input rate.
	InputTokens int64
	// CachedInputTokens is the part of the prompt served from a provider cache.
	CachedInputTokens int64
	// CacheWrite5mTokens and CacheWrite1hTokens are Anthropic/OpenAI cache
	// creation tokens. Providers that report a cache write without a TTL use
	// the 5-minute bucket, which is the common/default cache lifetime.
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	// OutputTokens is the completion size the vendor charged for.
	OutputTokens int64
}

// IsZero reports whether no usage has been recorded.
func (u Usage) IsZero() bool {
	return u.InputTokens == 0 && u.CachedInputTokens == 0 &&
		u.CacheWrite5mTokens == 0 && u.CacheWrite1hTokens == 0 &&
		u.OutputTokens == 0
}

// TotalInputTokens returns every input token category. The ledger keeps this
// aggregate for a stable public schema while settlement prices each category
// at its own rate.
func (u Usage) TotalInputTokens() int64 {
	return u.InputTokens + u.CachedInputTokens + u.CacheWrite5mTokens + u.CacheWrite1hTokens
}

// Merge folds another usage report into this one.
//
// Vendors report usage in pieces during a stream: Anthropic sends input
// tokens on message_start and output tokens on message_delta. Later non-zero
// values win, because a running output count is revised upwards as the
// completion grows.
func (u *Usage) Merge(other Usage) {
	if other.InputTokens > 0 {
		u.InputTokens = other.InputTokens
	}
	if other.CachedInputTokens > 0 {
		u.CachedInputTokens = other.CachedInputTokens
	}
	if other.CacheWrite5mTokens > 0 {
		u.CacheWrite5mTokens = other.CacheWrite5mTokens
	}
	if other.CacheWrite1hTokens > 0 {
		u.CacheWrite1hTokens = other.CacheWrite1hTokens
	}
	if other.OutputTokens > 0 {
		u.OutputTokens = other.OutputTokens
	}
}

// OpenAIUsageFrom reads the common OpenAI-compatible usage shape, including
// the cache fields used by OpenAI, xAI, Gemini, Kimi, DeepSeek and Z.AI.
func OpenAIUsageFrom(m map[string]any) (Usage, bool) {
	raw, ok := m["usage"].(map[string]any)
	if !ok {
		return Usage{}, false
	}

	prompt := IntField(raw, "prompt_tokens", "input_tokens")
	cached := IntField(raw, "prompt_cache_hit_tokens", "cached_tokens")
	cacheWrite := IntField(raw, "cache_write_tokens", "cache_creation_tokens")
	if details, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		if n := IntField(details, "cached_tokens"); n > 0 {
			cached = n
		}
		if n := IntField(details, "cache_write_tokens", "cache_creation_tokens"); n > 0 {
			cacheWrite = n
		}
	}
	if details, ok := raw["input_tokens_details"].(map[string]any); ok {
		if n := IntField(details, "cached_tokens"); n > 0 {
			cached = n
		}
		if n := IntField(details, "cache_write_tokens", "cache_creation_tokens"); n > 0 {
			cacheWrite = n
		}
	}

	uncached := IntField(raw, "prompt_cache_miss_tokens")
	if uncached == 0 {
		uncached = prompt - cached - cacheWrite
	}
	if uncached < 0 {
		uncached = 0
	}

	u := Usage{
		InputTokens:        uncached,
		CachedInputTokens:  cached,
		CacheWrite5mTokens: cacheWrite,
		OutputTokens:       IntField(raw, "completion_tokens", "output_tokens"),
	}
	return u, !u.IsZero()
}

// promptTextKeys are the request fields whose text contributes to the prompt.
//
// Walking known keys rather than the whole document avoids counting model
// names, tool schemas and identifiers as prompt text, while staying robust to
// vendors adding fields.
var promptTextKeys = map[string]bool{
	"messages": true,
	"system":   true,
	"prompt":   true,
	"input":    true,
	"content":  true,
	"text":     true,
}

// structuralKeys are fields that sit inside a prompt-bearing structure without
// being prompt text themselves.
//
// Without this, every message's "role" would be counted, adding four to nine
// characters per turn. The error is small and always upward, but it is
// avoidable and it makes short prompts look several times larger than they are.
var structuralKeys = map[string]bool{
	"role":          true,
	"type":          true,
	"name":          true,
	"id":            true,
	"url":           true,
	"image_url":     true,
	"media_type":    true,
	"cache_control": true,
}

// CountPromptChars sums the characters of every string reachable under a
// prompt-bearing key in a decoded request body.
//
// It is deliberately forgiving because this value is used only for marked
// fallback settlement. Authorization uses the conservative request-byte
// ceiling instead.
func CountPromptChars(m map[string]any) int64 {
	return countChars(m, false)
}

func countChars(v any, underPromptKey bool) int64 {
	switch t := v.(type) {
	case string:
		if underPromptKey {
			return int64(len([]rune(t)))
		}
	case []any:
		var n int64
		for _, item := range t {
			n += countChars(item, underPromptKey)
		}
		return n
	case map[string]any:
		var n int64
		for k, item := range t {
			if structuralKeys[k] {
				continue
			}
			n += countChars(item, underPromptKey || promptTextKeys[k])
		}
		return n
	}
	return 0
}

// UnsupportedBillingFeature finds request features that can carry provider
// charges beyond ordinary text tokens. Enforcement must refuse these until a
// matching price unit exists instead of pretending a token budget covers them.
func UnsupportedBillingFeature(m map[string]any) string {
	if containsAnyKey(m, map[string]bool{
		"image_url": true, "input_image": true, "input_audio": true,
		"audio": true, "file": true, "file_id": true,
		"inline_data": true, "input_file": true, "attachments": true,
	}) {
		return "media or file inputs have billing dimensions outside the token price book"
	}
	if containsTypeValue(m, map[string]bool{
		"image": true, "input_image": true, "audio": true, "input_audio": true,
		"document": true, "file": true, "input_file": true,
		"video": true, "input_video": true,
	}) {
		return "media or file inputs have billing dimensions outside the token price book"
	}
	if tools, ok := m["tools"].([]any); ok {
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			kind := StringField(tool, "type")
			if kind != "" && kind != "function" && kind != "custom" {
				return "provider-hosted tools may add charges outside the token price book"
			}
		}
	}
	return ""
}

// UnsupportedBillingModifier rejects an explicit request modifier unless its
// value is known to use the ordinary token rates represented by the price
// book. An omitted or null modifier leaves the provider's account-level
// default in place; spendlease cannot inspect that external configuration.
func UnsupportedBillingModifier(m map[string]any, field string, ordinaryValues ...string) string {
	raw, present := m[field]
	if !present || raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if ok {
		for _, ordinary := range ordinaryValues {
			if value == ordinary {
				return ""
			}
		}
		return fmt.Sprintf("%s %q can select provider pricing outside the token price book", field, value)
	}
	return fmt.Sprintf("%s has an unreviewed value that may select provider pricing outside the token price book", field)
}

func containsTypeValue(v any, types map[string]bool) bool {
	switch value := v.(type) {
	case []any:
		for _, item := range value {
			if containsTypeValue(item, types) {
				return true
			}
		}
	case map[string]any:
		if kind, ok := value["type"].(string); ok && types[kind] {
			return true
		}
		for _, item := range value {
			if containsTypeValue(item, types) {
				return true
			}
		}
	}
	return false
}

func containsAnyKey(v any, keys map[string]bool) bool {
	switch value := v.(type) {
	case []any:
		for _, item := range value {
			if containsAnyKey(item, keys) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if keys[key] || containsAnyKey(item, keys) {
				return true
			}
		}
	}
	return false
}

// DecodeBody parses a request or response body, returning nil when it is not
// a JSON object.
//
// A body that will not parse is not a parser error. The gateway decides whether
// to forward it unmetered in observe mode or reject it in enforce mode.
func DecodeBody(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	return m
}

// StringField reads a string field from a decoded body.
func StringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// IntField reads the first positive numeric field among the given keys.
//
// JSON numbers decode as float64; token counts and ceilings are well inside
// the range where that is exact.
func IntField(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if v, ok := m[key].(float64); ok && v > 0 {
			return int64(v)
		}
	}
	return 0
}

// BoolField reads a boolean field from a decoded body.
func BoolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// UsageFrom reads a usage object using the given field names, so both vendors
// share one implementation. Nested usage objects are searched too, which is
// what makes Anthropic's message_start event readable.
func UsageFrom(m map[string]any, inputKeys, outputKeys []string) (Usage, bool) {
	if raw, ok := m["usage"].(map[string]any); ok {
		u := Usage{
			InputTokens:  IntField(raw, inputKeys...),
			OutputTokens: IntField(raw, outputKeys...),
		}
		if !u.IsZero() {
			return u, true
		}
	}

	// Anthropic nests the initial usage under "message" on message_start.
	if nested, ok := m["message"].(map[string]any); ok {
		return UsageFrom(nested, inputKeys, outputKeys)
	}
	return Usage{}, false
}
