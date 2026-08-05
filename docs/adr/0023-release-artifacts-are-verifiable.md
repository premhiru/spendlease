# ADR-0023: Release artifacts are independently verifiable

**Status:** Accepted

## Context

A checksum catches a damaged download, but a checksum published beside the
file does not prove who built either one. Mutable container tags have the same
problem. A compromised action tag or base-image tag can also change a release
without changing this repository.

The project publishes five binaries, a multi-platform image, and two SDK
packages. The release process needs one trust model that works for all of them
without long-lived signing keys in repository secrets.

## Decision

Release builds use only actions pinned to full commit SHAs. Docker build and
runtime images are pinned by digest, with Dependabot responsible for proposing
reviewable updates. The Go patch version in `go.mod` is a security floor rather
than an unpinned major/minor preference.

Every binary release includes a SHA-256 checksum, an SPDX JSON SBOM, signed
SLSA provenance, and a signed SBOM attestation. The workflow stores the
Sigstore bundles beside the binary so verification does not depend on an
attestation API being available later. Python and npm release archives receive
checksums and signed build provenance. The container image carries BuildKit
max-level provenance and an SPDX SBOM in the registry, plus a GitHub artifact
attestation bound to its immutable digest.

GitHub Actions mints short-lived OIDC identities for signing. There is no
project signing key to copy, rotate, or expose. Consumers verify the repository,
release workflow, source tag, artifact digest, and predicate type rather than
trusting a filename or mutable tag.

Pull requests also run reachable-call analysis against the Go vulnerability
database, reject newly introduced high-severity dependency findings, and scan
the actual runtime image for high or critical vulnerabilities. These checks run
again on `main` and on a weekly schedule where applicable.

## Consequences

- A release consumer can verify provenance online through GitHub or from the
  attached Sigstore bundle with a separately obtained trusted root.
- Container deployments can pin a digest and enforce an attestation policy.
- Updating an action, compiler, or base image is a visible dependency change.
- Release jobs need `id-token: write` and `attestations: write`; the image job
  additionally needs `packages: write`.
- An attestation proves origin and build inputs, not that the software is free
  of defects. Vulnerability gates and review remain necessary.

## Rejected alternatives

**Checksums only.** They detect corruption but do not authenticate the builder
or source revision.

**A long-lived private signing key.** It adds secret distribution and rotation
work and makes the repository secret store part of the release trust root.

**Mutable action and image tags.** They are convenient for maintenance but let
upstream content change between review and execution. Dependabot preserves the
maintenance path while keeping each merged workflow immutable.
