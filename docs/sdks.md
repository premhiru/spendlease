# SDKs and examples

The SDKs are deliberately thin. They validate an `sll_` lease token, normalize
the gateway URL, and produce configuration for the official vendor clients.
They do not wrap completion APIs and do not add framework integrations.

## Python

Install the package from `sdk/python` while the project is pre-release:

```bash
pip install ./sdk/python
```

```python
from openai import OpenAI
from spendlease import Lease

lease = Lease.from_env()
openai = OpenAI(**lease.openai_kwargs())
```

`Lease.from_env()` reads `SPENDLEASE_LEASE_TOKEN` and the optional
`SPENDLEASE_URL` (default `http://localhost:4000`). For Anthropic, pass
`lease.anthropic_kwargs()` to `Anthropic`.

## TypeScript

Install the package from `sdk/typescript`:

```bash
npm install ./sdk/typescript
```

```typescript
import OpenAI from "openai";
import { Lease } from "@spendlease/sdk";

const lease = Lease.fromEnv();
const openai = new OpenAI(lease.openAIOptions());
```

Use `lease.anthropicOptions()` with the official Anthropic client.

## Admin controls

Both packages export `AdminClient`, with `set_mode`/`setMode` and `revoke`
methods. Supply the gateway's admin token when calling from off-machine. The
client returns the refreshed dashboard table HTML because these are the same
guarded endpoints used by the dashboard.

## Runnable examples

The repository's [`examples`](https://github.com/premhiru/spendlease/tree/main/examples)
directory contains complete OpenAI examples for both languages. Direct base
URL configuration remains supported in every language; using a spendlease SDK
is optional.
