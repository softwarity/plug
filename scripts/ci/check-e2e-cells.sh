#!/usr/bin/env bash
# The e2e chain is written ONCE (.github/workflows/_e2e.yml) and run by four
# callers in ci.yml, so the three families cannot drift from each other any
# more. What can still drift is checked here, in seconds, before any runner is
# spent: the ORDER of the cells against the one written below, the FILE each
# cell dispatches to (scripts/ci/e2e/<cell>.sh defining do_<cell>), a cell
# file nothing runs, a cell that cannot fail (continue-on-error) or cannot run
# (a shell that is not bash), the callers' legs, and the names the other
# scripts read: abort-on-fail matches `^e2e-` on job names, and
# idle-until-caller-done.sh looks for the caller's "kill <family> clusters".
#
# Drift is silent: nothing fails when a cell quietly stops being run, or when a
# renamed cell file leaves its step dispatching to nothing. This does.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

python3 - <<'PY'
import os, re, sys, yaml

bad = []
def fail(msg): bad.append(msg)

# The chain, in order. `compat` is scripts/ci/compat-launcher.sh, every other
# cell is scripts/ci/e2e/<cell>.sh through e2e-matrix.sh. The arm64 client runs
# the cells NOT marked subset-skipped (see _e2e.yml for why those are skipped).
CHAIN = [
    ("setup", False), ("env", False), ("workloadenv", False), ("privdrop", False),
    ("dnshonesty", False), ("dnsrelay", False), ("clientonly", False), ("doctor", False),
    ("takeover", True), ("orphan", True), ("resilience", True), ("mount", True),
    ("matrix", False), ("multicluster", False),
    ("dockerrun", True), ("keymount", True), ("outage", True),
    ("expose", False), ("exposevar", False), ("gateway", False), ("collision", False),
    ("lease", False), ("sameport", False), ("multiport", False),
    ("compat", True), ("update", True), ("updatetag", True), ("updatenotify", True), ("updatejump", True),
]

leg = yaml.safe_load(open(".github/workflows/_e2e.yml", encoding="utf-8"))
steps = leg["jobs"]["leg"]["steps"]
cells = []
for s in steps:
    run = " ".join(str(s.get("run", "")).split())
    m = re.match(r"bash scripts/ci/e2e-matrix\.sh (\S+) ", run)
    if m:
        cell = m.group(1)
    elif run.startswith("bash scripts/ci/compat-launcher.sh "):
        cell = "compat"
    elif "run" in s:
        continue  # the job clock
    else:
        continue  # checkout, the composite action
    cond = str(s.get("if", ""))
    gated = "inputs.full" in cond
    if cond and cond != "success() && inputs.full":
        fail(f"_e2e.yml step {s.get('name')!r}: `if: {cond}` is neither absent nor the subset gate "
             "`success() && inputs.full` (without success() a cell would run after an earlier one failed)")
    if s.get("shell") != "bash":
        fail(f"_e2e.yml step {s.get('name')!r} ({cell}): shell is {s.get('shell')!r}, not bash")
    if "continue-on-error" in s:
        fail(f"_e2e.yml step {s.get('name')!r} ({cell}): a cell that cannot fail is not a test (continue-on-error)")
    cells.append((cell, gated))

if cells != CHAIN:
    fail("_e2e.yml does not run the chain this script expects, in this order:")
    for i in range(max(len(cells), len(CHAIN))):
        a = cells[i] if i < len(cells) else None
        b = CHAIN[i] if i < len(CHAIN) else None
        if a != b:
            fail(f"  step {i + 1}: _e2e.yml has {a}, expected {b}")

# Every cell has its file and its function; every file is a cell.
dispatched = {c for c, _ in cells if c != "compat"}
for c in sorted(dispatched):
    p = f"scripts/ci/e2e/{c}.sh"
    if not os.path.isfile(p):
        fail(f"{p} is missing: the step dispatching `{c}` would fail on every leg")
        continue
    src = open(p, encoding="utf-8").read()
    if not re.search(rf"^do_{re.escape(c)}\(\) \{{", src, re.M):
        fail(f"{p} does not define do_{c}()")
    if "—" in src:
        fail(f"{p} contains an em dash")
for f in sorted(os.listdir("scripts/ci/e2e")):
    c = f[:-3] if f.endswith(".sh") else f
    if c == "lib":
        continue
    if c not in dispatched:
        fail(f"scripts/ci/e2e/{f} is not run by any step of _e2e.yml")
