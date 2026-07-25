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

    problems = []

    # 每个 OpenWrt 版本必须恰好一个 LuCI 包, 否则那个版本的用户没有界面可装。
    for release in releases:
        found = [n for n in luci_files if f"openwrt-{release}." in n]
        if len(found) != 1:
            problems.append(
                f"OpenWrt {release}: found {len(found)} LuCI packages, want exactly 1"
            )

    # 逐个 release 记录实际产出的架构, 用来确认没有 job 悄悄丢了产物。
    def arch_set(names, prefix):
        found = set()
        for name in names:
            parts = name[len(prefix) + 1:].split("_")
            if len(parts) < 3:
                continue
            release = parts[-1].rsplit(".", 1)[0]
            if release.startswith("openwrt-"):
                release = release[len("openwrt-"):]
            found.add((release, "_".join(parts[1:-1])))
        return found

    main_arches = arch_set(main_files, args.main)
    if not main_arches:
        problems.append(f"no {args.main} package was collected at all")

    # 期望的架构总数由矩阵决定; 这里至少保证每个 OpenWrt 版本都有产物。
    for release in releases:
        if not any(r == release for r, _ in main_arches):
            problems.append(f"OpenWrt {release}: no {args.main} package was collected")
    if problems:
        for problem in problems:
            print(f"ERROR: {problem}", file=sys.stderr)
        raise SystemExit(1)

    # 资产名里只有 OpenWrt 大版本(24.10), 因为点版本会随上游发布漂移而文件名
    # 需要可预期。精确点版本和提交记在这里, 否则 --clobber 之后没有任何东西
    # 能说明某个 .apk 到底是拿哪个 SDK 编的。
    build_info = []
    for root, _dirs, files in os.walk(args.input):
        for name in sorted(files):
            if name.startswith("BUILD_INFO_") and name.endswith(".txt"):
                with open(os.path.join(root, name), encoding="utf-8") as handle:
                    build_info.append(handle.read().rstrip("\n"))
    if build_info:
        manifest = os.path.join(args.output, "BUILD_MANIFEST.txt")
        with open(manifest, "w", encoding="utf-8") as handle:
            handle.write("\n\n".join(sorted(build_info)) + "\n")
        print(f"  wrote {os.path.basename(manifest)} with {len(build_info)} build records")


if __name__ == "__main__":
    main()
