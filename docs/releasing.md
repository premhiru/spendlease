# Releasing

This page is for maintainers. Releases are tag-driven and use
`.github/workflows/release.yml`.

## One-time registry setup

The workflow uses short-lived GitHub OIDC credentials; do not add long-lived
PyPI or npm tokens to repository secrets.

For PyPI, create a pending trusted publisher for project `spendlease` with:

- owner `premhiru`;
- repository `spendlease`;
- workflow `release.yml`; and
- environment `pypi`.

PyPI can create a project from a pending publisher, but the pending record does
not reserve the name. Configure it immediately before the release and confirm
that `https://pypi.org/project/spendlease/` is still unclaimed. Follow PyPI's
[pending publisher procedure](https://docs.pypi.org/trusted-publishers/creating-a-project-through-oidc/).

For npm, first make sure the `@spendlease` scope is controlled by the project
maintainer. npm configures trusted publishers from an existing package's
settings; it does not provide PyPI's pending-publisher flow. If the package has
never been published, claim it with a reviewed bootstrap prerelease such as
`0.2.0-beta.0`, using an interactive npm login with 2FA and the `beta` dist-tag.
Do this from a temporary clean checkout and do not change the release branch.
Then configure trusted publishing for package `@spendlease/sdk`, repository
`premhiru/spendlease`, workflow `release.yml`, environment `npm`, and allowed
action `npm publish`. npm requires CLI 11.5.1 or later and Node 22.14.0 or
later for OIDC publishing; the workflow checks this before packing. The fields
and current constraints are documented in npm's
[trusted publishing guide](https://docs.npmjs.com/trusted-publishers/).

Protect the `pypi` and `npm` GitHub environments so only an authorized
maintainer can approve publication. Once each registry form has been saved and
checked, set the corresponding repository variable:

```bash
gh variable set PYPI_TRUSTED_PUBLISHER_READY --body true
gh variable set NPM_TRUSTED_PUBLISHER_READY --body true
```

Keep either variable false until its registry is ready. The release preflight
runs before any container, binary, or package publication and refuses a tag
when either value is not exactly `true`.

## Prepare a release

1. Update the Python version in `sdk/python/pyproject.toml` and
   `sdk/python/src/spendlease/__init__.py`.
2. Update the TypeScript version in `sdk/typescript/package.json` and its lock
   file.
3. Use equivalent PEP 440 and SemVer versions. For example, Python `0.2.0b1`
   matches npm `0.2.0-beta.1` and Git tag `v0.2.0-beta.1`.
4. Add the version and user-visible changes to `CHANGELOG.md`, then run:

   ```bash
   python scripts/release_preflight.py --tag v0.2.0-beta.1
   ```

5. Run the full checks documented in `CONTRIBUTING.md` and merge through the
   protected `main` branch.
6. Confirm the `CI` and `Security` workflows are green. Do not waive a
   reachable Go vulnerability, dependency-review failure, or high/critical
   runtime-image finding without a documented, reviewed VEX decision.
7. Confirm both registry-ready repository variables are `true`, then create the
   signed or annotated tag from the merged `main` commit and push
   it. Do not retag a different commit after publication.

The workflow refuses to publish when either package version differs from the
tag. A prerelease tag contains a hyphen and does not move the container's
`latest` tag.

## Published artifacts

A successful tag produces:

- platform binaries and SHA-256 checksum files;
- SPDX SBOMs and signed provenance/SBOM bundles for platform binaries;
- a multi-architecture GHCR image plus a digest-pinned reference, max-level
  BuildKit provenance, an attached SPDX SBOM, and a signed digest attestation;
- a Python wheel and source distribution;
- an npm tarball;
- checksums and signed provenance for both SDK package sets; and
- PyPI and npm registry publications.

Download and retain `container-image.txt` when promoting the container. It
uses a digest rather than a mutable tag.

## Verify the release before announcing it

Download one binary and its checksum, then run:

```bash
sha256sum -c spendlease_vX.Y.Z_linux_amd64.sha256
gh attestation verify spendlease_vX.Y.Z_linux_amd64 \
  --repo premhiru/spendlease \
  --signer-workflow premhiru/spendlease/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

Verify the image by copying the digest reference from `container-image.txt`:

```bash
gh attestation verify oci://ghcr.io/premhiru/spendlease@sha256:... \
  --repo premhiru/spendlease \
  --signer-workflow premhiru/spendlease/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

Finally, install each SDK from the registry in a clean temporary project and
run its quickstart. A green publication job is not a substitute for verifying
that the public index serves the expected version.
