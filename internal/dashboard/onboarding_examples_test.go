package dashboard

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderExamplesCoverEveryRoutedProvider(t *testing.T) {
	providers := []string{"openai", "anthropic", "kimi", "deepseek", "xai", "gemini", "zai"}
	d := &Dashboard{}
	req := httptest.NewRequest("GET", "https://gateway.example/", nil)
	examples := d.providerExamples(req, providers, "sll_example", "test")
	if len(examples) != len(providers) {
		t.Fatalf("examples = %d, want %d", len(examples), len(providers))
	}
	seenIDs := map[string]bool{}
	for _, example := range examples {
		if example.Model == "" || example.Endpoint == "" || example.Environment == "" ||
			example.Python == "" || example.JavaScript == "" || example.Curl == "" {
			t.Fatalf("incomplete %s example: %+v", example.Name, example)
		}
		if seenIDs[example.IDPrefix] {
			t.Fatalf("duplicate example id %q", example.IDPrefix)
		}
		seenIDs[example.IDPrefix] = true
		for _, snippet := range []string{example.Python, example.JavaScript, example.Curl} {
			if !strings.Contains(snippet, example.Model) || !strings.Contains(snippet, "64") {
				t.Errorf("%s snippet lacks model or strict output ceiling:\n%s", example.Name, snippet)
			}
		}
	}
	if !strings.Contains(examples[1].Python, "from anthropic import Anthropic") ||
		!strings.Contains(examples[1].Curl, "x-api-key") {
		t.Fatalf("Anthropic example does not use its native client/authentication: %+v", examples[1])
	}
	if !strings.Contains(examples[2].Python, "from openai import OpenAI") ||
		!strings.Contains(examples[2].Endpoint, "/kimi/v1") {
		t.Fatalf("compatible-provider example is wrong: %+v", examples[2])
	}
}
