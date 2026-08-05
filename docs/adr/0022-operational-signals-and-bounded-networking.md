# ADR-0022: Operational signals and bounded networking

**Status:** Accepted

## Context

A live process is not necessarily able to enforce a budget. A datastore outage
should remove a gateway from service without causing an orchestrator to restart
an otherwise healthy process forever. Logs alone are also a poor interface for
alerting and capacity planning, while request-path webhook delivery would turn
an alert receiver outage into agent latency.

The reverse proxy has one unusual timeout requirement: a normal completion can
wait a long time for first response headers, and a valid stream can remain open
for minutes. A generic short server write timeout would break working vendor
requests. Leaving every phase unbounded is not acceptable either.

## Decision

`/healthz` remains an I/O-free liveness signal. `/readyz` separately checks the
datastore under a two-second deadline. Startup verifies that every stored
vendor credential decrypts with the configured keyring.

`/metrics` exposes Prometheus text with fixed, bounded labels. Principal IDs,
run IDs, model names, request paths, prompts, headers, and credentials are not
metric labels. Request totals, duration sums, response bytes, budget outcomes,
and alert delivery outcomes are sufficient for service-level dashboards and
alerts without creating an unbounded or sensitive time series.

High-signal failures may also enter a bounded asynchronous webhook queue. The
request that caused an event never waits for delivery. Events contain only
sanitized operational fields, use an `alt_` ID, and are signed over the raw JSON
body with HMAC-SHA256. Production requires HTTPS and a signing secret. Redirects
are refused. Delivery retries three times, exposes sent/failed/dropped metrics,
and drains for a bounded interval during shutdown.

Proxy concurrency defaults to 256, while operational endpoints remain outside
that gate. Header and body reads, connects, TLS handshakes, response headers,
and non-streaming calls have explicit deadlines. Streaming calls share the
connection and response-header deadlines but deliberately have no total write
deadline.

## Consequences

- An orchestrator can distinguish “restart this process” from “stop routing
  traffic until its datastore recovers.”
- Monitoring does not need access to the operator API or its sensitive
  per-agent data.
- Alert receiver latency and outages cannot slow an agent request; sustained
  alert storms are bounded and visible as dropped events.
- The webhook queue is in memory. A process crash can lose queued events, so
  metrics and the durable operator audit remain the sources for reconciliation.
- Operators must select timeout and concurrency values that match their vendor
  latency and database capacity rather than treating larger limits as free.

## Rejected alternatives

**One health endpoint that checks everything.** It conflates liveness and
readiness and can create restart loops during an external outage.

**Principal and model metric labels.** They are attractive for dashboards but
create sensitive, unbounded cardinality. The datastore-backed dashboard and
ledger are the right interfaces for that detail.

**Synchronous webhook delivery.** It couples budget enforcement latency to an
unrelated receiver and creates a new availability dependency.

**A short global write timeout.** It terminates healthy streaming completions.
