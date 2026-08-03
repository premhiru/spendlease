# Examples

These examples cover every provider whose native SDK path is certified for
enforcement: OpenAI and Anthropic. Complete the
[getting-started guide](../docs/getting-started.md) first, issue the lease with
the matching provider scope, then set its shown-once token in the shell that
will run the example:

```bash
export SPENDLEASE_LEASE_TOKEN=sll_...
export SPENDLEASE_URL=http://localhost:4000
```

PowerShell equivalent:

```powershell
$env:SPENDLEASE_LEASE_TOKEN = "sll_..."
$env:SPENDLEASE_URL = "http://localhost:4000"
```

Run either Python example from the repository root:

```bash
python -m pip install ./sdk/python openai anthropic
python examples/openai_python.py
python examples/anthropic_python.py
```

Run either TypeScript example with a TypeScript runner after installing the
local package and both vendor clients:

```bash
npm install ./sdk/typescript openai @anthropic-ai/sdk tsx
npx tsx examples/openai_typescript.ts
npx tsx examples/anthropic_typescript.ts
```

Neither SDK wraps OpenAI or Anthropic. They only validate the lease token and
produce the base URL and authentication options expected by the vendor SDK.
The OpenAI-compatible providers use the same OpenAI client shape with a
provider-specific gateway base URL. See [Providers](../docs/providers.md) for
those URLs and their beta accounting status. For a credential-free
walkthrough, run `spendlease demo` instead.
