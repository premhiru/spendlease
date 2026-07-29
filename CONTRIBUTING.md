# Contributing to spendlease

Thanks for being here. This project is meant to be easy to contribute to. If any step below is harder than it reads, that is a bug in this document and we want to hear about it.

## Clone to green tests

Four commands. Go 1.25+ is the only prerequisite — see [ADR-0004](docs/adr/0004-go-version-floor.md) for why the floor is there.

```bash
git clone https://github.com/premhiru/spendlease.git
cd spendlease
make setup
make test
```

`make setup` installs the dev tooling (`golangci-lint`) and downloads modules. `make test` runs the suite with the race detector on, exactly as CI does. If you do not have a C toolchain, `make test-short` skips the race detector.

To build and start the gateway locally:

```bash
make run
```

Then open <http://localhost:4000>.

## Price book updates

**This is the best place to start, and it is the most valuable contribution to the project.**

Vendor prices change constantly. Nobody maintains a normalized cost table across every vendor an agent might call, which is exactly why this one matters. Updating it needs no Go, no tests, and no understanding of the gateway internals.

The price book lives in [`/pricing`](pricing/) as plain YAML:

```yaml
version: 1
effective: 2026-07-01
providers:
  openai:
    source: https://developers.openai.com/api/docs/pricing
    models:
      gpt-4o:
        input_per_1m: 2.50      # USD per 1M input tokens
        output_per_1m: 10.00    # USD per 1M output tokens
        default_max_tokens: 4096
```

`source` is required: a price without a link to the vendor's pricing page cannot be reviewed. `default_max_tokens` is the output ceiling assumed when a request does not specify one — a *reservation* default, not the model's output limit.

To submit an update:

1. **A price that has already changed:** edit the entry under `/pricing`.
2. **A price that changes on a future date:** add a new file named for that date, such as `anthropic-2026-09-01.yaml`, containing only the models that change. Never overwrite an old price. The loader ignores a future-dated file until its date arrives, then applies it to those models only, leaving everything else alone. Past ledger entries have to stay explainable, and they can only be explained against the price that was in force when they were written.
3. Set `effective` to the date the price actually takes effect, not today's date.
4. **Link the vendor's public pricing page in your PR description.** This is the one hard requirement.
5. Run `make test`.

The test suite validates the shipped price book, so a mistake fails CI rather than reaching an invoice. It catches a missing `source`, an absent or non-positive `default_max_tokens`, a rate that is not a number, output priced below input, and prices large enough to suggest a units error — such as a per-thousand-token price entered into a per-million field.

Full format reference, including how unknown models are priced: [docs/pricing-book.md](docs/pricing-book.md).

Issues labelled [`good first issue`](https://github.com/premhiru/spendlease/labels/good%20first%20issue) are mostly price book updates and small provider adapters. Pick one up without asking.

## Working on code

### Branches and commits

`main` is protected. Everything goes through a pull request from a feature branch.

| Prefix | For |
|---|---|
| `feat/` | New capability |
| `fix/` | Bug fix |
| `docs/` | Documentation only |
| `chore/` | Tooling, CI, dependencies |

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `feat(pricing): add gemini price book`.

### What a complete PR looks like

- CI passes: `go test -race`, `golangci-lint`, `go vet`, and `gofmt`.
- Tests included. Table-driven where there is more than one case.
- Docs updated in the same PR. A user-facing change without documentation is not finished.
- Every exported symbol has a doc comment.
- The description explains **why**, not just what. What is already in the diff.

### Architecture decisions

If you have to make a judgment call this document does not cover, write it down as an ADR in [`docs/adr/`](docs/adr/) in the same PR rather than deciding silently. Copy the format of an existing one. A short honest ADR beats a long agonised one: record the options you rejected and why, because that is the part nobody can reconstruct later.

### Things we are deliberately not building

Please check [the "what it does not do" list](README.md#what-it-does-not-do) before starting something large. Reconciliation, ERP export, RBAC, multi-currency, payment rails, approval workflows, anomaly detection, least-cost routing, multi-tenancy, SSO, charts in the dashboard, and framework-specific integrations are all out of scope on purpose. Several are good products, just not this one and not yet. A PR implementing one of them will be declined however good it is, so please open a discussion first and save yourself the work.

## Security

Do not open a public issue for a vulnerability. Follow [SECURITY.md](SECURITY.md).

## Code of conduct

Participation is governed by the [Contributor Covenant](CODE_OF_CONDUCT.md).
