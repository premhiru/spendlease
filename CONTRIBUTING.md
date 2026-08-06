# Contributing to spendlease

This guide covers the development setup, project conventions, and the checks a
pull request is expected to pass. If a command is incomplete or out of date,
please open an issue or fix it with the change that exposed the problem.

## Clone to green tests

You need Go 1.25.12 or later, Git, and Make. The full race test also needs a C
toolchain. See [ADR-0004](docs/adr/0004-go-version-floor.md) and its security
patch amendment in [ADR-0023](docs/adr/0023-release-artifacts-are-verifiable.md)
for the Go version decision.

```bash
git clone https://github.com/premhiru/spendlease.git
cd spendlease
make setup
make test
```

`make setup` installs `golangci-lint` and downloads Go modules. `make test`
runs the Go suite with the race detector. If you do not have a C toolchain,
`make test-short` runs the same packages without race detection.

CI also tests the SDKs and documentation. Install Python 3.12 and Node.js 22
when changing those areas, then run:

```bash
PYTHONPATH=sdk/python/src python -m unittest discover -s sdk/python/tests
(cd sdk/typescript && npm ci && npm test)
python -m pip install -r docs/requirements.txt
mkdocs build --strict
python scripts/release_preflight.py
```

The release preflight keeps the Python, npm, tag, changelog, repository, and
packaged-license metadata aligned. Run it for any SDK, packaging, or release
workflow change, even when the declared version stays the same.

To build and start the gateway locally:

```bash
make run
```

Then open <http://localhost:4000>.

## Price book updates

Vendor prices change independently of the code. Updating the price book is a
small, reviewable contribution that does not require changing Go.

The price book lives in [`/pricing`](pricing/) as plain YAML:

```yaml
version: 1
effective: 2026-07-01
verified: 2026-08-06
providers:
  openai:
    source: https://developers.openai.com/api/docs/pricing
    models:
      gpt-4o:
        input_per_1m: 2.50      # USD per 1M input tokens
        output_per_1m: 10.00    # USD per 1M output tokens
        default_max_tokens: 4096
```

`source` is required: a price without a link to the vendor's pricing page cannot be reviewed. `verified` records when every entry in that file was last compared with the source. `default_max_tokens` is the output ceiling assumed when a request does not specify one — a *reservation* default, not the model's output limit.

To submit an update:

1. Add a file named for the effective date, such as
   `anthropic-2026-09-01.yaml`, containing only the models that change. Do this
   for an already-active change as well as a scheduled one; do not overwrite
   an older effective price.
2. Set `effective` to the date the price actually takes effect, not today's
   date. A future-dated file is ignored until that date.
3. Set `verified` to the date you checked every entry in the new file.
4. Link the vendor's public pricing page in the PR description.
5. Run `spendlease pricing verify` and `make test`.

The test suite validates the shipped price book, so a mistake fails CI rather than reaching an invoice. It catches a missing `source`, an absent or non-positive `default_max_tokens`, a rate that is not a number, output priced below input, and prices large enough to suggest a units error — such as a per-thousand-token price entered into a per-million field.

Full format reference, including how unknown models are priced: [docs/pricing-book.md](docs/pricing-book.md).

Issues labeled [`good first issue`](https://github.com/premhiru/spendlease/labels/good%20first%20issue)
are intended to be self-contained. Comment on an issue if its scope is unclear
or if someone else may already be working on it.

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

If a change makes an architectural tradeoff that future contributors will need
to understand, add an ADR under [`docs/adr/`](docs/adr/). Follow an existing
record and include the alternatives that were considered. Routine
implementation choices do not need an ADR.

### Things we are deliberately not building

Check [the "what it does not do" list](README.md#what-it-does-not-do) before
starting a large feature. Open a discussion before working on an item listed
there so its scope can be agreed before implementation.

## Security

Do not open a public issue for a vulnerability. Follow [SECURITY.md](SECURITY.md).

## Code of conduct

Participation is governed by the [Contributor Covenant](CODE_OF_CONDUCT.md).
