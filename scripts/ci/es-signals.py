"""Signals and exits from an eslogger JSON stream, one line each.

    es-signals.py <es.jsonl>

Keeps what concerns plug, the commands the e2e cells run under it (echo-local
and friends) and the shells that drive them: a signal says who sent it to
whom, an exit says with what status. Tolerant of a field it does not find: a
line it cannot read is skipped, never a reason to stop.
"""
import json
import sys

KEEP = ("plug", "echo-local", "sink", "bash", "perl", "curl")


def proc(p):
    p = p or {}
    path = ((p.get("executable") or {}).get("path")) or "?"
    pid = (p.get("audit_token") or {}).get("pid", "?")
    ppid = p.get("ppid", "?")
    return path, pid, ppid


def short(path):
    return path.rsplit("/", 1)[-1]


def kept(path):
    name = short(path)
    return any(name == k or name.startswith(k) for k in KEEP)


with open(sys.argv[1], errors="replace") as f:
    for line in f:
        try:
            m = json.loads(line)
        except ValueError:
            continue
        ev = m.get("event") or {}
        t = (m.get("time") or "")[11:23]
        spath, spid, sppid = proc(m.get("process"))
        if "signal" in ev:
            s = ev["signal"]
            tpath, tpid, tppid = proc(s.get("target"))
            if kept(tpath):
                print(f"{t} signal {s.get('sig')} from {short(spath)}[{spid}] (parent {sppid}) to {short(tpath)}[{tpid}] (parent {tppid})")
        elif "exit" in ev:
            if kept(spath):
                st = ev["exit"].get("stat")
                how = f"status {st >> 8}" if isinstance(st, int) and st & 0x7F == 0 else f"signal {st & 0x7F}" if isinstance(st, int) else st
                print(f"{t} exit {short(spath)}[{spid}] (parent {sppid}): {how}")
