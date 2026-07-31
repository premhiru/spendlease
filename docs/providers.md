# Providers

`spendlease` routes OpenAI, Anthropic, Kimi, DeepSeek, xAI, Gemini, and Z.AI.
The provider name is also the value used when storing a credential and scoping
a lease.

| Provider | Credential name | Application base URL | Default upstream |
|---|---|---|---|
| OpenAI | `openai` | `http://localhost:4000/v1` | `https://api.openai.com` |
| Anthropic | `anthropic` | `http://localhost:4000` | `https://api.anthropic.com` |
| Kimi | `kimi` | `http://localhost:4000/kimi/v1` | `https://api.moonshot.ai` |
| DeepSeek | `deepseek` | `http://localhost:4000/deepseek/v1` | `https://api.deepseek.com` |
| xAI | `xai` | `http://localhost:4000/xai/v1` | `https://api.x.ai` |
| Gemini | `gemini` | `http://localhost:4000/gemini/v1beta/openai` | `https://generativelanguage.googleapis.com` |
| Z.AI | `zai` | `http://localhost:4000/zai/api/paas/v4` | `https://api.z.ai` |

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
    messages=[{"role": "user", "content": "hello"}],
)
print(response.choices[0].message.content)
```

The lease must include every provider the application may call. A multi-vendor
lease can use `--providers openai,anthropic,gemini`.

## Pricing scope

The embedded price book contains selected current models for all seven
providers. It prices ordinary input, reported cache hits, reported cache
writes, output, and documented long-context tiers when the response exposes
the required token counts.

It does not try to reproduce every invoice line. Batch, flex, fast, priority,
regional, cache-storage, tool, image, audio, grounding, and other non-token
charges remain outside the budget calculation. Start a new workload in
observe mode and compare its ledger entries with the provider console before
turning on enforcement. See [Price book](pricing-book.md) for the exact fields
and source links.

## Override an upstream

Each provider has a `serve` flag for testing, a private relay, or a regional
endpoint:

```bash
spendlease serve --deepseek-url http://127.0.0.1:8080
```

The available flags are `--openai-url`, `--anthropic-url`, `--kimi-url`,
`--deepseek-url`, `--xai-url`, `--gemini-url`, and `--zai-url`.