if not os.path.isfile("scripts/ci/compat-launcher.sh"):
    fail("scripts/ci/compat-launcher.sh is missing")

# The callers in ci.yml.
ci = yaml.safe_load(open(".github/workflows/ci.yml", encoding="utf-8"))
jobs = ci["jobs"]
callers = {k: v for k, v in jobs.items() if str(v.get("uses", "")).endswith("/_e2e.yml")}
if not callers:
    fail("ci.yml has no job calling _e2e.yml")
families = {}
for k, v in callers.items():
    w = v.get("with") or {}
    if not k.startswith("e2e-"):
        fail(f"ci.yml job {k}: a leg's job key must start with e2e- (abort-on-fail matches ^e2e- on the job name)")
    legs = yaml.safe_load(str(w.get("legs", "[]")))
    full = w.get("full", True)
    fam = w.get("family")
    for x in ("cluster_a", "cluster_b"):
        if not str(w.get(x, "")).startswith(f"plug-{fam}-"):
            fail(f"ci.yml job {k}: {x}={w.get(x)!r} does not start with plug-{fam}- (the name lib.sh reads the family off)")
    if full is True:
        families.setdefault(fam, []).append((k, legs))
    ran = [c for c, g in cells if full is True or not g]
    print(f"{k}: family={fam} legs={legs} minutes={w.get('minutes')} {'full' if full is True else 'subset'} -> {len(ran)} steps")
    print("  " + " ".join(ran))
if len(families) != 3:
    fail(f"expected one full caller per family (compose, k8s, swarm), got {sorted(families)}")
legsets = {tuple(l) for fam in families.values() for _, l in fam}
if len(legsets) != 1:
    fail(f"the three families do not run on the same legs: {legsets}")

# The names the other scripts read.
for fam in ("compose", "k8s", "swarm"):
    kill = jobs.get(f"kill-{fam}") or {}
    if kill.get("name") != f"kill {fam} clusters":
        fail(f"ci.yml kill-{fam} must be named 'kill {fam} clusters' (idle-until-caller-done.sh looks for it)")
    for n in kill.get("needs", []):
        if n.startswith("e2e-") and n not in callers:
            fail(f"ci.yml kill-{fam} needs {n}, which is not a leg job")
for n in jobs["image"]["needs"]:
    if n.startswith("e2e-") and n not in callers:
        fail(f"ci.yml image needs {n}, which is not a leg job")
missing = sorted(set(callers) - {n for j in ("kill-compose", "kill-k8s", "kill-swarm") for n in jobs[j]["needs"]})
if missing:
    fail(f"no kill job waits for {missing}: their clusters would be cancelled from under them")
abort = yaml.safe_load(open(".github/workflows/ci.yml", encoding="utf-8"))["jobs"]["abort-on-fail"]
if '^e2e-' not in str(abort["steps"][0].get("run", "")):
    fail("abort-on-fail no longer selects jobs by ^e2e-")

# The cluster side of the hostname contract.
cl = open(".github/workflows/_cluster.yml", encoding="utf-8").read()
if "hostname: plug-${{ inputs.family }}-${{ inputs.corr }}" not in cl:
    fail("_cluster.yml does not name its tailnet node plug-<family>-<corr>")
for fam, script in (("compose", "cluster-serve.sh"), ("k8s", "k8s-serve.sh"), ("swarm", "swarm-serve.sh")):
    fc = yaml.safe_load(open(f".github/workflows/{fam}-for.yml", encoding="utf-8"))
    w = fc["jobs"]["cluster"]["with"]
    if w.get("family") != fam or w.get("serve_script") != f"scripts/ci/{script}":
        fail(f"{fam}-for.yml does not call _cluster.yml with family={fam} and scripts/ci/{script}")
    if fc.get("run-name") != f"{fam}-for-${{{{ inputs.corr }}}}":
        fail(f"{fam}-for.yml run-name is not {fam}-for-<corr> (dispatch-cluster.sh looks for it)")

if bad:
    print("\n".join(bad))
    sys.exit("\nthe e2e chain, its files and the names other scripts read must agree; see above")
print(f"\nthe chain runs {len(CHAIN)} cells in the expected order; every cell has its file and every file is a cell")
PY
