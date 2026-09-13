#!/usr/bin/env python3
"""sickill — kill processes by command-line pattern without shooting yourself.

`pkill -f <pattern>` matches the FULL command line of every process, and the
caller's own command line contains the pattern -- because the caller just typed
it. So `ssh host 'pkill -f foo'` kills the ssh session, and an MCP shell tool
kills its own `sh -c '(...)'` wrapper. Measured on 2026-09-13: one self-hit over
ssh, three over lmcp (wrapper, subshell fork, command substitution).

The fix is not "be careful with the pattern". It is to make the caller's own
process tree unkillable by construction:

    taboo = every ancestor of this process (up to, but excluding, PID 1)
            + every descendant of each of those ancestors

That covers self, the shell wrapper, sibling forks and the transport (sshd /
sicd), because they all hang off a shared ancestor. Everything else is fair game.

Default is to LIST, not to kill. Killing needs --force. This is not a dry-run
pretence: the listing is a real listing of real matches, and it is the output
you act on.

Exit codes:
    0  matches found (listed, or killed with --force)
    1  no match
    2  usage / runtime error
"""

import argparse
import os
import re
import signal
import sys

PROC = "/proc"


def read_cmdline(pid):
    """Full command line of pid with NULs turned into spaces, or None if gone."""
    try:
        with open(f"{PROC}/{pid}/cmdline", "rb") as fh:
            raw = fh.read()
    except (OSError, IOError):
        return None
    if not raw:
        return None  # kernel thread -- never a kill target here
    return raw.replace(b"\0", b" ").decode("utf-8", "replace").strip()


def read_ppid(pid):
    """Parent pid, read from stat's 4th field. Robust against spaces in comm."""
    try:
        with open(f"{PROC}/{pid}/stat", "rb") as fh:
            data = fh.read().decode("utf-8", "replace")
    except (OSError, IOError):
        return None
    # comm is in parentheses and may itself contain spaces AND parentheses,
    # so split after the LAST ')' rather than on whitespace.
    close = data.rfind(")")
    if close == -1:
        return None
    fields = data[close + 2:].split()
    if len(fields) < 2:
        return None
    try:
        return int(fields[1])
    except ValueError:
        return None


def all_pids():
    return [int(e) for e in os.listdir(PROC) if e.isdigit()]


def build_tree(pids):
    """Return {ppid: [child, ...]} and {pid: ppid}."""
    parent = {}
    children = {}
    for pid in pids:
        ppid = read_ppid(pid)
        if ppid is None:
            continue
        parent[pid] = ppid
        children.setdefault(ppid, []).append(pid)
    return parent, children


def ancestors(pid, parent):
    """Ancestor chain of pid, excluding PID 1 and excluding pid itself.

    PID 1 is excluded on purpose: taking it as a taboo root would make the whole
    machine taboo and the tool would never match anything.
    """
    out = []
    seen = {pid}
    cur = parent.get(pid)
    while cur and cur != 1 and cur not in seen:
        out.append(cur)
        seen.add(cur)
        cur = parent.get(cur)
    return out


def descendants(pid, children):
    out = set()
    stack = [pid]
    while stack:
        cur = stack.pop()
        for kid in children.get(cur, ()):
            if kid not in out:
                out.add(kid)
                stack.append(kid)
    return out


def taboo_set(me, parent, children):
    """Every pid that must survive.

    Deliberately NOT "every descendant of every ancestor" -- that was the first
    attempt and it is too wide: a process started from the same shell is a
    sibling, so killing a background job you launched yourself would silently
    match nothing. The tests caught it.

    What actually has to be spared is the caller's own chain plus the forks of
    the invocation wrapper. Those forks (an MCP `sh -c '(...)'` subshell, a
    command substitution) are recognisable because they carry EXACTLY the same
    command line as one of my ancestors -- that is what makes them phantoms in
    the first place. A real victim never does.
    """
    taboo = {me}
    taboo |= descendants(me, children)          # substitutions inside my own call

    anc = ancestors(me, parent)
    taboo.update(anc)

    # Forks of the wrapper: same command line as an ancestor, anywhere on the box.
    anc_cmdlines = set()
    for a in anc:
        cmd = read_cmdline(a)
        if cmd:
            anc_cmdlines.add(cmd)
    if anc_cmdlines:
        for pid in list(children.keys()) + [p for kids in children.values() for p in kids]:
            if pid in taboo:
                continue
            cmd = read_cmdline(pid)
            if cmd and cmd in anc_cmdlines:
                taboo.add(pid)
                taboo |= descendants(pid, children)
    return taboo


def find_matches(pattern, exact_name=False):
    """Return [(pid, cmdline)] matching pattern, with our own tree removed."""
    me = os.getpid()
    pids = all_pids()
    parent, children = build_tree(pids)
    taboo = taboo_set(me, parent, children)

    try:
        rx = re.compile(pattern)
    except re.error as exc:
        raise SystemExit(f"sickill: bad pattern: {exc}")

    hits = []
    for pid in sorted(pids):
        if pid in taboo or pid == 1:
            continue
        cmd = read_cmdline(pid)
        if cmd is None:
            continue
        if exact_name:
            # compare against argv[0]'s basename only -- the pgrep -x semantics
            # that cannot match a caller who merely mentions the name
            argv0 = cmd.split(" ", 1)[0]
            if os.path.basename(argv0) != pattern:
                continue
        elif not rx.search(cmd):
            continue
        hits.append((pid, cmd))
    return hits


def main():
    ap = argparse.ArgumentParser(
        prog="sickill",
        description="Kill by command-line pattern without matching your own process tree.",
    )
    ap.add_argument("pattern", help="regex matched against the full command line")
    ap.add_argument("-x", "--exact", action="store_true",
                    help="match argv[0] basename exactly instead of a regex over the full line")
    ap.add_argument("-f", "--force", action="store_true",
                    help="actually send the signal (without this, matches are only listed)")
    ap.add_argument("-s", "--signal", default="TERM",
                    help="signal name or number to send with --force (default TERM)")
    ap.add_argument("--width", type=int, default=110, help="truncate listed command lines")
    args = ap.parse_args()

    try:
        if args.signal.isdigit():
            sig = int(args.signal)
        else:
            sig = getattr(signal, "SIG" + args.signal.upper().removeprefix("SIG"))
    except AttributeError:
        raise SystemExit(f"sickill: unknown signal: {args.signal}")

    hits = find_matches(args.pattern, exact_name=args.exact)
    if not hits:
        print(f"sickill: no match for {args.pattern!r} (own process tree excluded)")
        return 1

    for pid, cmd in hits:
        print(f"  {pid:>8}  {cmd[:args.width]}")

    if not args.force:
        print(f"sickill: {len(hits)} match(es), nothing signalled. Add --force to send SIG{args.signal.upper()}.")
        return 0

    killed, failed = 0, 0
    for pid, _ in hits:
        try:
            os.kill(pid, sig)
            killed += 1
        except ProcessLookupError:
            pass  # exited between listing and signalling -- not an error
        except PermissionError:
            failed += 1
            print(f"sickill: no permission for pid {pid}", file=sys.stderr)
    print(f"sickill: sent SIG{args.signal.upper()} to {killed} process(es)"
          + (f", {failed} refused" if failed else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
