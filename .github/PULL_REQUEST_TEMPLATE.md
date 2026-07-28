<!--
Thanks for contributing. The "why" section is the one we actually read closely —
the diff already tells us what changed.
-->

## Why

<!-- What problem does this solve? What breaks or stays broken without it? -->

## What changed

<!-- Brief summary. Skip anything obvious from the diff. -->

## Checklist

- [ ] Tests included, table-driven where there is more than one case
- [ ] Docs updated in this PR (a user-facing change without docs is not finished)
- [ ] Every new exported symbol has a doc comment
- [ ] `make lint` and `make test` pass locally
- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
- [ ] An ADR is included in `docs/adr/` if this made a judgment call not covered by existing docs

## For price book changes only

- [ ] Link to the vendor's public pricing page: <!-- required -->
- [ ] `effective` is the date the price actually takes effect, not today
- [ ] Existing dated entries were added alongside, not overwritten

## Scope

- [ ] This does not implement anything from the [out-of-scope list](../CONTRIBUTING.md#things-we-are-deliberately-not-building)
