# `@spendlease/sdk`

Thin, dependency-free configuration helpers for vendor SDKs and the
spendlease admin surface. The package does not wrap model APIs.

Install the pinned beta from npm, or use a source checkout:

```bash
npm install @spendlease/sdk@0.2.0-beta.1
# From a checkout instead:
npm install ./sdk/typescript openai
```

Set `SPENDLEASE_LEASE_TOKEN` to an `sll_` token issued by the gateway.
`SPENDLEASE_URL` is optional and defaults to `http://localhost:4000`.

```typescript
import OpenAI from "openai";
import { Lease } from "@spendlease/sdk";

const lease = Lease.fromEnv();
const client = new OpenAI(lease.openAIOptions());
```

`AdminClient` manages runs and leases, reports effective remaining budget,
and verifies or exports the ledger through the guarded `/api/v1` operator API.

See the [examples](../../examples/) and
[getting-started guide](https://premhiru.github.io/spendlease/getting-started/)
for the complete gateway setup.
