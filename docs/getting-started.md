# Getting started

This guide uses `v0.2.0-beta.1` to store an OpenAI key, create a budgeted run,
issue a lease, and make one request through the gateway. Anthropic uses the
same flow with a different provider name and base URL.

> [!NOTE]
> `spendlease` is pre-v1. `v0.2.0-beta.1` is the current beta. Pin its verified
> binary or the immutable container digest from `container-image.txt` while
> evaluating it; use `edge` only when you deliberately want unreleased `main`.

## Prerequisites

- An OpenAI API key
- Python 3.9 or later for the Python example

## Install

Download the binary for your platform and its `.sha256` file from the
[`v0.2.0-beta.1` release](https://github.com/premhiru/spendlease/releases/tag/v0.2.0-beta.1),
verify the checksum, and place `spendlease` (or `spendlease.exe`) on your
`PATH`.

To build from source instead, install Go 1.25.12 or later and run:

```bash
git clone https://github.com/premhiru/spendlease.git
cd spendlease
go install ./cmd/spendlease
```

If `spendlease` is not found afterwards, add Go's binary directory to your
`PATH` or replace `spendlease` in the commands below with
`go run ./cmd/spendlease`.

## Try the demo first

The demo runs the real gateway against a local mock provider. It creates three
simulated agents, lets `retry-loop` exhaust its budget, and revokes that
agent's lease:

```bash
spendlease demo
```

Open the dashboard URL printed by the command. The demo stops after 30 seconds;
use `--duration 0` to leave it running until Ctrl+C. Its database is in memory,
so none of the demo state is reused below.

## Use the dashboard instead

The numbered steps below show the CLI because those commands are easy to
automate. For an interactive setup, start the persistent gateway first:

```bash
spendlease serve
```

Open <http://localhost:4000>. Under **Provider keys**, store the OpenAI key.
Then use **Add an agent** to choose a name, mode, run budget, lease duration,
and allowed providers. The dashboard creates the principal, first run, and
lease together and shows the `sll_...` token once.

Copy that lease into `SPENDLEASE_LEASE_TOKEN`, use the displayed provider base
URL in the vendor SDK, and continue at [Make a request](#5-make-a-request).
The provider key is never shown again. Dashboard-created principals do not
expose the older `slk_...` compatibility key; use the CLI below only if a
legacy integration still needs one.

## 1. Create a principal

A principal is the stable identity of an agent or service:

```bash
spendlease keys principal create --name checkout-agent
```

The command prints a principal ID and a one-time `slk_` key. Keep that key out
of application environments. It is a long-lived compatibility and bootstrap
credential; the application will use a short-lived `sll_` lease instead.

New principals start in `observe` mode. Requests are priced and recorded, but
budget overruns are not blocked until you switch the principal to `enforce`.

## 2. Store the vendor key

Piping the key keeps it out of shell history.

On Bash or zsh:

```bash
printf '%s' "$OPENAI_API_KEY" | spendlease keys provider set openai
```

On PowerShell:

```powershell
$env:OPENAI_API_KEY | spendlease keys provider set openai
```

You can also pass `--key sk-proj-...` directly. Confirm that the provider was
stored without exposing its value:

```bash
spendlease keys provider list
```

For local development, spendlease creates a master key beside the SQLite
database. Production deployments must provide the key directly, through a
mounted secret file, or through a secret-manager/KMS command; see
[Self-hosting](self-hosting.md#master-key).

## 3. Create a run and issue a lease

Create a run with a $25 budget:

```bash
spendlease keys run create --principal checkout-agent --budget 25.00
```

Copy the `run_...` ID from the output, then issue a 15-minute lease scoped to
OpenAI:

```bash
spendlease keys lease issue --run run_... --ttl 15m --providers openai
```

The lease token starts with `sll_` and is shown once. Set it in the shell that
will run the application.

On Bash or zsh:

```bash
export SPENDLEASE_LEASE_TOKEN=sll_...
export SPENDLEASE_URL=http://localhost:4000
```

On PowerShell:

```powershell
$env:SPENDLEASE_LEASE_TOKEN = "sll_..."
$env:SPENDLEASE_URL = "http://localhost:4000"
```

The lease expires after 15 minutes. Issue a new one if the example later
returns `401 unauthenticated`.

## 4. Start the gateway

Run this from the same directory as the earlier `keys` commands so every
command uses the same `./spendlease.db` file:

```bash
spendlease serve
```

The gateway listens on <http://localhost:4000>. Leave it running and use a
second terminal for the next step. The dashboard is available at the same URL.

## 5. Make a request

### Python helper

Install the thin spendlease helper and the official OpenAI client:

```bash
python -m pip install 'spendlease==0.2.0b1' openai
```

The relevant application code is:

```python
from openai import OpenAI
from spendlease import Lease

client = OpenAI(**Lease.from_env().openai_kwargs())
```

The helper reads `SPENDLEASE_LEASE_TOKEN` and `SPENDLEASE_URL`. It only builds
the OpenAI client options; it does not wrap the OpenAI API.

### Configure OpenAI directly

The helper is optional. Any OpenAI client can use the gateway directly:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:4000/v1",
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
)

response = client.chat.completions.create(
    model="gpt-5.4-mini",
    messages=[{"role": "user", "content": "hello"}],
)
print(response.choices[0].message.content)
```

For Anthropic, store the key with `keys provider set anthropic`, include
`anthropic` in the lease's provider scope, and use
`base_url="http://localhost:4000"`.

Kimi, DeepSeek, xAI, Gemini, and Z.AI use their OpenAI-compatible APIs through
an explicit provider prefix. [Providers](providers.md) lists the exact base
URL and credential name for each one.

### curl

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $SPENDLEASE_LEASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"hello"}]}'
```

## 6. Turn on enforcement

Check the dashboard and compare its totals with the vendor's usage. When the
numbers look right for your workload, switch the principal to enforcement:

```bash
spendlease keys principal set-mode --name checkout-agent --mode enforce
```

Requests whose reservation does not fit the run budget now return
`402 budget_exceeded` without reaching the vendor. To revoke every active
lease for this principal:

```bash
spendlease keys revoke --all --principal checkout-agent
```

Or use the JSON operator API from an orchestrator to create and close runs,
issue or revoke individual leases, and inspect effective remaining budget.
See [API reference](api-reference.md#json-operator-api) for curl examples and
[SDKs and examples](sdks.md#admin-controls) for typed client methods.

## Troubleshooting

### `401 unauthenticated`

Check that the application is using the shown-once `sll_` lease token, not the
vendor key or the long-lived `slk_` principal key. The lease may also have
expired or been revoked; issue a fresh lease and retry.

### `403 lease_scope_denied`

The lease does not include the provider selected by the request path. Issue a
new lease with that provider in `--providers`; multiple names are a
comma-separated list such as `--providers openai,anthropic,gemini`.

### `402 budget_exceeded`

The request's upper-bound reservation does not fit the run or one of its
budgeted ancestors. Reduce the request's output limit, create a run with a
larger budget, or temporarily return the principal to `observe` while checking
the estimate.

### `503 provider_credential_missing`

Run `spendlease keys provider set openai` and make sure the command uses the
same `--store` path as `spendlease serve`. A decryption error means
`SPENDLEASE_MASTER_KEY` does not match the key used when the vendor credential
was stored.

### `404 unknown_route`

The request path is not claimed by a provider adapter. Prefix the path with
its provider name, such as `/openai`, `/anthropic`, or `/deepseek`; the prefix
is removed before forwarding. See
[API reference](api-reference.md#ambiguous-and-unknown-paths).

## Next steps

- [SDKs and examples](sdks.md) covers Python, TypeScript, and the admin client.
- [Policy reference](policy-reference.md) documents budgets, lease scope, and
  reservation settings.
- [Self-hosting](self-hosting.md) covers persistent state, secrets, remote
  dashboard access, backups, and upgrades.
