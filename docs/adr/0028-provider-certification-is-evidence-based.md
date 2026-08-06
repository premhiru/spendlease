# 28. Provider certification is evidence-based

## Status

Accepted.

## Context

Several vendors accept the OpenAI wire format, but compatibility is not a
permanent property. Streaming terminal chunks, cache counts, and reasoning
counts differ even when the request endpoint looks identical. Calling a route
supported without recording the evidence and date hides that drift risk.

Live tests alone are also a poor default. They cost money, require repository
secrets, depend on external availability, and are unsuitable for every pull
request.

## Decision

Provider documentation distinguishes two certification levels:

- Native providers have a dedicated adapter, gateway integration tests, and
  runnable SDK examples.
- Compatible providers use the shared OpenAI-compatible adapter and must have
  dated, vendor-documented fixtures for non-streaming and streaming usage. The
  fixtures cover every reported billing dimension the adapter consumes.

Every fixture records its vendor source and review date. Deterministic fixture
tests run in normal CI. A separate weekly workflow performs streaming and
non-streaming calls only for provider secrets the repository owner chooses to
configure. An empty live run is explicitly reported as skipped rather than
treated as fresh certification.

Reasoning-token handling uses the vendor's reported total as an invariant. If
reasoning exactly fills the gap between prompt plus completion and the total,
it is additional billed output. Otherwise it is already included in the
completion count and is not added again.

## Consequences

- A shared protocol no longer means untested accounting behavior.
- Certification dates can become visibly stale and prompt a review.
- Pull-request CI remains deterministic and does not need paid credentials.
- Live drift detection is available without forcing maintainers to fund every
  provider.
- Fixture certification does not replace comparison with a real vendor bill
  before enabling enforcement for a new workload.
