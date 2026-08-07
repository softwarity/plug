#!/usr/bin/env bash
# The three e2e families must run THE SAME TESTS, and you must be able to SEE it.
#
# e2e-compose, e2e-swarm and e2e-k8s exist to prove that plug behaves the same
# whichever backend provisions a name. That only reads as a comparison if the
# three step lists are identical — same cells, same order, same names. They had
# drifted: the same test appeared as "park the deployed service", "scale the
# deployed service to 0" and "repoint the deployed Service" (three names for one
# assertion, differing only in how the backend does it), the order diverged, and
# two cells ran on compose alone for no reason anyone could point at — the
# cluster resources they need are declared in all three families.
#
# Drift is silent: nothing fails when one family quietly stops running a cell.
# This does.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
ci="${1:-$root/.github/workflows/ci.yml}"

python3 - "$ci" <<'PY'
import sys, yaml

jobs = ("e2e-compose", "e2e-swarm", "e2e-k8s")
d = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))

def block(job):
    # Every step that RUNS something — the cells. `uses:` steps are the runner's
    # own setup (checkout, tailnet, artifacts) and legitimately differ.
    return [(s.get("name"), s.get("if"), " ".join(str(s.get("run", "")).split()))
            for s in d["jobs"][job]["steps"] if "run" in s]

ref = block(jobs[0])
if len(ref) < 15:
    sys.exit(f"only {len(ref)} cells parsed from {jobs[0]} — the extraction is broken, not the workflow")

bad = False
for job in jobs[1:]:
    got = block(job)
    if got == ref:
        continue
    bad = True
    print(f"\n{job} does not run the same block as {jobs[0]}:")
    for i in range(max(len(ref), len(got))):
        a = ref[i] if i < len(ref) else None
        b = got[i] if i < len(got) else None
        if a == b:
            continue
        print(f"  step {i + 1}:")
        print(f"    {jobs[0]}: {a[0] if a else '(nothing)'}")
        if a and b and a[0] == b[0]:
            if a[1] != b[1]: print(f"      if:  {a[1]!r} vs {b[1]!r}")
            if a[2] != b[2]: print(f"      run: {a[2]!r} vs {b[2]!r}")
        else:
            print(f"    {job}: {b[0] if b else '(nothing)'}")

if bad:
    sys.exit("\nThe three families must run the same cells, in the same order, "
             "under the same names — a family-specific step belongs BELOW the "
             "common block, under its own heading.")
print(f"the three e2e families run the same {len(ref)} cells, in the same order")
PY
