#!/usr/bin/env python3
"""Flatten per-job build artifacts into a single release asset directory.

Each build job uploads an artifact containing already correctly named package
files plus a BUILD_INFO_<arch>.txt. This script copies the package files into
one flat directory, drops the per-job build info, and sanity checks the result
so a partially uploaded release is caught before it is published.
"""

import argparse
import hashlib
import os
import shutil
import sys
from collections import defaultdict

PACKAGE_EXTENSIONS = (".apk", ".ipk")


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--main", required=True)
    parser.add_argument("--luci", required=True)
    args = parser.parse_args()

    os.makedirs(args.output, exist_ok=True)

    collected = {}
    duplicates = defaultdict(list)

    for root, _dirs, files in os.walk(args.input):
        for name in sorted(files):
            if not name.endswith(PACKAGE_EXTENSIONS):
                continue
            source = os.path.join(root, name)
            if name in collected:
                # The LuCI package is PKGARCH:=all, so identical copies are
                # harmless; genuinely different files are not.
                if digest(source) == digest(collected[name]):
                    continue
                duplicates[name].append(source)
                continue
            collected[name] = source

    if duplicates:
        print("Conflicting artifacts with the same name:", file=sys.stderr)
        for name, paths in duplicates.items():
            print(f"  {name}: {collected[name]} vs {', '.join(paths)}", file=sys.stderr)
        raise SystemExit(1)

    if not collected:
        raise SystemExit(f"no package files found under {args.input}")

    for name, source in sorted(collected.items()):
        shutil.copy2(source, os.path.join(args.output, name))

    main_files = [n for n in collected if n.startswith(args.main + "_")]
    luci_files = [n for n in collected if n.startswith(args.luci + "_")]

    releases = sorted({n.rsplit("openwrt-", 1)[-1].rsplit(".", 1)[0] for n in collected})

    print(f"Collected {len(collected)} package files")
    print(f"  {args.main}: {len(main_files)}")
    print(f"  {args.luci}: {len(luci_files)}")
    print(f"  OpenWrt releases: {', '.join(releases)}")

    # Every OpenWrt release in the set must ship exactly one LuCI package,
    # otherwise users on that release have no web UI to install.
    problems = []
    for release in releases:
        found = [n for n in luci_files if f"openwrt-{release}." in n]
        if len(found) != 1:
            problems.append(
                f"OpenWrt {release}: found {len(found)} LuCI packages, want exactly 1"
            )
    if problems:
        for problem in problems:
            print(f"ERROR: {problem}", file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
