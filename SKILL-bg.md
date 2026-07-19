---
name: sic-bg
description: Run a long-running command on a remote host in the background via sic, capture its output to a log, and poll or follow it.
---

# sic-bg — background remote commands

For commands that outlive a single `sic` call (builds, downloads, soak tests).
The command is launched detached on the host, its output goes to a log file, and
you poll or follow the log with later `sic` calls. Built on `sic --sh`; no extra
binary. `sic --sh` delivers the launch string to the host's `sh -c` unchanged,
so there is only one shell to reason about.

Jobs live in `~/.sic-jobs/<name>.{log,pid,rc}` on the target host.

## Launch

```
sic --sh <host> 'mkdir -p ~/.sic-jobs && setsid sh -c '\''<command>; echo $? >~/.sic-jobs/<name>.rc'\'' >~/.sic-jobs/<name>.log 2>&1 </dev/null & echo launched <name> pid=$!'
```

`<command>` is a shell snippet (pipes and redirects allowed). `setsid` detaches
it from the ssh session, so it keeps running after `sic` returns. `<name>` is any
label. Example:

```
sic --sh boltz 'mkdir -p ~/.sic-jobs && setsid sh -c '\''make -j8 2>&1; echo $? >~/.sic-jobs/build.rc'\'' >~/.sic-jobs/build.log 2>&1 </dev/null & echo launched build pid=$!'
```

## Follow for a bounded time

`timeout` caps how long the follow runs, then returns. Use this to watch
progress without blocking:

```
sic --sh <host> 'timeout 20 tail -f ~/.sic-jobs/<name>.log'
```

## Snapshot the tail

No shell needed:

```
sic <host> tail -n 60 ~/.sic-jobs/<name>.log
```

## Status and exit code

```
sic --sh <host> 'p=$(cat ~/.sic-jobs/<name>.pid 2>/dev/null); kill -0 "$p" 2>/dev/null && echo running || echo "done rc=$(cat ~/.sic-jobs/<name>.rc 2>/dev/null)"'
```

The `.rc` file holds the command's exit code once it finishes.

## Stop a job

```
sic --sh <host> 'kill $(cat ~/.sic-jobs/<name>.pid) 2>/dev/null; echo stopped'
```

## Notes

- One `<name>` per job. Reusing a name overwrites its log.
- `setsid ... </dev/null` is required: without it the job is killed when the ssh
  session closes.
- Poll with the bounded `tail -f` or a snapshot; do not hold a `sic` call open
  for the whole job.
