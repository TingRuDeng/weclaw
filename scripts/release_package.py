#!/usr/bin/env python3
"""Seal local release artifacts and reject source or artifact drift before upload."""

import hashlib
import json
import re
import sys
from pathlib import Path


ASSETS = ("weclaw_darwin_arm64", "weclaw_linux_amd64", "checksums.txt")


def fingerprint(directory):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("package must be a real directory")
    if {path.name for path in directory.iterdir()} != set(ASSETS):
        raise ValueError("unexpected or missing package assets")
    result = {}
    for name in ASSETS:
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"asset must be a regular file: {name}")
        with path.open("rb") as stream:
            digest = hashlib.sha256()
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
            result[name] = digest.hexdigest()
    checksums = {}
    for line in (directory / "checksums.txt").read_text(encoding="ascii").splitlines():
        digest, name = line.split()
        if name in checksums or name not in ASSETS[:2]:
            raise ValueError("invalid or duplicate checksum entry")
        checksums[name] = digest
    if checksums != {name: result[name] for name in ASSETS[:2]}:
        raise ValueError("asset checksum mismatch")
    return result


def main():
    action, path, version, commit = sys.argv[1:]
    if action not in ("seal", "verify"):
        raise ValueError("expected seal or verify")
    if not re.fullmatch(r"v\d+\.\d+\.\d+", version):
        raise ValueError("invalid package version")
    if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", commit):
        raise ValueError("invalid package commit")
    directory = Path(path)
    manifest = directory.with_name(directory.name + ".package.json")
    expected = {"schema": 1, "version": version, "commit": commit,
                "assets": fingerprint(directory)}
    if action == "seal":
        with manifest.open("x", encoding="utf-8") as stream:
            json.dump(expected, stream, sort_keys=True, indent=2)
            stream.write("\n")
    else:
        if manifest.is_symlink() or not manifest.is_file():
            raise ValueError("missing regular package manifest")
        if json.loads(manifest.read_text(encoding="utf-8")) != expected:
            raise ValueError("package version, commit or asset digest changed")
    print(f"Package {action}: {version} ({commit[:12]})")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as error:
        sys.exit(f"Package validation failed: {error}")
