# 4. Go version floor is 1.25

- **Status:** Accepted
- **Date:** 2026-07-28
- **Amends:** the "Go 1.22+" statement in the original project brief

## Context

The project was specified as "Go 1.22+", and PR #1 shipped with `go 1.22` in `go.mod` and `golang:1.22-alpine` in the Dockerfile.

Phase 2 introduced the one dependency the whole storage layer rests on: `modernc.org/sqlite`, the pure-Go SQLite translation. It is not an incidental choice — it is what allows a `CGO_ENABLED=0` static binary in a distroless container with no libc, which is what makes the zero-configuration quickstart possible. Anything replacing it would take the 8MB image and the "no database to provision" promise with it.

Current versions of that driver require a newer Go than 1.22. Checked on 2026-07-28:

| `modernc.org/sqlite` | Requires |
|---|---|
| v1.34.5 | go 1.21 |
| v1.37.1 – v1.38.2 | go 1.23.0 |
| v1.44.0 | go 1.24.0 |
| v1.49.1 – v1.54.0 | go 1.25.0 |

Holding the floor at 1.22 means pinning v1.34.5 — around twenty releases and many months behind, for the component that owns credential storage and the spend ledger.

## Decision

The floor moves to **Go 1.25**. `go.mod` declares `go 1.25.0`, the Dockerfile builds on `golang:1.25-alpine`, and `CONTRIBUTING.md` states 1.25+ as the prerequisite.

This is read as satisfying the original "Go 1.22+" rather than contradicting it: the intent of that line was "a modern Go toolchain, single static binary", not "must keep working on 1.22 specifically". Recording it here rather than changing the number silently, because a contributor whose toolchain suddenly stops working deserves to find out why.

## Consequences

- The storage layer runs a current, maintained driver.
- Contributors need Go 1.25 or newer. Go 1.26 is current, so this is not a burden today, but it does exclude anyone pinned to an older toolchain by policy.
- CI resolves the toolchain from `go.mod` via `go-version-file`, so the matrix follows this automatically and cannot drift from it.
- Raising the floor again later is cheap and should be done the same way: deliberately, with the reason written down.

## Options rejected

- **Pin `modernc.org/sqlite` at v1.34.5 and keep Go 1.22.** Rejected: an old and diverging dependency in the persistence and credential path is a worse liability than a raised toolchain floor, and the gap only widens.
- **Switch to `mattn/go-sqlite3`.** Requires cgo. That means a C toolchain to build, a libc in the runtime image, no trivial cross-compilation, and the end of the distroless static container. It would trade a version floor for the project's entire distribution story.
- **Write a storage layer against an embedded key-value store instead.** Would avoid the dependency, but SQL is what makes the ledger's immutability trigger, the CHECK constraints and the aggregate queries possible in the database rather than in application code.
