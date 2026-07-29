# Security policy

`spendlease` holds vendor API credentials and sits in the request path of production traffic. We take reports seriously and will not argue about severity before fixing something.

## Reporting a vulnerability

**Do not open a public issue.**

Use [GitHub's private vulnerability reporting](https://github.com/premhiru/spendlease/security/advisories/new), which is enabled on this repository. It routes straight to the maintainers and keeps the report confidential until a fix ships.

Please include the version or commit, what an attacker gains, and a reproduction if you have one. You will get an acknowledgement within 3 working days and an assessment within 10. We will credit you in the advisory unless you would rather we did not.

We ask for 90 days before public disclosure, and will usually be much faster than that. We will not pursue legal action against good-faith research that stays within the scope below.

## Supported versions

Pre-1.0, only the latest minor release receives security fixes.

| Version | Supported |
|---|---|
| 0.1.x | Yes |
| < 0.1 | No |

## Scope

In scope:

- Credential disclosure from the vault, in logs, in error responses, or over the wire
- Any bypass of budget enforcement, lease scoping, TTL expiry, or the revocation set
- Ledger tampering, or breaking the hash chain without detection
- Authentication or authorization bypass on the admin API or dashboard
- Injection, SSRF, or path traversal through proxied requests

Out of scope:

- Findings that require an already-compromised `SPENDLEASE_MASTER_KEY` or host
- Denial of service by sheer request volume against your own deployment
- Vulnerabilities in upstream vendor APIs
- Missing hardening headers on the dashboard with no demonstrated impact

## What we guarantee about credentials

- Vendor keys are encrypted at rest with AES-256-GCM under `SPENDLEASE_MASTER_KEY`.
- Principal keys (`slk_`) and lease tokens (`sll_`) are stored **only** as SHA-256 hashes. Plaintext is shown once at creation and is not recoverable afterwards: not by us, not by an admin, not from the database.
- Key material and request bodies are never logged at default log levels.

If you find any of these three statements to be untrue in practice, that is a vulnerability and we want the report.

## Deployment notes

`spendlease` ships with no authentication on the admin API when bound to loopback, because the 60-second quickstart depends on it. **Do not expose port 4000 to an untrusted network without putting authentication in front of it.** See [self-hosting](docs/self-hosting.md) for the production checklist.

This matters more than it did before the dashboard existed. `POST /admin/principals/{id}/mode` changes state and is unauthenticated: anyone who can reach the port can switch a principal back to observe mode and **disable enforcement for it**. The dashboard prints a warning on the page when the gateway is not bound to loopback, but a warning is not a control. Put authentication in front of it.

In production, `SPENDLEASE_MASTER_KEY` must be set explicitly. The auto-generated development key is refused when `SPENDLEASE_ENV=production`, so a misconfigured deployment fails to start rather than quietly encrypting your vendor credentials under a key that was written to disk beside them.
