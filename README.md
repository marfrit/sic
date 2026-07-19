# sic

Run a command on a remote host without a shell re-parsing its arguments.

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
sic quotesu echo hello world
sic quotesu touch 'a b'                # one file named "a b"
sic quotesu echo '$HOME'               # literal, no expansion
sic --sh quotesu 'echo hi | wc -c'
```

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
- `demos/quoting-hell.py` — six cases run both ways, plain `ssh` versus `sic`.
- `SKILL.md`, `SKILL-bg.md` — agent skill definitions (foreground and background).
