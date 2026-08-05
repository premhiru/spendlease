# Security policy

`spendlease` holds vendor API credentials and sits in the request path of production traffic. We take reports seriously and will not argue about severity before fixing something.

## Reporting a vulnerability

**Do not open a public issue.**

Use [GitHub's private vulnerability reporting](https://github.com/premhiru/spendlease/security/advisories/new), which is enabled on this repository. It routes straight to the maintainers and keeps the report confidential until a fix ships.

Please include the version or commit, what an attacker gains, and a reproduction if you have one. You will get an acknowledgement within 3 working days and an assessment within 10. We will credit you in the advisory unless you would rather we did not.

We ask for 90 days before public disclosure, and will usually be much faster than that. We will not pursue legal action against good-faith research that stays within the scope below.

## Supported versions

There is no stable release. Security fixes are applied to the current `main`
branch and the container built from it. The existing alpha tag predates the
current gateway implementation.

| Version | Supported |
|---|---|
| Current `main` / matching `sha-...` image | Yes |
| `edge` image | Yes, but mutable |
| `v0.1.0-alpha.1` | No |

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

- Vendor keys are encrypted at rest with AES-256-GCM under the configured
  primary master key. Rotation re-encrypts the complete vault transactionally.
- Principal keys (`slk_`), lease tokens (`sll_`), and named operator tokens
  (`slo_`) are stored **only** as SHA-256 hashes. Plaintext is shown once at
  creation and is not recoverable afterwards.
- Authenticated HTTP mutations produce append-only attempt and result records.
  The database rejects updates and deletes against that operator audit table.
- Key material and request bodies are never logged at default log levels.
- Metrics and alert events exclude prompts, headers, credentials, model names,
  and raw error text. Production webhooks require HTTPS and an HMAC secret;
  redirects are refused so signatures are not forwarded to another host.

If you find any of these three statements to be untrue in practice, that is a vulnerability and we want the report.

## Deployment notes

`spendlease` ships with no authentication on the operator API only when both the
TCP peer and HTTP host are loopback, because the 60-second quickstart depends
on it. Non-loopback or non-local-host access fails closed unless a named
operator exists. State-changing requests
also require `X-Spendlease-Admin: 1` and browser mutations must be same-origin.
The dashboard accepts operator tokens through HTTP Basic authentication;
scripts use Bearer authentication. `SPENDLEASE_ADMIN_TOKEN` remains only as a
logged, deprecated migration path. See [self-hosting](docs/self-hosting.md) for
the production checklist and put TLS in front of the port before exposing it
to an untrusted network.

In production, configure exactly one explicit primary key source: a direct
environment value, a mounted secret file, or a no-shell external command. The
auto-generated development key is refused when `SPENDLEASE_ENV=production`,
so a misconfigured deployment fails to start rather than quietly encrypting
vendor credentials under a key written beside the database. External command
stderr is discarded and its stdout is bounded to reduce disclosure risk.
