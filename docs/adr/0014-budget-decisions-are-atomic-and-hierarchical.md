# ADR-0014: Budget decisions are atomic and hierarchical

**Status:** Accepted

## Context

A reservation decision reads settled spend and existing holds, compares them
with a budget, and writes a new hold. Separate store calls make the obvious
implementation wrong: concurrent requests can both read the same balance and
both reserve it. Parent runs also have to constrain all descendants, otherwise
two sibling sub-agents can each spend the parent's full remaining budget.

## Decision

The store exposes one reserve operation. It checks the target run and every
budgeted ancestor, counting ledger entries and pending reservations across each
run's full descendant subtree, and conditionally inserts the hold in the same
transaction.

Zero means “no configured ceiling”, matching the existing dashboard semantics.
Observe mode inserts the hold even when the decision would have blocked;
enforce mode does not.

Settlement also joins the ledger append and reservation transition in one
transaction, with a unique reservation-to-ledger link making retries
idempotent.

## Consequences

- Concurrent requests cannot oversubscribe one run or a shared parent.
- Store backends own transaction and locking details rather than leaking them
  into the gateway.
- The reserve result has to identify the limiting run as well as its spend,
  holds, request and shortfall so the `402` can explain the decision.
- PostgreSQL must provide the same serialization guarantee when that backend
  is implemented.

## Rejected alternatives

**Sum spend, sum holds, then insert through three store methods.** Each call is
correct alone and the combination races.

**A Go mutex in the gateway.** It does not cover multiple gateway processes
and makes correctness depend on callers remembering the lock.

**Check only the leaf run.** That breaks the documented rule that budget flows
down and lets siblings overspend their parent.
