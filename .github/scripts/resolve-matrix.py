#!/usr/bin/env python3
"""Resolve the OpenWrt build matrix and validate it before the builds start.

Two jobs:

1. Turn a major OpenWrt version ("24.10") into the newest published point
   release ("24.10.4"), so the matrix never pins a stale release by hand.
2. Verify that every arch -> target/subtarget mapping in arch-matrix.json is
   actually true for that release, by reading the target's own profiles.json
   and comparing its "arch_packages" field.

Step 2 matters because the SDK is published per target/subtarget while the
release assets are named per CPU architecture. If OpenWrt renames a subtarget
or moves an architecture, this fails in about a minute with a precise message
instead of burning ~43 SDK builds first.
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

# Overridable so the CI self-test can point at a local fixture server.
RELEASES_INDEX = os.environ.get(
    "OPENWRT_RELEASES_INDEX", "https://downloads.openwrt.org/releases/"
)
TARGET_URL = os.environ.get(
    "OPENWRT_TARGET_URL",
    "https://downloads.openwrt.org/releases/{version}/targets/{target}/profiles.json",
)
TIMEOUT = int(os.environ.get("OPENWRT_FETCH_TIMEOUT", "30"))


def fetch(url, retries=3):
    last = None
    for attempt in range(retries):
        try:
            request = urllib.request.Request(
                url, headers={"User-Agent": "singbox-formula-ci"}
            )
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                return response.read().decode("utf-8", "replace")
        except (urllib.error.URLError, OSError) as err:
            last = err
    raise SystemExit(f"could not fetch {url}: {last}")


def latest_point_release(major):
    """Pick the highest x.y.z published under the given x.y major version."""
    body = fetch(RELEASES_INDEX)
    pattern = re.compile(r"\b" + re.escape(major) + r"\.(\d+)\b")
    found = sorted({int(m.group(1)) for m in pattern.finditer(body)})
    if not found:
        raise SystemExit(
            f"no published point release found for OpenWrt {major}; "
            f"checked {RELEASES_INDEX}"
        )
    return f"{major}.{found[-1]}"


def verify_target(version, target, expected_arch):
    """Confirm the target really produces packages for expected_arch."""
    url = TARGET_URL.format(version=version, target=target)
    try:
        data = json.loads(fetch(url, retries=2))
    except SystemExit:
        return f"{target}: profiles.json not reachable ({url})"
    except json.JSONDecodeError as err:
        return f"{target}: profiles.json is not valid JSON ({err})"

    actual = data.get("arch_packages")
    if not actual:
        return f"{target}: profiles.json has no arch_packages field"
    if actual != expected_arch:
        return (
            f"{target}: builds arch_packages={actual!r}, "
            f"but the matrix expects {expected_arch!r}"
        )
    return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--matrix", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--only-version", default=None)
    parser.add_argument("--only-arch", default=None)
    parser.add_argument(
        "--skip-verify",
        action="store_true",
        help="resolve versions but do not contact the target index",
    )
    args = parser.parse_args()

    with open(args.matrix, encoding="utf-8") as handle:
        spec = json.load(handle)

    entries = []
    problems = []

    for release in spec["openwrt"]:
        major = release["version"]
        if args.only_version and major != args.only_version:
            continue

        version = latest_point_release(major)
        print(f"OpenWrt {major} -> {version}", file=sys.stderr)

        luci_arch = release.get("luci_build_arch")
        targets = release["targets"]
        if args.only_arch:
            targets = [t for t in targets if t["arch"] == args.only_arch]
            if not targets:
                raise SystemExit(
                    f"arch {args.only_arch} is not defined for OpenWrt {major}"
                )

        for target in targets:
            arch = target["arch"]
            if not args.skip_verify:
                problem = verify_target(version, target["target"], arch)
                if problem:
                    problems.append(f"OpenWrt {version}: {problem}")
                    continue
            entries.append(
                {
                    "openwrt_major": major,
                    "openwrt_version": version,
                    "arch": arch,
                    "target": target["target"],
                    # Exactly one arch per release collects the LuCI package:
                    # it is PKGARCH:=all, so every job would produce an
                    # identical file.
                    "collect_luci": "true" if arch == luci_arch else "false",
                }
            )

    if problems:
        print("\nMatrix validation failed:\n", file=sys.stderr)
        for problem in problems:
            print(f"  - {problem}", file=sys.stderr)
        print(
            "\nFix .github/arch-matrix.json (or drop the architecture) and retry.",
            file=sys.stderr,
        )
        raise SystemExit(1)

    if not entries:
        raise SystemExit("resolved an empty matrix")

    # Guard against a release silently losing its LuCI producer.
    if not args.only_arch:
        for release in spec["openwrt"]:
            major = release["version"]
            if args.only_version and major != args.only_version:
                continue
            collectors = [
                e for e in entries
                if e["openwrt_major"] == major and e["collect_luci"] == "true"
            ]
            if len(collectors) != 1:
                raise SystemExit(
                    f"OpenWrt {major} has {len(collectors)} LuCI collectors, want exactly 1 "
                    f"(check luci_build_arch in the matrix)"
                )

    with open(args.output, "w", encoding="utf-8") as handle:
        json.dump(entries, handle, separators=(",", ":"))

    print(f"\nResolved {len(entries)} build jobs:", file=sys.stderr)
    for entry in entries:
        flag = "  [+luci]" if entry["collect_luci"] == "true" else ""
        print(
            f"  {entry['openwrt_version']:10} {entry['arch']:28} {entry['target']}{flag}",
            file=sys.stderr,
        )


if __name__ == "__main__":
    main()
