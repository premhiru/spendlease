# Getting started

Install, register an agent, store a vendor key, and make your first proxied call.

> [!NOTE]
> `spendlease` is pre-v1. The complete v0.1 feature set is implemented, but
> pin a release before production use.

## Install

```bash
docker run -p 4000:4000 ghcr.io/premhiru/spendlease:edge
```

Or as a binary:

```bash
go install github.com/premhiru/spendlease/cmd/spendlease@latest
```

## Try the zero-credential demo

Before configuring anything, exercise the complete system locally:

```bash
spendlease demo
```

Open the printed dashboard URL. Three simulated agents call a mock provider;
the `retry-loop` agent exhausts its fake budget and then has its lease revoked
by the real kill switch. The command exits after 30 seconds by default. Use
`--duration 0` to run until Ctrl+C. No vendor key or persistent database is
created.

## 1. Register a principal

A principal is a registered agent with a stable identity. It holds a long-lived key.

```bash
spendlease keys principal create --name checkout-agent
```

```
Created principal checkout-agent (prn_qdvzedzj7ys6rg6repw65twoha), mode observe

  slk_zmrstdtyy4r5r4sa66xwfbprcmpi2sifs5txcqkegdmshqbtp27q

This key is shown once and is not recoverable. Store it now.
```

The key is shown once. Only its SHA-256 hash is stored, so it cannot be recovered from the database, from a backup, or by an administrator. If it is lost, create a new principal.

New principals start in **observe** mode. See [concepts](concepts.md#principal).

## 2. Store a vendor API key

The gateway holds the real vendor credentials so your agents never do.

```bash
spendlease keys provider set openai --key sk-proj-...
spendlease keys provider set anthropic --key sk-ant-...
```

Or pipe it, to keep the key out of your shell history:

```bash
cat openai-key.txt | spendlease keys provider set openai
```

Keys are encrypted at rest with AES-256-GCM. To confirm what is stored — names only, never values:

```bash
spendlease keys provider list
```

## 3. Start the gateway

```bash
spendlease serve
```

```
spendlease v0.1.0 listening on :4000
```

If a provider has no key stored, the gateway says so at startup rather than waiting for a failed request.

## 4. Create a run and lease

```bash
spendlease keys run create --principal checkout-agent --budget 25.00
spendlease keys lease issue --run <run-id> --ttl 15m --providers openai
```

Store the shown-once `sll_` token in `SPENDLEASE_LEASE_TOKEN`.

## 5. Point an SDK at it

The thin Python package validates the lease token and supplies the right vendor
SDK settings:

```bash
pip install ./sdk/python
```

```python
from openai import OpenAI
from spendlease import Lease

client = OpenAI(**Lease.from_env().openai_kwargs())
```

The package does not wrap model APIs. You can always override the base URL
directly and use the lease token where the vendor key would go:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:4000/v1",
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],  # sll_..., not your OpenAI key
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hello"}],
)
```

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://localhost:4000",
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
)
```

Streaming works exactly as it does against the vendor directly: chunks pass through as they arrive, with no buffering.

See [SDKs and examples](sdks.md) for TypeScript and admin-client usage.

Or with `curl`:

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $SPENDLEASE_LEASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

## What you should see

Every request produces one structured log line, with attribution and without anything sensitive:

```
level=INFO msg=request method=POST path=/v1/chat/completions status=200
  duration_ms=412 bytes=1204 principal=prn_qdvz... provider=openai
```

Request bodies, response bodies, and key material are never logged.

## Common problems

**`The credential presented is not a spendlease key`** — you passed a vendor key. Use the `slk_` key; the gateway supplies the vendor credential itself.

**`spendlease has no openai API key`** — run `spendlease keys provider set openai --key ...`. The error names the exact command.

**`No provider handles /some/path`** — the path is not one either adapter claims. Force a provider with an explicit prefix, for example `/openai/v1/whatever`. See [ADR-0006](adr/0006-provider-routing.md).

**`The stored openai credential could not be decrypted`** — `SPENDLEASE_MASTER_KEY` does not match the one this database was written with. Restore the original key, or re-enter the vendor credentials under the new one.

## Production

Do not expose port 4000 to an untrusted network without authentication in front of it, and set `SPENDLEASE_MASTER_KEY` explicitly:

```bash
spendlease keys master generate    # store this in your secret manager
export SPENDLEASE_ENV=production
export SPENDLEASE_MASTER_KEY=<the generated key>
```

With `SPENDLEASE_ENV=production`, the gateway refuses to start rather than generate a key beside the database. See [self-hosting](self-hosting.md).
