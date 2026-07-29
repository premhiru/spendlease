# Examples

These examples use the official vendor clients with a spendlease lease. Start
the gateway, create a run, issue a lease, and export its shown-once token:

```bash
export SPENDLEASE_LEASE_TOKEN=sll_...
export SPENDLEASE_URL=http://localhost:4000
```

- [`openai_python.py`](openai_python.py) uses the thin Python helper.
- [`openai_typescript.ts`](openai_typescript.ts) uses the thin TypeScript helper.

Neither SDK wraps OpenAI or Anthropic. They only validate the lease token and
produce the base URL and authentication options expected by the vendor SDK.
For a credential-free end-to-end walkthrough, run `spendlease demo` instead.
