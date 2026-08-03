# spendlease Python SDK

Thin, dependency-free configuration helpers for vendor SDKs and the
spendlease admin surface. The package does not wrap model APIs; it gives the
official OpenAI and Anthropic clients the correct gateway URL and lease token.

Install the beta from PyPI after its release, or from a checkout:

```bash
python -m pip install 'spendlease==0.2.0b1'
# From a checkout instead:
python -m pip install ./sdk/python openai
```

Set `SPENDLEASE_LEASE_TOKEN` to an `sll_` token issued by the gateway.
`SPENDLEASE_URL` is optional and defaults to `http://localhost:4000`.

```python
from openai import OpenAI
from spendlease import Lease

lease = Lease.from_env()
client = OpenAI(**lease.openai_kwargs())
```

`AdminClient` manages runs and leases, reports effective remaining budget,
and verifies or exports the ledger through the guarded `/api/v1` operator API.

See the [examples](../../examples/) and
[getting-started guide](https://premhiru.github.io/spendlease/getting-started/)
for the complete gateway setup.
