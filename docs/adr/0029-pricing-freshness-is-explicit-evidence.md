# 29. Pricing freshness is explicit evidence

## Status

Accepted.

## Context

A price's effective date says when the rate applies, not when somebody last
checked it. Treating the newest file or the total model count as freshness can
hide old entries when a later file updates only one model. Runtime scraping is
also unreliable: vendor pages are not stable machine-readable price feeds.

## Decision

Each price file may carry a `verified` date recording when every entry in that
file was compared with its linked vendor source. Active canonical entries
retain the date of the file that supplied their resolved price. Freshness is
therefore calculated from the oldest active entry, not the newest file.

The CLI lists and inspects the embedded snapshot and verifies both schema and
evidence age. The default review window is 45 days. The dashboard displays the
oldest date and warns when evidence is missing or stale. A weekly repository
workflow runs the same command and owns one tracking issue until verification
passes again.

`pricing verify` does not fetch vendor sites. A maintainer must review the
linked source and update the evidence date; automation only makes neglected
review visible.

## Consequences

- Effective dates and review dates cannot be confused.
- A partial price update does not make untouched models look newly verified.
- Operators can inspect the exact shipped rates without opening YAML files.
- Stale evidence becomes visible before it quietly survives several releases.
- Maintainers remain responsible for comparing rates with official sources.
