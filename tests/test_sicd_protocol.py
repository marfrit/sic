"""Executable spec for the sic wire protocol v2 — Python reference daemon gateway/sicd.

Wire format (one hop):

    frame   := MAGIC len32 netstring
    MAGIC   := 0x00                                (1 byte)
    len32   := big-endian uint32                   (byte length of the netstring that follows)
    netstring := <n>:<content>,                    (djb netstring)
    content := <command> 0x00 <payload>            (split at the FIRST NUL; NUL is required)

Semantics each sicd MUST implement:

  * Read exactly the frame (5 bytes preamble + len32 bytes) from stdin.
  * Split content at the first NUL: command / payload.
  * exec the command (argv = command split on ASCII spaces, shell=False).
  * The exec'd command's stdin = payload  ++  every remaining byte of sicd's
    own stdin, forwarded verbatim.  (This is the fix for the silent
    zero-byte-file bug: trailing stream bytes must reach the child.)
  * Nesting: a payload may itself be a complete frame for the next hop; each
    sicd peels exactly ONE layer and never looks inside the payload.
  * Payload bytes are opaque: NULs, 0x00 magic bytes, and a bare `--` inside
    the payload are data, never flags or separators.
  * Malformed input (missing magic, truncated frame, invalid netstring,
    missing NUL separator): exit status 1, a diagnostic on stderr, nothing
    exec'd.

sicd inherits the child's exit status on success paths.
"""

import struct
import subprocess
import sys
from pathlib import Path

import pytest

SICD = str((Path(__file__).resolve().parent.parent / "gateway" / "sicd"))

TIMEOUT = 10


def netstring(data: bytes) -> bytes:
    return str(len(data)).encode() + b":" + data + b","


def frame(command: bytes, payload: bytes) -> bytes:
    """Build one wire frame: magic, 4-byte BE length, netstring(command NUL payload)."""
    ns = netstring(command + b"\x00" + payload)
    return b"\x00" + struct.pack(">I", len(ns)) + ns


def run_sicd(wire: bytes) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(
            [sys.executable, SICD],
            input=wire,
            capture_output=True,
            timeout=TIMEOUT,
        )
    except subprocess.TimeoutExpired:
        pytest.fail("sicd hung instead of completing (stdin not consumed/forwarded correctly)")


def test_single_hop_peel_payload_reaches_command_stdin():
    """One frame, command=cat: sicd execs cat and cat's stdin is exactly the payload."""
    payload = b"hello from hop zero\n"
    r = run_sicd(frame(b"cat", payload))
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == payload


def test_two_hop_nested_peel():
    """Payload of the outer frame is itself a frame; each sicd peels exactly one layer.

    The inner frame starts with the 0x00 magic byte, so this also pins
    'split at the FIRST NUL' — a split at any later NUL corrupts the hop.
    """
    inner = frame(b"cat", b"nested payload survived two hops\n")
    outer = frame(f"{sys.executable} {SICD}".encode(), inner)
    r = run_sicd(outer)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == b"nested payload survived two hops\n"


def test_trailing_stdin_forwarded_after_payload():
    """Bytes after the frame reach the child's stdin, after the payload, verbatim.

    Trailing bytes are raw binary (NULs, 0xFF, a colon and comma that must not
    be re-parsed as netstring material).
    """
    payload = b"payload-first:"
    trailing = b"then \x00 raw \xff bytes, 7:not,a netstring\n"
    r = run_sicd(frame(b"cat", payload) + trailing)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == payload + trailing


def test_zero_byte_file_regression_large_body_not_swallowed():
    """The historical bug: sicd read ALL of stdin for framing, so a streamed
    file body after the frame silently became a zero-byte file. A 64 KiB body
    after an empty-payload frame must come out of cat intact."""
    body = bytes(range(256)) * 256  # 64 KiB, every byte value
    r = run_sicd(frame(b"cat", b"") + body)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == body


@pytest.mark.parametrize("payload", [b"--", b"-- --help --version\n", b"a -- b\n"])
def test_bare_double_dash_payload_is_opaque(payload):
    """A bare -- inside the payload is data, never an option/separator: it must
    reach the command's stdin byte-for-byte and never abort the run."""
    r = run_sicd(frame(b"cat", payload))
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == payload


@pytest.mark.parametrize(
    "wire",
    [
        pytest.param(b"", id="empty-input"),
        pytest.param(b"garbage with no magic byte", id="missing-magic"),
        pytest.param(b"4:exec,3:cat,0:,", id="old-v1-frame-without-preamble"),
        pytest.param(b"\x00" + struct.pack(">I", 14) + b"not&a netstrng", id="body-not-a-netstring"),
        pytest.param(b"\x00" + struct.pack(">I", 100) + netstring(b"cat\x00hi"), id="truncated-declared-length"),
        pytest.param(b"\x00" + struct.pack(">I", 6) + netstring(b"ca"), id="content-missing-nul-separator"),
    ],
)
def test_malformed_input_exits_1_with_diagnostic(wire):
    """Malformed frames: exit status exactly 1, a clear message on stderr,
    and nothing exec'd (no stdout)."""
    r = run_sicd(wire)
    assert r.returncode == 1, f"expected exit 1, got {r.returncode}"
    assert r.stdout == b"", "malformed input must not silently exec anything"
    assert r.stderr.strip() != b"", "malformed input must produce a diagnostic on stderr"


