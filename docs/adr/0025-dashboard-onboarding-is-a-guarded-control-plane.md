# 25. Dashboard onboarding is a guarded control plane

- **Status:** Accepted
- **Date:** 2026-08-05
- **Amends:** [ADR-0013](0013-the-dashboard-is-one-table.md), [ADR-0018](0018-version-the-operator-control-plane.md), [ADR-0021](0021-named-operators-rbac-and-audit.md)

## Context

The first useful request required several CLI commands: store a vendor key,
create a principal, copy its bootstrap key, create a run, issue a lease, and
copy the lease into an application. Each command was individually documented,
but the sequence made the safest credential (`sll_...`) the hardest one for a
new user to reach.

The dashboard already changes enforcement and invokes the principal kill
switch. Adding setup controls therefore does not create a new trust boundary,
but it does increase the number of secret-bearing and state-changing browser
requests inside that boundary.

## Decision

The embedded dashboard provides guided agent setup and access management:

- an admin creates a principal, root run, and first scoped lease in one form;
- an operator creates and closes runs and issues or revokes individual leases;
- an admin stores, replaces, or removes provider credentials; and
- plaintext lease tokens are rendered only in the successful issue response.

The three onboarding records are inserted in one datastore transaction. The
dashboard discards the new principal's compatibility key instead of placing
another long-lived secret in the browser. A legacy integration that still
needs an `slk_...` key must use the CLI.

Provider credential access is write-only. The dashboard can list configured
provider names but its interface has no method that returns a decrypted key.
Form bodies are bounded, responses are `no-store`, HTML escaping remains on,
and every mutation uses the existing role guard, same-origin anti-CSRF header,
and operator audit trail.

The `/admin/...` routes return HTML fragments and are not an automation API.
The versioned JSON API and CLI remain the supported programmatic surfaces.

## Consequences

The first useful agent can be created from one page without weakening the
lease model. Provider keys and issued leases still cannot be recovered from a
later page load or database backup.

The dashboard is no longer literally one table, although the operational
summary remains a single spend-sorted table with no charts. It now also has
small control panels for tasks that otherwise block first use.

An operator can manage access but cannot create identities, change policy, use
the principal-wide kill switch, or manage vendor credentials. Those actions
remain admin-only.
