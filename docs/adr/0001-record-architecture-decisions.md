# 1. Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

`spendlease` is an authorization system. Authorization systems accumulate decisions that look arbitrary from the outside and are expensive to revisit — why reservations have a TTL, why the ledger has no `UPDATE` path, why observe mode is the default. Six months from now the reasoning behind each will be reconstructible only by whoever was in the room.

That is worse for an open source project than a closed one. A contributor who cannot find the reasoning either reimplements a rejected option or, more often, does not send the PR at all.

## Decision

Every judgment call not already settled by the README or `CONTRIBUTING.md` gets an ADR in `docs/adr/`, in the same pull request that makes the call. Not afterwards, and not in a separate documentation PR.

Format is deliberately light: context, decision, consequences, and — the part that matters most — the options rejected and why. A short honest ADR beats a long agonised one. ADRs are numbered sequentially and are immutable once merged; to change a decision, write a new ADR that supersedes the old one and mark the old one `Superseded by ADR-NNNN`.

## Consequences

- Any contributor can find out why something is the way it is without asking.
- Review conversations get shorter, because the alternatives were addressed before review started.
- Some ADRs will turn out to be wrong. They stay in the tree, marked superseded. The record of a decision that did not work out is more useful than a clean history.
- Small cost per PR. Accepted.

## Options rejected

- **Decide in review comments.** Free, and completely unfindable a year later. GitHub search does not reach into resolved review threads.
- **A single `DESIGN.md`.** Turns into a merge-conflict magnet and drifts, because nobody is responsible for any specific paragraph.
- **A wiki.** Not versioned with the code, not reviewable in a PR, and invisible to anyone reading the repository offline.
