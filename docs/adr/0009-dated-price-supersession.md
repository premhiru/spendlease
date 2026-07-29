# 9. Prices are superseded by date, never edited

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

Vendor prices change. Sometimes with notice: Anthropic publishes Claude Sonnet 5 at an introductory $2/$10 per MTok "through August 31, 2026, after which the standard pricing of $3/$15 per million input/output tokens will take effect."

A ledger entry written today has to remain explainable in six months. If the price book is edited in place when a price changes, every historical entry silently becomes unauditable: the cost recorded no longer matches the cost the book would compute, and there is no way to tell whether that is a bug or a price change.

The price book is also the file most likely to receive contributions from people who have never read the rest of the codebase. Whatever the mechanism is, it has to be obvious from looking at the directory.

## Decision

Each price file carries an `effective` date. **Prices are never edited; a change ships as a new dated file.**

Lookup takes an instant and searches files from the newest applicable backwards, so:

- A file whose `effective` date is in the future is ignored entirely.
- The newest applicable file wins for the models it mentions.
- Every other model is unaffected — a file containing one model supersedes exactly that one price.

The shipped book demonstrates it with the real Sonnet 5 change: `anthropic.yaml` (effective 2026-07-01) carries the introductory rate, and `anthropic-2026-09-01.yaml` carries the standard one. There is a test asserting the switch happens on the correct day, driven by the shipped data rather than a fixture.

A failed reload leaves the previous prices in place. Continuing to charge yesterday's rates is far better than a gateway that stops pricing because somebody committed malformed YAML.

## Consequences

- Historical spend stays explainable: the price in force at any past instant is recoverable by asking the book for that instant.
- A scheduled change can be committed in advance and takes effect on its own. No deploy, no cron job, no forgetting.
- The mechanism is visible from `ls pricing/` — dated filenames are self-describing to a contributor who has read nothing else.
- The directory grows over time. Files are small, and pruning ones older than the ledger's retention would be safe if it ever mattered.
- Lookup is a scan backwards through files rather than a map hit. With a handful of files and a map per provider inside each, this is not a hot-path concern; it happens once per request, not per token.

## Options rejected

- **Edit prices in place, with git history as the record.** Superficially fine, and it makes every past ledger entry unverifiable without a `git blame` per row. History is not queryable by the running system, which is the point.
- **A per-model `effective` list inside one file.** Keeps everything in one place, at the cost of a much more complex schema for the file contributors are most likely to be editing. The one-price-per-entry shape is what makes a price book PR a one-line diff.
- **Store prices in the database and manage them through an API.** Turns a data file that anyone can send a pull request against into an operational task. It would also put price history behind the same immutability question the ledger already answers, for no gain.
- **Fetch prices from vendor APIs at runtime.** No vendor offers a machine-readable price feed, and depending on one would make the gateway's pricing unavailable whenever the vendor's marketing site is.
