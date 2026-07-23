#!/usr/bin/env python3
"""Flag pipefail hazards in workflow shell blocks.

GitHub runs an explicit `shell: bash` as `bash --noprofile --norc -eo pipefail`.
Piping a producer that keeps writing (tar -tf, find, cat) into a consumer that
exits early (head, grep -q) makes the producer take SIGPIPE, and pipefail then
promotes that to a step failure. This is a silent trap because the same code is
fine in a plain job `run:` block, which defaults to `bash -e` without pipefail.
"""
import re, sys, yaml

HAZARDS = [
    (re.compile(r"\|\s*head\b"), "pipes into `head`, which exits early"),
    (re.compile(r"\|\s*grep\s+-[A-Za-z]*q"), "pipes into `grep -q`, which exits early"),
    (re.compile(r"\|\s*sed\s+-n\s+'[^']*q"), "pipes into `sed ... q`, which exits early"),
]

def run_blocks(path):
    doc = yaml.safe_load(open(path, encoding="utf-8"))
    blocks = []
    for name, job in (doc.get("jobs") or {}).items():
        for i, step in enumerate(job.get("steps") or []):
            if isinstance(step, dict) and "run" in step:
                blocks.append((f"{name} / {step.get('name', i)}", step["run"]))
    for i, step in enumerate((doc.get("runs") or {}).get("steps") or []):
        if "run" in step:
            blocks.append((f"action / {step.get('name', i)}", step["run"]))
    return blocks

problems = []
for path in sys.argv[1:]:
    for where, script in run_blocks(path):
        for lineno, line in enumerate(script.splitlines(), 1):
            if line.lstrip().startswith("#"):
                continue
            for pattern, why in HAZARDS:
                if pattern.search(line):
                    problems.append(f"{path}: {where}: line {lineno} {why}\n      {line.strip()}")

if problems:
    print("pipefail hazards found:")
    for p in problems:
        print("  - " + p)
    sys.exit(1)
print("no pipefail hazards")
