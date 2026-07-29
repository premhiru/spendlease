package providers

import (
	"encoding/json"
)

// RequestInfo is what the gateway needs to know about a request before it is
// forwarded: which model, how much output was asked for, whether the response
// will stream, and roughly how large the prompt is.
type RequestInfo struct {
	// Model is the requested model identifier, empty if the request did not
	// name one.
	Model string
	// MaxTokens is the caller's output ceiling, zero if unspecified. Zero
	// means the price book's per-model default applies.
	MaxTokens int64
	// Stream is true when the caller asked for a streamed response.
	Stream bool
	// PromptChars is the total length of the text fields in the request, used
	// to estimate input tokens. Characters rather than bytes, so multibyte
	// text is not over-counted.
	PromptChars int64
	// WantsUsage is true when the caller explicitly asked the vendor to
	// report usage. Relevant only for OpenAI-compatible streaming, where
	// usage is opt-in.
	WantsUsage bool
}

// Usage is a token count reported by a vendor.
type Usage struct {
	// InputTokens is the prompt size the vendor charged for.
	InputTokens int64
	// OutputTokens is the completion size the vendor charged for.
	OutputTokens int64
}

// IsZero reports whether no usage has been recorded.
func (u Usage) IsZero() bool { return u.InputTokens == 0 && u.OutputTokens == 0 }

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
	if other.OutputTokens > 0 {
		u.OutputTokens = other.OutputTokens
	}
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
// It is deliberately forgiving: a body it cannot fully understand yields a
// smaller count rather than an error, because refusing to proxy a request
// over an unrecognised field would be a worse failure than a rough estimate.
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

// DecodeBody parses a request or response body, returning nil when it is not
// a JSON object.
//
// A body that will not parse is not an error. It may be a multipart upload or
// a vendor shape this build does not know, and the request must still be
// proxied; it simply cannot be measured.
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
