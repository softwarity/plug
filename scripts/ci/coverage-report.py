#!/usr/bin/env python3
"""Merge Go coverage profiles and render what they mean, for the run summary.

MERGING BY CONCATENATION, and it is not a shortcut. A Go text profile is one
`mode:` line then one line per block, `file:start.col,end.col numstmt count`.
`go tool cover` parses duplicate blocks by SUMMING their counts, so appending
profiles keeps a block covered if ANY of them covered it. Checked before this was
written, both ways: the same profile twice gives the same total (no double
counting), and an all-zero profile plus a real one gives the real one (a union,
not an average).

Merging is what makes the number honest HERE in particular. plug is full of
files behind build tags - graft_darwin.go, pidroute_windows.go, nsshim_linux.go -
and on a single runner those are not "uncovered", they are not compiled at all:
absent from both sides of the fraction. A percentage from one OS would quietly
leave out a third of the program.

WHAT THIS NUMBER IS NOT. It counts what `go test` executes. Whole files sit at
0% here while being exercised hard by the e2e matrix - daemon_darwin.go,
selftest.go, swarm.go run only against a real datapath or a real cluster. So the
number is unit coverage of a project whose backends are proven by 21 cells on
three OSes and three orchestrators, and the report says so rather than letting a
reader draw the other conclusion. That is also why nothing here fails a build: a
floor would push someone to write unit tests for swarm.go, duplicating worse what
the e2e already proves.

Usage: coverage-report.py <out.md> <profile>...
"""
import collections
import os
import sys


def read(paths):
    """Every block from every profile, keyed by (file, span) with counts summed."""
    blocks = collections.Counter()
    mode = "atomic"
    for p in paths:
        with open(p, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                if line.startswith("mode:"):
                    mode = line.split(":", 1)[1].strip()
                    continue
                loc, nstmt, count = line.rsplit(" ", 2)
                blocks[(loc, int(nstmt))] += int(count)
    return mode, blocks


def per_file(blocks):
    total, covered = collections.Counter(), collections.Counter()
    for (loc, nstmt), count in blocks.items():
        path = loc.split(":")[0]
        total[path] += nstmt
        if count > 0:
            covered[path] += nstmt
    return total, covered


def shorten(path):
    """github.com/softwarity/plug/cli/internal/tun/dns.go -> cli/internal/tun/dns.go"""
    for marker in ("/plug/", "/"):
        if marker in path:
            i = path.find("/plug/")
            if i >= 0:
                return path[i + len("/plug/"):]
            break
    return path


def main():
    if len(sys.argv) < 3:
        sys.exit("usage: coverage-report.py <out.md> <profile>...")
    out, profiles = sys.argv[1], [p for p in sys.argv[2:] if os.path.getsize(p) > 0]
    if not profiles:
        sys.exit("no non-empty coverage profile was given")

    _, blocks = read(profiles)
    total, covered = per_file(blocks)
    T, C = sum(total.values()), sum(covered.values())
    if T == 0:
        sys.exit("the merged profile counts no statements at all")

    # Worst first, by the number of statements NOT covered rather than by
    # percentage: a 0% file of nine lines is noise, and a 40% file of eight
    # hundred is where the mass is.
    gaps = sorted(total, key=lambda f: covered[f] - total[f])

    lines = [
        "## Coverage",
        "",
        f"**{100 * C / T:.1f}%** of statements, {C:,} of {T:,}, "
        f"merged from {len(profiles)} profile(s).",
        "",
        "Merged across operating systems on purpose: files behind build tags are not "
        "compiled on a runner that does not need them, so a single-OS number would "
        "leave them out of the fraction entirely rather than count them as uncovered.",
        "",
        "It measures what `go test` runs. Files that only work against a real datapath "
        "or a real cluster sit low here and are covered by the e2e matrix instead, "
        "which no counter sees. Nothing below fails the build for that reason.",
        "",
        "| statements uncovered | of | file | |",
        "| ---: | ---: | :--- | ---: |",
    ]
    for f in gaps[:15]:
        miss, tot = total[f] - covered[f], total[f]
        if miss == 0:
            break
        lines.append(f"| {miss} | {tot} | `{shorten(f)}` | {100 * covered[f] / tot:.0f}% |")
    lines.append("")

    with open(out, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(f"{100 * C / T:.1f}% ({C}/{T} statements) from {len(profiles)} profile(s)")


if __name__ == "__main__":
    main()
