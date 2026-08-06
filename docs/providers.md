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

| Provider | Status | Credential name | Application base URL | Default upstream |
|---|---|---|---|---|
| OpenAI | Certified | `openai` | `http://localhost:4000/v1` | `https://api.openai.com` |
| Anthropic | Certified | `anthropic` | `http://localhost:4000` | `https://api.anthropic.com` |
| Kimi | Beta | `kimi` | `http://localhost:4000/kimi/v1` | `https://api.moonshot.ai` |
| DeepSeek | Beta | `deepseek` | `http://localhost:4000/deepseek/v1` | `https://api.deepseek.com` |
| xAI | Beta | `xai` | `http://localhost:4000/xai/v1` | `https://api.x.ai` |
| Gemini | Beta | `gemini` | `http://localhost:4000/gemini/v1beta/openai` | `https://generativelanguage.googleapis.com` |
| Z.AI | Beta | `zai` | `http://localhost:4000/zai/api/paas/v4` | `https://api.z.ai` |

**Certified** means the native vendor request and usage-response shapes have
dedicated gateway tests and copy-paste examples in Python and TypeScript.
**Beta** means routing, credential replacement, price-book entries, and the
shared OpenAI-compatible accounting path are implemented, but provider-side
behavior may change independently. Start every beta-provider workload in
observe mode and compare it with the vendor console before enforcing a budget.

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

Runnable native-client examples for both certified providers are in the
[`examples`](https://github.com/premhiru/spendlease/tree/main/examples)
directory.

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
