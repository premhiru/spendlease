# 2. Project name and Go module path

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

The project was originally specified under the name `leash`. Before any code was written, an availability check found that name occupied in every registry the project needs, and — more seriously — occupied by an active project in an overlapping category.

Checked on 2026-07-28:

| Registry | `leash` | Detail |
|---|---|---|
| PyPI `leash` | Taken | v0.3.0, *"Leash – AI Agent Identity, Authorization, and Audit Layer"* |
| PyPI `leash-sdk` | Taken | v0.4.0, the exact package name the SDK milestone needed |
| npm `leash` | Taken | v1.0.0, a dormant MongoDB service handler |
| npm `@leash/sdk` | Taken | v0.4.4, active publisher, the exact scope the SDK milestone needed |
| GitHub `chadeckles/leash` | Exists | Apache-2.0, *"API-layer policy engine for authorization, audit... deny-by-default, simple YAML rules, and tamper-evident logs"* |

The GitHub project is the real problem rather than the registries. It is not merely a name collision: it shares the category, the license, and even the tamper-evident-log primitive that this project's ledger is built around. It has one star and was created and last pushed on the same day (2026-04-06), so it is not an active competitor — but it does own the search results for "leash ai agent", and a user who finds the wrong one is a user lost silently.

The two SDK package names were unavailable at any price, since both are held by an active publisher.

## Decision

The project is named **`spendlease`**. The Go module path is `github.com/premhiru/spendlease`.

Every identifier derives from it consistently:

| | |
|---|---|
| Binary and CLI | `spendlease` |
| Module | `github.com/premhiru/spendlease` |
| Image | `ghcr.io/premhiru/spendlease` |
| Principal key prefix | `slk_` |
| Lease token prefix | `sll_` |
| Master key env var | `SPENDLEASE_MASTER_KEY` |
| Python SDK | `spendlease` |
| TypeScript SDK | `@spendlease/sdk` |

All of these were verified free on npm, the npm scope, PyPI, and GitHub at the time of writing.

Two things recommend the name beyond availability. "Lease" is the actual core object in the identity model, so the name describes the mechanism rather than gesturing at a metaphor. And "spend" narrows the claim: the crowded space is generic agent *security* and *guardrails*, while this project does one specific thing — authorize and account for money.

## Consequences

- The name is settled before PR #1, so nothing has to be renamed later. Changing a Go module path after publication breaks every downstream import.
- The `leash` metaphor in prose ("keep your agents on a leash") is not available as a tagline without pointing at somebody else's project. The README leads with the mechanism instead.
- `spendlease` is slightly longer to type than `leash` as a CLI verb. Accepted; it is typed at setup time, not in a loop.
- If the project later moves to a dedicated GitHub organisation, the module path changes and that is a breaking change for importers. It should happen before 1.0 or not at all.

## Options rejected

- **Keep `leash`, rename only the SDK packages** (`leash-gateway` on PyPI, `@leash-gateway/sdk` on npm). Technically viable, since repository names only need to be unique within an owner. Rejected because it leaves the project sharing a name, a category, and a license with an existing project, and because inconsistent naming between the binary and its own SDKs is a permanent papercut in every quickstart.
- **`spendleash`.** Free everywhere and keeps the metaphor. Rejected as a near-homograph of `spendlease` — the two differ by one letter and would be mistyped in `pip install` and `docker pull` forever.
- **Single dictionary words** (`purser`, `bridle`, `curb`, `rein`, `ration`, `scrip`, `tollgate`). All taken on npm, PyPI, or GitHub, or all three.
- **`agentpurse`, `spendward`.** Free, but "purse" and "ward" describe custody rather than authorization, which is the wrong emphasis for a system whose core operation is issuing a scoped, expiring grant.
