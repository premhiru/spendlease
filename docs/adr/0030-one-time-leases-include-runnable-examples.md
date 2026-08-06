# 30. One-time leases include runnable examples

## Status

Accepted.

## Context

Creating an agent in the dashboard removed the CLI setup sequence, but the
result still left users to translate a lease and provider URL into SDK
configuration. That is especially error-prone for Anthropic authentication,
prefixed compatible-provider routes, and strict mode's output ceiling.

## Decision

The one-time response that creates or issues a lease includes copyable
environment values, dependency installation, Python, JavaScript, and `curl`
examples for every selected provider. Anthropic uses its native SDK and
`x-api-key`; OpenAI and the five compatible providers use the OpenAI client
shape and bearer authentication. Every sample supplies an explicit output
limit.

The examples are rendered only with the plaintext lease result. They are not
stored, reconstructed, or returned by later dashboard reads. Refreshing the
page therefore removes both the secret and the prefilled environment block.

## Consequences

- A new user can make a bounded first request without translating prose.
- Examples match the exact host and provider prefix used to create the lease.
- Multiple selected providers remain manageable through collapsed sections.
- The HTML contains additional copies of a secret during its one-time display,
  so the response remains `no-store` and must be treated as sensitive.