def test_empty_payload_command_gets_immediate_eof():
    """Empty payload, no trailing bytes: the command runs, sees EOF at once
    (no hang), produces nothing, exits 0."""
    r = run_sicd(frame(b"cat", b""))
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == b""

def test_large_payload_64k_plus_1_no_deadlock():
    """Payload+trailing exceeding the 64 KiB pipe buffer must not deadlock.
    The child reads from stdin, so the parent must pump through a pipe
    after fork — not pre-write the entire blob into a pipe and block."""
    body = bytes(range(256)) * 257  # 65,792 bytes > 65,536 pipe buffer
    r = run_sicd(frame(b"cat", b"") + body)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert len(r.stdout) == len(body), f"expected {len(body)} bytes, got {len(r.stdout)}"


def test_multi_mib_payload_no_deadlock():
    """1 MiB body after an empty-payload frame must stream through without
    hanging or truncating — pins the streaming pump loop, not just the
    pipe-buffer edge."""
    body = bytes(range(256)) * 4096  # 1 MiB
    r = run_sicd(frame(b"cat", b"") + body)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == body


def test_nonexistent_command_exits_nonzero():
    """Exec of a command that does not exist must not hang or exit 0."""
    r = run_sicd(frame(b"__nonexistent_command_42__", b""))
    assert r.returncode != 0, f"expected non-zero exit, got {r.returncode}"


# ---------------------------------------------------------------------------
# Fault injection: deterministic EPIPE and short-write coverage.
#
# sicd runs under a tiny driver that patches os primitives *before* executing
# gateway/sicd, instead of racing signals against the pump loop:
#
#   widow-before-write : os.fork is wrapped so the parent blocks (waitid with
#                        WNOWAIT -- the child stays reapable for the later
#                        waitpid) until the child is dead.  Exec'ing a
#                        nonexistent command therefore guarantees the pipe is
#                        widowed before the payload write: the EPIPE case,
#                        made deterministic.
#   short-write        : os.write is wrapped to transfer at most 7 bytes per
#                        call -- the kernel's partial-write behaviour on
#                        signal interruption.  Code that ignores os.write's
#                        return value truncates; a write-all loop is
#                        unaffected.
# ---------------------------------------------------------------------------

DRIVER = """\
import os
import sys

sicd_path = sys.argv[1]
mode = sys.argv[2]

if mode == "short-write":
    real_write = os.write
    def short_write(fd, data):
        return real_write(fd, bytes(data)[:7])
    os.write = short_write
elif mode == "widow-before-write":
    real_fork = os.fork
    def fork_then_wait_for_child_death(*args):
        pid = real_fork(*args)
        if pid:
            os.waitid(os.P_PID, pid, os.WEXITED | os.WNOWAIT)
        return pid
    os.fork = fork_then_wait_for_child_death
else:
    raise SystemExit("unknown fault mode: " + mode)

sys.argv = [sicd_path]
code = compile(open(sicd_path, "rb").read(), sicd_path, "exec")
exec(code, {"__name__": "__main__", "__file__": sicd_path})
"""


def run_sicd_fault(wire: bytes, mode: str, tmp_path) -> subprocess.CompletedProcess:
    driver = tmp_path / "fault_driver.py"
    driver.write_text(DRIVER)
    try:
        return subprocess.run(
            [sys.executable, str(driver), SICD, mode],
            input=wire,
            capture_output=True,
            timeout=TIMEOUT,
        )
    except subprocess.TimeoutExpired:
        pytest.fail(f"sicd hung under fault mode {mode!r}")


def test_epipe_child_dead_before_payload_write_no_traceback(tmp_path):
    """Child is guaranteed dead (nonexistent command; the wrapped fork waits
    for the corpse) before the parent writes the payload: sicd must handle
    EPIPE and exit with the child's status -- never dump a BrokenPipeError
    traceback on stderr."""
    wire = frame(b"__nonexistent_command_42__", b"payload for a corpse\n")
    r = run_sicd_fault(wire, "widow-before-write", tmp_path)
    assert b"Traceback" not in r.stderr, (
        f"unhandled exception instead of clean EPIPE handling:\n"
        f"{r.stderr.decode(errors='replace')}"
    )
    assert b"BrokenPipeError" not in r.stderr
    assert r.returncode == 1, f"expected the child's exit status 1, got {r.returncode}"


def test_partial_write_payload_not_truncated(tmp_path):
    """os.write may transfer fewer bytes than requested; the payload write
    must loop on its return value.  With every write capped at 7 bytes, cat
    must still receive the full 37-byte payload, not just the first chunk."""
    payload = b"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n"
    r = run_sicd_fault(frame(b"cat", payload), "short-write", tmp_path)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == payload, (
        f"payload truncated by ignored os.write return value: "
        f"got {len(r.stdout)} of {len(payload)} bytes"
    )


def test_partial_write_pump_loop_not_truncated(tmp_path):
    """The same 7-byte write cap applied to the stdin pump loop: an 8 KiB
    trailing body must reach the child intact, not 7 bytes per read chunk."""
    body = bytes(range(256)) * 32  # 8 KiB
    r = run_sicd_fault(frame(b"cat", b"") + body, "short-write", tmp_path)
    assert r.returncode == 0, f"expected exit 0, got {r.returncode}, stderr={r.stderr!r}"
    assert r.stdout == body, (
        f"pump loop truncated trailing stdin: got {len(r.stdout)} of {len(body)} bytes"
    )
