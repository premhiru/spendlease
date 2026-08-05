# SDKs and examples

The SDKs validate an `sll_` lease token, normalize the gateway URL, and build
configuration for the official vendor clients. They do not wrap model APIs,
retry requests, or add framework integrations.

The first beta packages are versioned as Python `0.2.0b1` and TypeScript
`0.2.0-beta.1`. Until the beta release workflow has successfully claimed the
registry names, install them from a pinned checkout or release artifact.

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

Both packages export `AdminClient`. Supply a named operator token when calling
from off-machine. Mutation methods automatically send the required
`X-Spendlease-Admin: 1` header.

The original dashboard controls remain available as `set_mode`/`setMode` and
`revoke`; they return the refreshed dashboard table HTML. The versioned JSON
methods return structured records:

| Operation | Python | TypeScript |
|---|---|---|
| Create run | `create_run` | `createRun` |
| List runs | `list_runs` | `listRuns` |
| Read run | `get_run` | `getRun` |
| Close run | `close_run` | `closeRun` |
| Remaining budget | `remaining_budget` | `remainingBudget` |
| Issue lease | `issue_lease` | `issueLease` |
| List leases | `list_leases` | `listLeases` |
| Revoke one lease | `revoke_lease` | `revokeLease` |
| Verify ledger | `verify_ledger` | `verifyLedger` |
| Export ledger | `export_ledger` | `exportLedger` |

Python example:

```python
from spendlease import AdminClient

admin = AdminClient("http://localhost:4000")
run = admin.create_run("prn_...", budget_usd="5.00")
lease = admin.issue_lease(
    run["id"], ttl_seconds=900, providers=["openai"], ceiling_usd="1.00"
)
print(lease["token"])  # shown once
print(admin.remaining_budget(run["id"])["effective_remaining_usd"])
admin.revoke_lease(lease["id"])
```

TypeScript example:

```typescript
import { AdminClient } from "@spendlease/sdk";

const admin = new AdminClient("http://localhost:4000");
const run = await admin.createRun("prn_...", "5.00");
const lease = await admin.issueLease(run.id, {
  ttlSeconds: 900,
  providers: ["openai"],
  ceilingUSD: "1.00",
});
console.log(lease.token); // shown once
console.log((await admin.remainingBudget(run.id)).effective_remaining_usd);
await admin.revokeLease(lease.id);
```

The admin client is for operator tooling, not agent code. Do not place an
`slo_` operator token in an agent environment.

## Runnable examples

The repository's [`examples`](https://github.com/premhiru/spendlease/tree/main/examples)
directory contains runnable OpenAI and Anthropic examples for both languages.
These are the two native SDK paths certified for enforcement. Direct base URL
configuration remains supported in every language, so using a spendlease SDK
is optional.
