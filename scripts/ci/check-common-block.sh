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

# The keys that decide whether a cell COUNTS. Comparing only (name, if, run) let
# a family keep the right name and the right command while quietly disarming it:
# `continue-on-error: true` on one cell made it unable to fail the leg, and
# `shell: pwsh` on a `bash scripts/...` line made it unable to run at all. Both
# left ci-shape printing "the three e2e families run the same 19 cells" with
# exit 0, which is the silent drift this script exists to catch. `env`,
# `timeout-minutes` and `working-directory` are here for the same reason: a cell
# that gets a different environment, a different budget or a different cwd in one
# family is not the same assertion, however identical its command reads.
COUNTS = ("name", "if", "run", "shell",
          "continue-on-error", "timeout-minutes", "env", "working-directory")

def cell(s):
    c = {k: s.get(k) for k in COUNTS if k in s}
    c["run"] = " ".join(str(s.get("run", "")).split())
    return c

def block(job):
    # Every step that RUNS something — the cells. `uses:` steps are the runner's
    # own setup (checkout, tailnet, artifacts) and legitimately differ.
    return [cell(s) for s in d["jobs"][job]["steps"] if "run" in s]

ref = block(jobs[0])
if len(ref) < 15:
    sys.exit(f"only {len(ref)} cells parsed from {jobs[0]} — the extraction is broken, not the workflow")

bad = False

# The same LEGS too, and this is not cosmetic. The arm64 client sat in
# e2e-compose as a fourth leg, so the run graph showed 4 · 3 · 3 and read as
# "compose runs something the others do not" — which was untrue and impossible
# to tell apart from a real divergence at a glance. It has its own job now.
# A leg that answers a question other than "which backend?" does not belong in
# these matrices.
legs = {j: d["jobs"][j]["strategy"]["matrix"]["os"] for j in jobs}
if len(set(map(tuple, legs.values()))) != 1:
    bad = True
    print("\nthe three families do not run on the same legs:")
    for j, os_ in legs.items():
        print(f"  {j}: {os_}")
# `continue-on-error` is not a divergence to weigh family against family: it is a
# property of the block. A cell carrying it cannot fail its leg, so the three
# families stay perfectly identical AND stop proving anything, and the check that
# is supposed to notice would agree with itself. Refused wherever it appears,
# including on the reference family.
for job in jobs:
    for i, c in enumerate(block(job)):
        coe = c.get("continue-on-error")
        if "continue-on-error" in c and coe is not False:
            bad = True
            print(f"\n{job} step {i + 1} ({c.get('name')!r}) sets "
                  f"continue-on-error: {coe!r}")
            print("  a cell that cannot fail is not a test. The common block has "
                  "no place for it,\n  in any family.")

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
        print(f"    {jobs[0]}: {a['name'] if a else '(nothing)'}")
        if a and b and a.get("name") == b.get("name"):
            for k in COUNTS:
                if a.get(k) != b.get(k):
                    print(f"      {k}: {a.get(k)!r} vs {b.get(k)!r}")
        else:
            print(f"    {job}: {b['name'] if b else '(nothing)'}")

if bad:
    sys.exit("\nThe three families must run the same cells, in the same order, "
             "under the same names — a family-specific step belongs BELOW the "
             "common block, under its own heading.")
print(f"the three e2e families run the same {len(ref)} cells, in the same order")
PY
