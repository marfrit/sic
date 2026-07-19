# sic — design

A daemon (`sicd`) receives an argument vector over a stream and runs it, so the
caller does not pass a quoted string through a shell. A client (`sic`) frames
the argv and pipes it over ssh.

## Problem

Composing `shell → ssh → remote shell → program` re-parses arguments at each
layer. `ssh host touch 'a b'` creates two files: `ssh` concatenates its argv with
spaces and hands the string to the remote login shell, which re-splits it.
Escaping tricks (`base64`, heredocs, `printf %q`, `${var@Q}`) all exist to
survive that round-trip.

## Approach: transmit argv, then execvp

A shell flattens argv into one string and re-parses it. `sicd` skips that: it
receives the argument vector as discrete framed fields and calls `execvp(argv)`
(`subprocess.run(argv, shell=False)`). No shell touches the arguments, so quotes,
`$`, backticks, newlines, and spaces are ordinary bytes in a field.

This is the mechanism behind `find -print0` / `xargs -0` / `git -z`: frame argv
unambiguously instead of serialising it as a shell string.

## Wire format: netstrings

Each field is a netstring (djb): `<length>:<bytes>,`.

```
4:echo,            bytes "echo",        length 4
11:hello world,    bytes "hello world", length 11
0:,                empty netstring, used as a terminator
```

The length prefix means there is no delimiter to collide with and nothing to
escape. Netstrings are chosen over ASCII US/RS separators (`0x1F`/`0x1E`) because
those bytes can appear inside arbitrary payloads; netstrings are binary-safe and
need no escaping. The cost is a few header bytes per field.

### Frame grammar

```
<mode-netstring> <arg-netstring>* <empty-netstring>
```

- `mode-netstring` — first field, `4:exec,` or `2:sh,`.
- `arg-netstring*` — zero or more argument fields. In `exec` mode these are the
  elements of `argv` (first = program). In `sh` mode there is one field: the
  whole shell string.
- `empty-netstring` — `0:,` terminates the frame. The daemon runs it and resets.
  An empty terminator (not a separator) lets multiple frames pipeline in one
  connection.

### Examples

| Intent | Wire bytes |
|--------|------------|
| `argv = ["echo", "hello world"]` | `4:exec,4:echo,11:hello world,0:,` |
| `argv = ["touch", "a b"]` | `4:exec,5:touch,3:a b,0:,` |
| shell `echo hi \| wc -c` | `2:sh,15:echo hi \| wc -c,0:,` |
| two frames | `4:exec,2:id,0:,4:exec,7:uptime,0:,` |

## Exec modes

| mode | selector | behaviour | when |
|------|----------|-----------|------|
| `exec` (default) | `4:exec,` | `execvp(argv)`, `shell=False`. No pipes/redirects/globs. | Default path. |
| `sh` | `2:sh,` | single field to `sh -c`. Pipes, redirects, globs work; shell parsing is re-accepted for that one string. | Only when a pipeline/redirect/glob is required. |

`sh` mode is a per-frame opt-in, so shell parsing is never entered by accident.

## Transport and security

`sicd` runs what it is sent. That is remote code execution by design, so:

- Do not listen on an open TCP or message-bus port. An unauthenticated
  stream-to-exec endpoint is a backdoor.
- Run `sicd` over ssh: as the remote command on ssh stdin, or pinned to a key
  with `command="sicd"` in `authorized_keys`. This reuses ssh's authentication,
  encryption, and host identity; `sicd` adds only the framing.
- For same-host fan-out, a Unix domain socket with `0600` permissions is an
  option. Still not a TCP port.

The client invokes `ssh host sicd` with the frame on stdin, so the outer shell
sees a plain command with no special characters.

## Response framing

`sicd` inherits the child's stdout, stderr, and exit status. Structured results
(for example a netstring triple `<exit-code> <stdout> <stderr>`) are not
implemented; add them the same way if needed.

## Prior art

| Technique | Used for |
|-----------|----------|
| `find -print0` / `xargs -0` / `git -z` | Framing argv without a shell re-split. |
| netstrings (djb) | Length-prefixed, escaping-free, binary-safe framing. |
| `execve` / `subprocess(shell=False)` | Bypassing the shell for arguments. |
| the `ssh` argv gotcha | The case this removes (`ssh host touch 'a b'` → two files). |

## Implementation

- `cmd/sic` — Go client.
- `cmd/sicd` — Go daemon (single static binary).
- `gateway/sicd` — Python reference daemon (~40 lines).

Reading all of stdin is sufficient for the ssh-per-call model. A long-lived
socket daemon needs incremental parsing; `read_netstring` already returns the
unconsumed remainder for that.
