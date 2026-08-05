#!/usr/bin/env python3
"""Check that every public version source describes the same release."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
import tomllib


ROOT = pathlib.Path(__file__).resolve().parents[1]


def release_tag(python_version: str) -> str:
    stable = re.fullmatch(r"(\d+\.\d+\.\d+)", python_version)
    if stable:
        return "v" + stable.group(1)
    prerelease = re.fullmatch(r"(\d+\.\d+\.\d+)(a|b|rc)(\d+)", python_version)
    if not prerelease:
        raise ValueError(f"unsupported Python release version {python_version!r}")
    label = {"a": "alpha", "b": "beta", "rc": "rc"}[prerelease.group(2)]
    return f"v{prerelease.group(1)}-{label}.{prerelease.group(3)}"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", help="tag being released, such as v0.2.0-beta.1")
    args = parser.parse_args()

    pyproject = tomllib.loads((ROOT / "sdk/python/pyproject.toml").read_text(encoding="utf-8"))
    python_version = pyproject["project"]["version"]
    init = (ROOT / "sdk/python/src/spendlease/__init__.py").read_text(encoding="utf-8")
    init_match = re.search(r'^__version__ = "([^"]+)"$', init, re.MULTILINE)

    package = json.loads((ROOT / "sdk/typescript/package.json").read_text(encoding="utf-8"))
    lock = json.loads((ROOT / "sdk/typescript/package-lock.json").read_text(encoding="utf-8"))
    npm_version = package["version"]
    expected_tag = release_tag(python_version)
    declared_tag = "v" + npm_version

    errors: list[str] = []
    if pyproject["project"]["name"] != "spendlease":
        errors.append("Python project name must remain spendlease")
    if package["name"] != "@spendlease/sdk":
        errors.append("npm package name must remain @spendlease/sdk")
    if not init_match or init_match.group(1) != python_version:
        errors.append("Python __version__ does not match pyproject.toml")
    if lock.get("version") != npm_version or lock.get("packages", {}).get("", {}).get("version") != npm_version:
        errors.append("npm package-lock.json does not match package.json")
    if expected_tag != declared_tag:
        errors.append(f"Python {python_version} maps to {expected_tag}, but npm maps to {declared_tag}")
    if args.tag and args.tag != expected_tag:
        errors.append(f"release tag {args.tag} does not match package versions ({expected_tag})")

    changelog = (ROOT / "CHANGELOG.md").read_text(encoding="utf-8")
    if f"## [{npm_version}]" not in changelog:
        errors.append(f"CHANGELOG.md has no [{npm_version}] release section")

    repository = package.get("repository", {}).get("url")
    if repository != "git+https://github.com/premhiru/spendlease.git":
        errors.append("npm repository.url must exactly match the trusted publisher repository")
    publish = package.get("publishConfig", {})
    if publish.get("access") != "public" or publish.get("provenance") is not True:
        errors.append("npm publishConfig must require public access and provenance")
    if "-" in npm_version and publish.get("tag") != "beta":
        errors.append("npm prereleases must publish under the beta dist-tag")
    if "-" not in npm_version and "tag" in publish:
        errors.append("stable npm releases must remove the prerelease dist-tag")

    root_license = (ROOT / "LICENSE").read_bytes()
    for package_license in ("sdk/python/LICENSE", "sdk/typescript/LICENSE"):
        if (ROOT / package_license).read_bytes() != root_license:
            errors.append(f"{package_license} does not match the repository license")

    if errors:
        for error in errors:
            print(f"release preflight: {error}", file=sys.stderr)
        return 1

    print(f"release preflight: {expected_tag} metadata is consistent")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
