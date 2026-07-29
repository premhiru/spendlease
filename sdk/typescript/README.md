# `@spendlease/sdk`

Thin, dependency-free configuration helpers for vendor SDKs and the
spendlease admin surface. The package does not wrap model APIs.

Install from the repository while the package is pre-release:

```bash
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

See the [examples](../../examples/) and
[getting-started guide](https://premhiru.github.io/spendlease/getting-started/)
for the complete gateway setup.
