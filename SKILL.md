---
name: sic
description: Run a command on a remote host without shell quoting bugs. Lighter than a per-host MCP server.
---

# sic — remote command execution

`sic <host> <command> [arg ...]` runs the command on `<host>`. Arguments go to
`sicd` on the host as netstrings and are passed to `execvp` directly, so no
shell re-parses them. `ssh host touch 'a b'` makes two files; `sic host touch
'a b'` makes one file named `a b`.

## Usage

```
sic <host> <command> [arg ...]        # exec mode (default)
sic --sh <host> '<shell string>'      # sh -c on the host
sic <host> -- <command> [arg ...]     # explicit separator if host/command starts with --
```

Pass each argument as a separate argument to `sic`. Do not construct netstrings;
`sic` frames them.

```
sic host1 echo hello world
sic host1 touch 'my file.txt'
sic host1 echo '$HOME'                                  # literal, no expansion
sic host1 python3 -c 'import sys; print(sys.argv)' a 'b c'
sic --sh host1 'grep foo *.log | wc -l'
```

## exec vs --sh

- Default (exec): a program with arguments. Quoting cannot break, but there are
  no pipes, redirects, or globs.
- `--sh`: use only when you need a pipe, redirect, or glob. The single string is
  parsed by `sh -c` on the host.

## Deploy sicd to a new host

```
scp gateway/sicd <host>:/tmp/sicd            # Python reference; or bin/sicd (Go)
sic --sh <host> 'sudo install -m755 /tmp/sicd /usr/local/bin/sicd'
sic <host> id                                # verify
```

For long-running commands, use the background skill (`SKILL-bg.md`).
