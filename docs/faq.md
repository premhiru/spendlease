# FAQ

## Is spendlease ready for production?

It is pre-v1. The complete v0.1 surface is implemented and tested, including
the encrypted vault, SQLite ledger, reserve/settle enforcement, short-lived
leases and sub-second kill switch. Pin a release and evaluate it in observe
mode before making it a production dependency.

## Why does a new principal default to observe mode?

An unproven gateway should not enter the blocking path on day one. Observe
mode performs the same pricing and budget decisions but forwards requests that
would have been blocked. Once the dashboard matches your vendor bill and
workload expectations, switch the principal to enforce mode.

## What happens when `max_tokens` is missing?

The active price-book entry provides `default_max_tokens`. Unknown models use
the fallback ceiling. A reservation is always bounded; missing the field never
means unlimited output and never reserves the run's entire remaining budget.

## Why did I receive `402 Payment Required`?

The request's upper-bound reservation did not fit the target run or one of its
budgeted ancestors. The JSON response names that run and includes its budget,
settled spend, pending holds, requested amount, remaining balance and
shortfall. Reduce the output ceiling, increase the run budget, or use observe
mode while validating the estimate.

## Are provider failures charged?

No. A non-2xx response or transport failure releases the full reservation and
does not append a ledger entry. A client disconnect is different: the vendor
may already have done billable work, so spendlease settles the usage observed
before the disconnect or records a marked estimate.

## Does the dashboard need internet access?

No. Its HTML, CSS and htmx are embedded in the binary. Loopback access is
credential-free; remote access requires an admin token and should be placed
behind TLS.

## Is PostgreSQL supported?

Not yet. SQLite is the implemented default and is configured for WAL, foreign
keys and a busy timeout. PostgreSQL remains an optional-backend milestone, not
a hidden requirement for the zero-configuration path.

## Why no LangChain or CrewAI integration?

Base-URL override works with vendor SDKs in every language and does not depend
on a framework's release cycle. Framework-specific adapters are deliberately
out of scope for v0.1.
