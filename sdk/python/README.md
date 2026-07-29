# spendlease Python SDK

Thin, dependency-free configuration helpers for vendor SDKs and the
spendlease admin surface. The package does not wrap model APIs; it gives the
official OpenAI and Anthropic clients the correct gateway URL and lease token.

```python
from openai import OpenAI
from spendlease import Lease

lease = Lease.from_env()
client = OpenAI(**lease.openai_kwargs())
```

See the [examples](../../examples/) and
[documentation](https://premhiru.github.io/spendlease/) for complete usage.
