# Send your first budgeted request

This guide stores one real provider key, creates a $5 run, issues a 15-minute
lease, and sends one request through spendlease. The example uses OpenAI; the
same flow works for every [supported provider](providers.md).

## Before you begin

You need:

- a vendor API key;
- the `v0.2.0-beta.2` spendlease binary on your `PATH`; and
- Python 3.9 or later for the example request.

Download the binary and matching `.sha256` from the
[`v0.2.0-beta.2` release](https://github.com/premhiru/spendlease/releases/tag/v0.2.0-beta.2).
For an evaluation, keep every command in the same working directory so it
uses the same `./spendlease.db` file.

!!! note "Use the dashboard if you prefer"

    Run `spendlease serve`, open <http://localhost:4000>, store the provider
    key under **Provider keys**, and use **Add an agent**. The form creates the
    principal, first run, and lease in one step, then shows copyable examples.
    The `sll_...` lease appears once. Continue at [make the
    request](#6-make-the-request).

## 1. Create the agent identity

```bash
spendlease keys principal create --name checkout-agent --mode observe
```

The command creates a principal and prints a legacy `slk_...` bootstrap key.
The application will not use that long-lived key; it will use a short-lived
lease. Keep or discard the bootstrap key according to your compatibility
needs, but never put it in source control.

`observe` is deliberate. Spendlease will calculate and record supported token
spend without blocking the first workload while you validate its accounting.

## 2. Store the vendor key

Piping the key keeps it out of shell history.

=== "Bash or zsh"

    ```bash
    printf '%s' "$OPENAI_API_KEY" | spendlease keys provider set openai
    ```

=== "PowerShell"

    ```powershell
    $env:OPENAI_API_KEY | spendlease keys provider set openai
    ```

Confirm that the credential exists without revealing it:

```bash
spendlease keys provider list
```

The vendor key is encrypted at rest. Development mode creates a local master
key beside SQLite; production must use an explicit
[master-key source](self-hosting.md#master-key).

## 3. Create a run

```bash
spendlease keys run create --principal checkout-agent --budget 5.00
```

Copy the printed `run_...` ID. A run represents one task or execution and owns
its budget. In a real orchestrator, create a new run for each unit of work
rather than reusing one indefinitely.

## 4. Issue a lease

Replace `run_...` with the ID from the previous step:

```bash
spendlease keys lease issue \
  --run run_... \
  --ttl 15m \
  --providers openai
```

The command prints an `sll_...` token once. It is not recoverable. Put it in
the environment of the process that will call the provider.

=== "Bash or zsh"

    ```bash
    export SPENDLEASE_LEASE_TOKEN='sll_...'
    export SPENDLEASE_URL='http://localhost:4000'
    ```

=== "PowerShell"

    ```powershell
    $env:SPENDLEASE_LEASE_TOKEN = 'sll_...'
    $env:SPENDLEASE_URL = 'http://localhost:4000'
    ```

If you lose the token, issue another lease. Do not try to retrieve it from the
database.

## 5. Start the gateway

```bash
spendlease serve
```

Leave this terminal open. In a second terminal, confirm both process and
datastore readiness:

```bash
curl --fail http://localhost:4000/healthz
curl --fail http://localhost:4000/readyz
```

The dashboard is at <http://localhost:4000>.

## 6. Make the request

Install the official OpenAI client:

```bash
python -m pip install openai
```

Create and run this small program:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:4000/v1",
    api_key=os.environ["SPENDLEASE_LEASE_TOKEN"],
)

response = client.chat.completions.create(
    model="gpt-5.4-mini",
    max_completion_tokens=128,
    messages=[{"role": "user", "content": "Reply with: spendlease works"}],
)

print(response.choices[0].message.content)
```

The explicit output limit is important. Strict enforcement cannot reserve an
upper bound for an output-producing request without one.

You can also use the spendlease helper, which only builds the client options:

```bash
python -m pip install 'spendlease==0.2.0b2' openai
```

```python
from openai import OpenAI
from spendlease import Lease

client = OpenAI(**Lease.from_env().openai_kwargs())
```

## 7. Confirm the result

Open the dashboard. You should see:

- `checkout-agent` with its $5 run;
- one allowed event for the selected model; and
- settled spend below the original budget.

For machine-readable verification:

```bash
spendlease ledger verify
spendlease ledger export --format json
```

If the request does not appear, check its `X-Spendlease-Accounting` response
header. `unmetered` means observe mode forwarded a request whose charge shape
could not be represented safely.

## 8. Prove enforcement before relying on it

After comparing representative traffic with the vendor's usage, switch the
principal to enforcement:

```bash
spendlease keys principal set-mode --name checkout-agent --mode enforce
```

Create a deliberately tiny run:

```bash
spendlease keys run create \
  --principal checkout-agent \
  --budget 0.000001
```

Issue a new OpenAI lease for that `run_...` ID and replace
`SPENDLEASE_LEASE_TOKEN` with the new token:

```bash
spendlease keys lease issue --run run_... --ttl 15m --providers openai
```

Run the same Python request again. Its reservation cannot fit, so it should
return:

```text
HTTP 402
X-Spendlease-Error: budget_exceeded
```

The provider is not contacted for that rejected request. Filter **Recent
events** by the agent and **Budget blocked** to confirm the decision.

To revoke all active leases immediately:

```bash
spendlease keys revoke --all --principal checkout-agent
```

The next request with the old token should return `401 unauthenticated`.

## Use another provider

Change three values: the credential name, the lease provider scope, and the
application base URL.

| Provider | Credential/scope | Base URL |
|---|---|---|
| Anthropic | `anthropic` | `http://localhost:4000` |
| Kimi | `kimi` | `http://localhost:4000/kimi/v1` |
| DeepSeek | `deepseek` | `http://localhost:4000/deepseek/v1` |
| xAI | `xai` | `http://localhost:4000/xai/v1` |
| Gemini | `gemini` | `http://localhost:4000/gemini/v1beta/openai` |
| Z.AI | `zai` | `http://localhost:4000/zai/api/paas/v4` |

Anthropic uses its native SDK. The other entries in the table use an
OpenAI-compatible client. Exact examples and certification boundaries are in
[Providers](providers.md).

## Next steps

- [Understand principals, runs, leases, and reservations](concepts.md).
- [Configure typed Python and TypeScript helpers](sdks.md).
- [Review common errors and resolutions](errors.md).
- [Complete the production checklist](production-checklist.md) before a real
  rollout.
