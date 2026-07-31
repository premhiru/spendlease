# 17. Filter the operational event stream

- **Status:** Accepted
- **Date:** 2026-07-31
- **Supersedes:** The rejection of dashboard filters in [ADR-0013](0013-the-dashboard-is-one-table.md)

## Context

ADR-0013 rejected filters when the dashboard contained one spend summary. The
dashboard now also carries an operational timeline containing allowed calls,
budget decisions, revocations, and expirations. Those events have a different
failure mode: routine calls from busy agents can push a budget block or
revocation out of the small recent window.

Filtering rows already sent to the browser would not solve that problem. A
matching event outside the first page would still look absent.

## Decision

Filter operational events in SQLite before applying the row limit. The event
view supports agent, result, time window, and run-or-lease ID filters. It opens
on attention events from the last 24 hours, keeping allowed calls available but
out of the default incident view.

The dashboard loads 20 matching rows initially and can expand to 200. Filters
apply only to the operational timeline; the spend summary remains complete and
sorted by spend.

## Consequences

- Blocks and lease changes are not immediately buried by successful calls.
- Search examines durable records rather than the current browser page.
- Filter choices survive the three-second refresh and state-changing controls.
- This remains an operational view, not unbounded audit-log pagination.
- ADR-0013's decisions to avoid charts and keep the spend summary simple still
  stand.
