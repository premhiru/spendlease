# SDKs and examples

The SDKs validate an `sll_` lease token, normalize the gateway URL, and build
configuration for the official vendor clients. They do not wrap model APIs,
retry requests, or add framework integrations.

Neither package is published to PyPI or npm yet. Install it from a pinned
checkout or commit while the project is pre-release.

Set the lease issued by `spendlease keys lease issue` before running an
example:

```bash
export SPENDLEASE_LEASE_TOKEN=sll_...
export SPENDLEASE_URL=http://localhost:4000
```

PowerShell equivalent:

```powershell
$env:SPENDLEASE_LEASE_TOKEN = "sll_..."
$env:SPENDLEASE_URL = "http://localhost:4000"
```

## Python

Install the package from `sdk/python` while the project is pre-release:

```bash
python -m pip install ./sdk/python openai
```

```python
from openai import OpenAI
from spendlease import Lease

lease = Lease.from_env()
openai = OpenAI(**lease.openai_kwargs())
```

`Lease.from_env()` reads `SPENDLEASE_LEASE_TOKEN` and the optional
`SPENDLEASE_URL` (default `http://localhost:4000`). For Anthropic, pass
`lease.anthropic_kwargs()` to `Anthropic` after installing the official
`anthropic` package.

## TypeScript

Install the package from `sdk/typescript`:

```bash
npm install ./sdk/typescript openai
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
methods return the refreshed dashboard table HTML because they use the same
endpoints as the dashboard controls. They also send the required
`X-Spendlease-Admin: 1` anti-CSRF header.

The admin client is for operator tooling, not agent code. Do not place
`SPENDLEASE_ADMIN_TOKEN` in an agent environment.

## Runnable examples

The repository's [`examples`](https://github.com/premhiru/spendlease/tree/main/examples)
directory contains runnable OpenAI examples for both languages. Direct base
URL configuration remains supported in every language, so using a spendlease
SDK is optional.
