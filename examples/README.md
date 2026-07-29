# Examples

These examples use the official OpenAI client with a spendlease lease. Complete
the [getting-started guide](../docs/getting-started.md) first, then set the
shown-once lease token in the shell that will run the example:

```bash
export SPENDLEASE_LEASE_TOKEN=sll_...
export SPENDLEASE_URL=http://localhost:4000
```

PowerShell equivalent:

```powershell
$env:SPENDLEASE_LEASE_TOKEN = "sll_..."
$env:SPENDLEASE_URL = "http://localhost:4000"
```

Run the Python example from the repository root:

```bash
python -m pip install ./sdk/python openai
python examples/openai_python.py
```

Run the TypeScript example with a TypeScript runner of your choice after
installing the local package and OpenAI client:

```bash
npm install ./sdk/typescript openai tsx
npx tsx examples/openai_typescript.ts
```

Neither SDK wraps OpenAI or Anthropic. They only validate the lease token and
produce the base URL and authentication options expected by the vendor SDK.
For a credential-free walkthrough, run `spendlease demo` instead.
