package dashboard

import (
	"fmt"
	"net/http"
	"strconv"
)

type providerExample struct {
	IDPrefix    string
	Name        string
	Label       string
	Model       string
	Endpoint    string
	Configured  bool
	Primary     bool
	Install     string
	Environment string
	Python      string
	JavaScript  string
	Curl        string
}

type providerExampleSpec struct {
	model     string
	packagePy string
	packageJS string
	anthropic bool
}

var providerExampleSpecs = map[string]providerExampleSpec{
	"openai":    {model: "gpt-5.4-nano", packagePy: "openai", packageJS: "openai"},
	"anthropic": {model: "claude-haiku-4-5-20251001", packagePy: "anthropic", packageJS: "@anthropic-ai/sdk", anthropic: true},
	"kimi":      {model: "kimi-k2.6", packagePy: "openai", packageJS: "openai"},
	"deepseek":  {model: "deepseek-v4-flash", packagePy: "openai", packageJS: "openai"},
	"xai":       {model: "grok-4.5", packagePy: "openai", packageJS: "openai"},
	"gemini":    {model: "gemini-3.5-flash-lite", packagePy: "openai", packageJS: "openai"},
	"zai":       {model: "glm-4.7-flash", packagePy: "openai", packageJS: "openai"},
}

func (d *Dashboard) providerExamples(r *http.Request, names []string, token, scope string) []providerExample {
	statuses := d.endpointStatuses(r, names)
	examples := make([]providerExample, 0, len(statuses))
	for i, status := range statuses {
		spec, ok := providerExampleSpecs[status.Name]
		if !ok {
			continue
		}
		example := providerExample{
			IDPrefix: scope + "-" + status.Name, Name: status.Name, Label: status.Label,
			Model: spec.model, Endpoint: status.Endpoint, Configured: status.Configured, Primary: i == 0,
			Install: fmt.Sprintf("python -m pip install %s\nnpm install %s", spec.packagePy, spec.packageJS),
			Environment: fmt.Sprintf("SPENDLEASE_LEASE_TOKEN=%s\nSPENDLEASE_BASE_URL=%s",
				strconv.Quote(token), strconv.Quote(status.Endpoint)),
		}
		if spec.anthropic {
			example.Python = anthropicPythonExample(spec.model)
			example.JavaScript = anthropicJavaScriptExample(spec.model)
			example.Curl = anthropicCurlExample(spec.model)
		} else {
			example.Python = openAICompatiblePythonExample(spec.model)
			example.JavaScript = openAICompatibleJavaScriptExample(spec.model)
			example.Curl = openAICompatibleCurlExample(spec.model)
		}
		examples = append(examples, example)
	}
	return examples
}

func openAICompatiblePythonExample(model string) string {
	return fmt.Sprintf(`import os
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
    base_url=os.environ["SPENDLEASE_BASE_URL"],
)
response = client.chat.completions.create(
    model=%s,
    messages=[{"role": "user", "content": "Say hello in one sentence."}],
    max_tokens=64,
)
print(response.choices[0].message.content)`, strconv.Quote(model))
}

func openAICompatibleJavaScriptExample(model string) string {
	return fmt.Sprintf(`import OpenAI from "openai";

const client = new OpenAI({
  apiKey: process.env.SPENDLEASE_LEASE_TOKEN,
  baseURL: process.env.SPENDLEASE_BASE_URL,
});
const response = await client.chat.completions.create({
  model: %s,
  messages: [{ role: "user", content: "Say hello in one sentence." }],
  max_tokens: 64,
});
console.log(response.choices[0].message.content);`, strconv.Quote(model))
}

func openAICompatibleCurlExample(model string) string {
	return fmt.Sprintf(`curl "$SPENDLEASE_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $SPENDLEASE_LEASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":%s,"messages":[{"role":"user","content":"Say hello in one sentence."}],"max_tokens":64}'`, strconv.Quote(model))
}

func anthropicPythonExample(model string) string {
	return fmt.Sprintf(`import os
from anthropic import Anthropic

client = Anthropic(
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
    base_url=os.environ["SPENDLEASE_BASE_URL"],
)
message = client.messages.create(
    model=%s,
    messages=[{"role": "user", "content": "Say hello in one sentence."}],
    max_tokens=64,
)
print(message.content[0].text)`, strconv.Quote(model))
}

func anthropicJavaScriptExample(model string) string {
	return fmt.Sprintf(`import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  apiKey: process.env.SPENDLEASE_LEASE_TOKEN,
  baseURL: process.env.SPENDLEASE_BASE_URL,
});
const message = await client.messages.create({
  model: %s,
  messages: [{ role: "user", content: "Say hello in one sentence." }],
  max_tokens: 64,
});
console.log(message.content[0].text);`, strconv.Quote(model))
}

func anthropicCurlExample(model string) string {
	return fmt.Sprintf(`curl "$SPENDLEASE_BASE_URL/v1/messages" \
  -H "x-api-key: $SPENDLEASE_LEASE_TOKEN" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":%s,"messages":[{"role":"user","content":"Say hello in one sentence."}],"max_tokens":64}'`, strconv.Quote(model))
}
