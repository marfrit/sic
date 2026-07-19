#!/usr/bin/env python3
"""Demonstrate quoting hell scenarios with plain SSH and how sicd fixes them.

Each scenario is run both ways:
  A) Plain SSH — shows the breakage
  B) sic        — shows the fix

Point it at any host that runs sicd (reachable over SSH with your key):

  python3 quoting-hell.py <host>
"""
import subprocess
import sys
import os

if len(sys.argv) < 2:
    sys.exit("usage: python3 quoting-hell.py <host>   # any host running sicd")

HOST = sys.argv[1]

SIC = os.path.expanduser("~/bin/sic")
SSH = ["/usr/bin/ssh", "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=5"]


def ssh(cmd_str):
    """Run a command string via plain SSH."""
    return subprocess.run(
        SSH + [HOST, cmd_str],
        capture_output=True, text=True, timeout=10
    )


def sic(*args):
    """Run argv via sic (which frames netstrings for sicd)."""
    return subprocess.run(
        [SIC, HOST] + list(args),
        capture_output=True, text=True, timeout=10
    )


def section(title):
    print(f"\n{'='*60}")
    print(f"  {title}")
    print(f"{'='*60}")


def sub(test_name):
    print(f"\n  ▶ {test_name}")


def result(label, r):
    stdout = r.stdout.strip() if r.stdout else ""
    stderr = r.stderr.strip() if r.stderr else ""
    status = "OK" if r.returncode == 0 else f"FAIL (rc={r.returncode})"
    print(f"    {label}: {status}")
    for line in stdout.split("\n"):
        if line:
            print(f"      → {line}")
    for line in stderr.split("\n"):
        if line:
            print(f"      ! {line}")


# ── 1. Space in filename ──────────────────────────────────────────
section("1. Space in filename — the classic")

sub(f"SSH: ssh {HOST} touch 'my file.txt'")
# SSH concatenates args with spaces, remote shell re-splits
r = ssh("touch 'my file.txt'")
r2 = ssh("ls my file.txt 2>&1; echo ---; ls 'my file.txt' 2>&1")
result("SSH", r2)
print("    → 'touch' sees TWO args: 'my' and 'file.txt' — two files created")

# Clean up
ssh("rm -f my file.txt 'my file.txt'")

sub(f"sic: sic {HOST} touch 'my file.txt'")
r = sic("touch", "my file.txt")
r2 = sic("ls", "-la", "my file.txt")
result("sic", r2)
print("    → 'touch' sees ONE arg: 'my file.txt' — one file created")
sic("rm", "-f", "my file.txt")


# ── 2. Dollar sign / variable expansion ───────────────────────────
section("2. Dollar sign — unintended variable expansion")

sub(f"SSH: ssh {HOST} 'echo \$HOME'")
r = ssh("echo $HOME")
result("SSH", r)
print("    → Remote shell expands $HOME — leaks local values")

sub(f"SSH escaped: ssh {HOST} 'echo \\$HOME'")
r = ssh("echo \\$HOME")
result("SSH escaped", r)

sub(f"sic: sic {HOST} echo '\$HOME'")
r = sic("echo", "$HOME")
result("sic", r)
print("    → No shell involved — literal $HOME printed")


# ── 3. Backticks / command substitution ──────────────────────────
section("3. Backticks — unintended command execution")

sub(f"SSH: ssh {HOST} 'echo `hostname`'")
r = ssh("echo `hostname`")
result("SSH", r)
print("    → Backticks execute on remote — prints the remote hostname")

sub(f"sic: sic {HOST} echo '`hostname`'")
r = sic("echo", "`hostname`")
result("sic", r)
print("    → Literal backticks — no execution")


# ── 4. Nested quotes ─────────────────────────────────────────────
section("4. Nested quotes — escaping hell")

sub(f'SSH: ssh {HOST} "echo \\"hello world\\""')
r = ssh('echo "hello world"')
result("SSH", r)

sub(f'sic: sic {HOST} echo "hello world"')
r = sic("echo", "hello world")
result("sic", r)
print("    → No nested quoting needed — just pass the string")


# ── 5. Newlines in arguments ──────────────────────────────────────
section("5. Newlines in arguments")

sub(f"SSH: ssh {HOST} 'echo \"line1\\nline2\"'")
r = ssh('echo "line1\nline2"')
result("SSH", r)
print("    → Newline in SSH command string is fragile")

sub(f"sic: sic {HOST} echo 'line1\\nline2'")
r = sic("echo", "line1\nline2")
result("sic", r)
print("    → Newline is just a byte in a netstring field")


# ── 6. Complex python script via ssh ─────────────────────────────
section("6. Python one-liner — where quoting really hurts")

script = """python3 -c "import sys; [print(i, repr(a)) for i,a in enumerate(sys.argv)]" -- "first arg" "second arg" "arg with $HOME" "arg with \`ls\`" "nested \\"quotes\\"" """

sub("SSH: the monstrosity")
r = ssh(script)
result("SSH", r)
print("    → Try to get all those quoting levels right...")

sub("sic: sic <host> python3 -c ... with clean args")
r = sic(
    "python3", "-c",
    "import sys; [print(i, repr(a)) for i,a in enumerate(sys.argv)]",
    "demo",  # sys.argv[0]
    "first arg",
    "second arg",
    "arg with $HOME",
    "arg with `ls`",
    'nested "quotes"'
)
result("sic", r)
print("    → Each argument is a separate field — no escaping needed")


# ── Summary ──────────────────────────────────────────────────────
section("SUMMARY")
print("""
  The pattern in every case:
    SSH  → flattens argv to one string, shell re-parses → QUOTING HELL
    sic  → sends argv as framed fields, execvp directly → NO ESCAPING

  Netstrings (length:bytes,) are the key: the length tells the parser
  exactly where each field ends, so NO delimiter needs escaping.
  This is the same insight as git -z / find -print0 / xargs -0,
  but generalized to arbitrary remote command execution.
""")
