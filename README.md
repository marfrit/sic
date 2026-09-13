# sic

Run a command on a remote host — or inside a container several hops deep —
without a shell re-parsing its arguments.

`ssh host touch 'a b'` creates two files: `ssh` joins its arguments with spaces
and hands the result to the remote login shell, which splits it again. `sic host
touch 'a b'` creates one file named `a b`. `sic` sends the argument vector to a
daemon on the host as length-prefixed fields, and the daemon calls `execvp` on
it. No shell parses the arguments.

## Components

- **`sic`** — client. Takes argv on the command line, frames it as netstrings,
  pipes the frame over `ssh` to `sicd`.
- **`sicd`** — daemon on the target host. Reads netstrings from stdin and runs
  the command.

Both are single static Go binaries (`cmd/sic`, `cmd/sicd`). A Python reference
implementation of the daemon is in `gateway/sicd`.

## Wire format

Each field is a netstring: `<length>:<bytes>,`. A frame is a mode field, zero or
more argument fields, and an empty terminator:

```
<mode> <arg>* 0:,
```

`argv = ["touch", "a b"]` is `4:exec,5:touch,3:a b,0:,`. The length prefix means
no byte needs escaping and no delimiter can collide with the payload.

| mode | selector | behaviour |
|------|----------|-----------|
| `exec` (default) | `4:exec,` | `execvp(argv)`, no shell. Arguments are passed through unchanged. |
| `sh` | `2:sh,` | one field passed to `sh -c`. Pipes, redirects, and globs work. |

## Usage

```
sic <host> <command> [arg ...]
sic --sh <host> '<shell string>'
sic <host> -- <command> [arg ...]      # explicit separator
```

```
sic host1 echo hello world
sic host1 touch 'a b'                # one file named "a b"
sic host1 echo '$HOME'               # literal, no expansion
sic --sh host1 'echo hi | wc -c'
```

## --kill: killing by pattern without killing yourself

```
sic --kill <host> <pattern> [-x] [--force] [-s SIGNAL]
```

`pkill -f <pattern>` cannot avoid matching the caller, because the caller's own
command line contains the pattern — they just typed it. Over ssh that kills the
session (`exit 255`); through an MCP shell tool it kills the `sh -c '(...)'`
wrapper and its forks. Measured on one box: **one self-hit over ssh, three over
lmcp**, against a single real target.

`--kill` ships an embedded Python script to the far end over stdin (so nothing
needs installing there) and excludes the caller's own process tree by
construction: this process, its descendants, its ancestors up to — but not
including — PID 1, and any fork carrying an ancestor's exact command line. A real
target never matches those.

```
sic --kill host1 llama-server          # list matches; signals nothing
sic --kill host1 llama-server --force  # send SIGTERM to the listed pids
sic --kill host1 llama-server -x       # match argv[0]'s basename, not a substring
sic --kill host1 'port 8095' -s KILL --force
```

Listing is the default on purpose: you see the pids before anything is signalled.
Exit status is `0` when there were matches, `1` when there were none.

Requires `python3` on the target.

## Nesting

The frame survives being wrapped again, so `sic` reaches *into* containers with no
shell anywhere in the path, at any depth. A target is a host followed by container
hops separated by `/`:

```
sic host1/app cat /etc/os-release     # into the 'app' guest on host1
sic host2/ct/svc touch 'a b'          # host2 -> ct -> svc, ONE file, three layers deep
```

Each host's hop runtimes come from `/etc/sic/hosts.toml`, one stanza per host:

```toml
[host1]
nest = ["incus"]            # host1/app     ->  incus exec app -- <argv>

[host2]
nest = ["pct", "docker"]    # host2/ct/svc  ->  pct exec ct -- docker exec svc -- <argv>
```

This is what separates `sic` from a shell helper script. The `ssh` equivalent —

```
ssh host2 "pct exec ct -- docker exec svc -- touch \"a b\""
```

nests quotes inside quotes inside quotes, and every layer re-splits the arguments; a
body containing a space, `$()`, a pipe, or a newline breaks a different layer each time.
`sic` frames the argv once and re-frames it at each hop, so `touch 'a b'` is one file
whether it runs on the host or three containers deep. No escaping, at any level.

## Build

```
go build -o bin/sic  ./cmd/sic
go build -o bin/sicd ./cmd/sicd
```

## Install

Client:

```
cp bin/sic ~/bin/sic
```

Daemon on each target host. `sicd` runs what it is sent, so it must only be
reachable over an authenticated transport. Pin it to a dedicated ssh key so the
target can run nothing else with that key:

```
scp bin/sicd host:/tmp/sicd
ssh host 'sudo install -m755 /tmp/sicd /usr/local/bin/sicd'
# ~/.ssh/authorized_keys on the target:
# command="sicd",no-port-forwarding,no-pty ssh-ed25519 AAAA...
```

Do not listen on a TCP or message-bus port.

## Test

```
printf '4:exec,2:id,0:,' | ssh host sicd
sic host echo hello world
```

## See also

- `docs/design.md` — wire format, exec modes, transport and security, prior art.
- `docs/design-nested.md` — how nested container hops resolve.
- `demos/quoting-hell.py` — six cases run both ways, plain `ssh` versus `sic`.
- `SKILL.md`, `SKILL-bg.md` — agent skill definitions (foreground and background).
