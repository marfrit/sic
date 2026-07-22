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
