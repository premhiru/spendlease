# 13. The dashboard is one table

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

Spend is being recorded, and until now the only way to read it was to open the SQLite file. The dashboard is what makes the ledger legible.

It is also the part of the product most likely to grow without limit. Every dashboard acquires charts, date pickers, filters and drill-downs, and each one is defensible in isolation.

## Decision

**One table, sorted by spend descending. No charts.**

The question during an incident is "which agent is costing me money right now". That is answered by sorting, and the answer is the top row. A chart of spend over time answers a question nobody asks at 3am, and the effort spent building it is effort not spent on enforcement.

The columns are the ones that change a decision: agent, mode, runs, calls, spend, last seen. A row carries two badges when they apply — **would have been blocked** when a run exceeded its budget, and a count of entries that were estimated rather than priced from vendor-reported usage.

That first badge is the point of observe mode. Every request behind it was served and would not have been under enforcement; showing that is what turns "install it and see" into "switch it on".

**Mode switching is one click.** A principal left in observe mode out of inconvenience is a principal that never gets enforced, so the toggle posts and swaps the table in place with no page navigation and no documentation to read.

**The table refreshes every three seconds, except while a button has focus.** Without that guard the table replaces itself underneath somebody reaching for the toggle and the click lands on an element that no longer exists. This was found by opening the page in a browser, not by a test.

## Consequences

- The page is legible at a glance and has nothing to learn.
- Tailwind and htmx load from a CDN, which the technology choices specify. **The dashboard therefore does not render correctly without internet access.** That is a real limitation for an air-gapped deployment, and it sits awkwardly beside a product whose selling point is a single container with nothing to provision. Vendoring both into the embedded filesystem would fix it and is a small change; it is not done here because the CDN is a fixed technology choice, but it is the first thing to revisit if anyone reports it.
- `POST /admin/principals/{id}/mode` is **unauthenticated**, like the rest of the gateway on loopback. It is also the first state-changing admin route, and switching a principal to observe mode disables enforcement. The gateway now prints a warning on the page itself when it is not bound to loopback. See `SECURITY.md`.
- Polling costs one aggregate query every three seconds per open tab. The query is two grouped joins over indexed columns; if it ever matters, the answer is caching rather than charts.
- Run-level detail exists in the store (`RunSummaries`) but is not surfaced yet. It is what a row should expand into, and adding it is the one extension that would not violate the spirit of this decision.

## Options rejected

- **A chart of spend over time.** The most-requested feature of any spend tool, and the least useful during the incident this product exists for. Explicitly out of scope.
- **Filters, date ranges, search.** Every one is reasonable and every one delays enforcement. A deployment with enough principals to need search has a different problem.
- **A JavaScript framework.** No build step is a feature: `docker run` has to produce a working dashboard with no node_modules anywhere in the story.
- **Server-sent events instead of polling.** Genuinely better, and it would mean a second streaming implementation in a codebase whose streaming path is its most carefully verified property. Three-second polling on a table is not the bottleneck.
