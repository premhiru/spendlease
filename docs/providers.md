# Providers

`spendlease` routes OpenAI, Anthropic, Kimi, DeepSeek, xAI, Gemini, and Z.AI.
The provider name is also the value used when storing a credential and scoping
a lease.

Provider routing and billing certification are separate. The gateway can pass
through more routes than it can safely price. Strict enforcement supports
ordinary text generation and embeddings with known models and explicit output
limits, permits reviewed no-spend routes, and blocks media, batches,
provider-hosted tools, explicit premium processing options, and unknown billing
shapes. Observe mode forwards unsupported traffic with
`X-Spendlease-Accounting: unmetered`.

| Provider | Certification | Verified | Credential name | Application base URL | Default upstream |
|---|---|---:|---|---|---|
| OpenAI | Native | 2026-08-06 | `openai` | `http://localhost:4000/v1` | `https://api.openai.com` |
| Anthropic | Native | 2026-08-06 | `anthropic` | `http://localhost:4000` | `https://api.anthropic.com` |
| Kimi | Compatible | 2026-08-06 | `kimi` | `http://localhost:4000/kimi/v1` | `https://api.moonshot.ai` |
| DeepSeek | Compatible | 2026-08-06 | `deepseek` | `http://localhost:4000/deepseek/v1` | `https://api.deepseek.com` |
| xAI | Compatible | 2026-08-06 | `xai` | `http://localhost:4000/xai/v1` | `https://api.x.ai` |
| Gemini | Compatible | 2026-08-06 | `gemini` | `http://localhost:4000/gemini/v1beta/openai` | `https://generativelanguage.googleapis.com` |
| Z.AI | Compatible | 2026-08-06 | `zai` | `http://localhost:4000/zai/api/paas/v4` | `https://api.z.ai` |

**Native** means the vendor has a dedicated adapter, gateway integration tests,
and copy-paste examples in Python and TypeScript. **Compatible** means the
vendor uses the shared OpenAI-compatible adapter and has dated, vendor-documented
fixtures for non-streaming usage, terminal streaming usage, cache counts, and
reasoning counts. The verification date records when those documented shapes
were last reviewed; it is not a promise that the vendor will never change.

OpenAI and Anthropic can be inferred from their normal API paths. The five
OpenAI-compatible providers use an explicit `/<provider>` prefix because their
paths overlap. The gateway removes only that prefix; the rest of the path is
forwarded unchanged.

## Store a key and issue a lease

This example uses DeepSeek. The same commands work for another provider after
changing its name.

```bash
spendlease keys provider set deepseek --key sk-...
spendlease keys run create --principal checkout-agent --budget 10.00
spendlease keys lease issue --run run_... --ttl 15m --providers deepseek
```

Then point an OpenAI-compatible client at the provider's spendlease URL:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:4000/deepseek/v1",
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
)

response = client.chat.completions.create(
    model="deepseek-v4-flash",
    max_completion_tokens=512,
    messages=[{"role": "user", "content": "hello"}],
)
print(response.choices[0].message.content)
```

The lease must include every provider the application may call. A multi-vendor
lease can use `--providers openai,anthropic,gemini`.

Runnable native-client examples for both native-certified providers are in the
[`examples`](https://github.com/premhiru/spendlease/tree/main/examples)
directory.

## Conformance evidence

The compatible-provider fixtures live in
[`internal/providers/testdata`](https://github.com/premhiru/spendlease/tree/main/internal/providers/testdata).
Each fixture names its vendor source, provenance, and review date. The tests require both a
streaming and non-streaming response for Kimi, DeepSeek, xAI, Gemini, and Z.AI,
including the usage fields that affect billing.

The repository also contains a weekly, opt-in live smoke workflow. It makes two
minimal requests per configured provider: one ordinary response and one stream,
both capped at one output token. Add any of these GitHub Actions secrets to
enable that provider:

```text
SPENDLEASE_SMOKE_KIMI_KEY
SPENDLEASE_SMOKE_DEEPSEEK_KEY
SPENDLEASE_SMOKE_XAI_KEY
SPENDLEASE_SMOKE_GEMINI_KEY
SPENDLEASE_SMOKE_ZAI_KEY
```

Run **Provider conformance smoke** manually after adding a secret. With no
secrets, the scheduled workflow makes no vendor calls and says so in its job
summary. A skipped run is not live certification. Model and endpoint overrides
are available when running the `live`-tagged Go test locally; see the test file
for their environment-variable names.

## Pricing scope

The embedded price book contains selected current models for all seven
providers. It prices ordinary input, reported cache hits, reported cache
writes, output, and documented long-context tiers when the response exposes
the required token counts.

It does not try to reproduce every invoice line. Explicit `service_tier`
values other than reviewed standard-rate values are refused before egress in
enforce mode. Anthropic fast mode and US inference routing are handled the same
way. Account-level defaults, negotiated rates, batch pricing, cache storage,
tool, image, audio, grounding, and other non-token charges remain outside the
budget calculation. Start a new workload in observe mode and compare its
ledger entries with the provider console before turning on enforcement. See
[Price book](pricing-book.md) for the exact fields and source links.

## Override an upstream

Each provider has a `serve` flag for testing, a private relay, or a regional
endpoint:

```bash
spendlease serve --deepseek-url http://127.0.0.1:8080
```

The available flags are `--openai-url`, `--anthropic-url`, `--kimi-url`,
`--deepseek-url`, `--xai-url`, `--gemini-url`, and `--zai-url`.
