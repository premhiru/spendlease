package billing

import "testing"

func TestCanonicalUsageJSON(t *testing.T) {
	u := Usage{UnitOutputTokens: 7, UnitInputTokens: 11, UnitCachedInputTokens: 0}
	got, err := u.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"input_tokens":11,"output_tokens":7}`; got != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
	parsed, err := ParseUsageJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed[UnitInputTokens] != 11 || parsed[UnitOutputTokens] != 7 {
		t.Fatalf("parsed usage = %#v", parsed)
	}
}

func TestUsageRejectsInvalidDimensions(t *testing.T) {
	for _, usage := range []Usage{
		{"Bad Unit": 1},
		{"tokensé": 1},
		{"1tokens": 1},
		{"calls": -1},
	} {
		if err := usage.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", usage)
		}
	}
	if _, err := ParseUsageJSON(`{"seconds":1.5}`); err == nil {
		t.Fatal("ParseUsageJSON accepted a fractional quantity")
	}
}
