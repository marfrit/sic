"""Executable spec for cmd/sic/sickill.py — the self-hit guard.

The whole reason this tool exists is one failure that `pkill -f` cannot avoid:
the caller's own command line contains the pattern, because the caller just
typed it. Over ssh that kills the session; over an MCP shell tool it kills the
`sh -c '(...)'` wrapper and its forks.

So the anchoring test is NOT "does it kill things" (any `pkill` does that). It is:

    a victim carrying the pattern dies,
    while the caller carrying the SAME pattern survives.

Guard against a hollow implementation: a stub that always reports "1 match"
passes a naive count check, so every assertion below pins the exact PID of the
victim we started ourselves, and test_pkill_would_hit_itself proves the trap is
real on this machine rather than assuming it.

Run:  python3 -m pytest tests/test_sickill.py -v
      (or plain: python3 tests/test_sickill.py)
"""

import os
import subprocess
import sys
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SICKILL = os.path.join(HERE, "..", "cmd", "sic", "sickill.py")

# Distinctive enough that nothing else on the machine can carry it by accident.
MARKER = "zzz-sickill-selftest-marker-8831"


def start_victim(marker=MARKER):
    """A process whose command line contains the marker. Returns Popen."""
    p = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(120)", marker],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    # Wait until /proc actually shows the argv, else we race the exec.
    for _ in range(100):
        try:
            with open(f"/proc/{p.pid}/cmdline", "rb") as fh:
                if marker.encode() in fh.read():
                    return p
        except OSError:
            pass
        time.sleep(0.02)
    p.kill()
    raise AssertionError("victim never showed the marker in /proc")


def run_sickill(*args):
    """Invoke sickill. Its own argv carries the pattern -- that is the point."""
    return subprocess.run(
        [sys.executable, SICKILL, *args],
        capture_output=True, text=True,
    )


class SelfHitGuard(unittest.TestCase):
    def setUp(self):
        self.victim = start_victim()
        self.addCleanup(self._reap)

    def _reap(self):
        if self.victim.poll() is None:
            self.victim.kill()
        self.victim.wait()

    def test_finds_the_victim_by_exact_pid(self):
        """Listing must name the victim's real PID, not just a count."""
        r = run_sickill(MARKER)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(str(self.victim.pid), r.stdout,
                      f"victim pid {self.victim.pid} missing from:\n{r.stdout}")

    def test_does_not_list_itself(self):
        """sickill's own argv carries MARKER; it must not appear as a match."""
        r = run_sickill(MARKER)
        listed = [ln for ln in r.stdout.splitlines() if ln.startswith("  ")]
        for line in listed:
            self.assertNotIn("sickill.py", line,
                             f"sickill listed its own process:\n{r.stdout}")
        self.assertEqual(len(listed), 1,
                         f"expected exactly the victim, got:\n{r.stdout}")

    def test_listing_does_not_kill(self):
        """Without --force nothing is signalled -- the victim must still live."""
        run_sickill(MARKER)
        time.sleep(0.3)
        self.assertIsNone(self.victim.poll(), "victim died without --force")

    def test_force_kills_the_victim(self):
        r = run_sickill(MARKER, "--force")
        self.assertEqual(r.returncode, 0, r.stderr)
        for _ in range(100):
            if self.victim.poll() is not None:
                break
            time.sleep(0.02)
        self.assertIsNotNone(self.victim.poll(),
                             f"victim survived --force:\n{r.stdout}{r.stderr}")

    def test_no_match_is_exit_1(self):
        r = run_sickill("zzz-nothing-can-match-this-9id7")
        self.assertEqual(r.returncode, 1)
        self.assertIn("no match", r.stdout)

    def test_pkill_would_hit_itself(self):
        """The trap is real on THIS machine, not just in the docstring.

        Without this, the suite could pass on a system where `pgrep -f` somehow
        excludes the caller, and the guard above would be proving nothing.
        """
        r = subprocess.run(
            ["/bin/sh", "-c", f"pgrep -f {MARKER} | wc -l"],
            capture_output=True, text=True,
        )
        n = int(r.stdout.strip() or 0)
        self.assertGreaterEqual(
            n, 2,
            "expected pgrep -f to match the victim AND its own shell; "
            f"got {n}. If this fails, the self-hit premise no longer holds here.",
        )


class ExactNameMode(unittest.TestCase):
    def test_exact_mode_ignores_a_mere_mention(self):
        """-x matches argv[0]'s basename, so a process that only MENTIONS the
        name in a later argument is not a match. That is the pgrep -x semantic
        which makes the self-hit impossible in the first place."""
        victim = start_victim()
        self.addCleanup(lambda: (victim.kill(), victim.wait()))
        # The victim's argv[0] is python3; the marker sits in argv[2].
        r = run_sickill(MARKER, "-x")
        self.assertEqual(r.returncode, 1, f"-x should not match a mention:\n{r.stdout}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
