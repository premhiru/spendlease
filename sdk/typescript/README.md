# `@spendlease/sdk`

Thin, dependency-free configuration helpers for vendor SDKs and the
spendlease admin surface. It deliberately does not wrap model APIs.

```typescript
import OpenAI from "openai";
import { Lease } from "@spendlease/sdk";

const lease = Lease.fromEnv();
const client = new OpenAI(lease.openAIOptions());
```
