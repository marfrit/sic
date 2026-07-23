# sic — nested (multi-hop) design & chain-resolution decision

Status: **Accepted** — 2026-07-23. This records a decision and its rejected alternatives
*for the audit trail* — a future reader should see that a shared-store router was considered
and killed for cause, not overlooked.

## Goal

Make `sic host/hop1/hop2 … argv` real: run a command inside a container reached through a
chain of hops (`incus exec`, `docker exec`, `pct exec`, …) without a shell re-parsing argv at
any layer. The v2 wire-protocol **daemon** (`cmd/sicd`, `gateway/sicd`) already implements
one-layer-per-hop peeling; this covers the **client** and the rollout.

## Model (confirmed)

The **client builds the whole onion up front**: wrap the innermost command in a frame for hop
N, wrap that for hop N-1, … outermost for hop 1, and ship the nested blob to hop 1. **Each
`sicd` peels exactly one layer** (its own), runs its runtime verb with **`sicd` itself** as the
command (`incus exec <c> -- sicd`), and that inner `sicd` reads the next frame from stdin and
repeats. There is **no separate `sic-run` binary** — see Decision 3. Construction is recursive
and up-front; unrolling is iterative and distributed.

One frame = preamble (`MAGIC 0x00`, then 4-byte big-endian `len32`) + a djb netstring whose
content is `netstring(argc)` + argc argv netstrings + `payload`; the payload of a non-terminal
frame is itself a full frame for the next hop.

## Decision 1 — chain resolution: **static per-client config**

`/etc/sic/hosts.toml` declares, per host, the ordered runtime verbs for its hops
(`nest = ["incus", "docker"]`, etc.). The client reads it to turn `host/hop1/hop2` into the
concrete verb + address at each layer. **Fail loud on any mismatch** (unknown host, more hops
than the config declares) — never guess.

### Rejected: a shared memory store as the chain router

Resolving chains by querying a shared, mutable fleet-memory store was proposed and **rejected**.
Even with the bootstrap circularity removed (the store's own address is a flat local setting, so
a lookup is one direct call, not a nested one), it loses on four counts:

- **Trust (dispositive).** Such a store is *mutable and shared*. Any writer poisons one chain
  entry and every subsequent `sic` call through that chain execs the attacker's verb on the
  target host — a supply-chain RCE vector. Static config is immutable-after-deploy and auditable
  once.
- **Staleness fails the wrong way.** A moved/renamed container makes the stored chain lie, and
  the client *silently* execs into the wrong container. Static config fails loud (connection
  refused); a mutable store turns a hard error into quiet wrong-target execution.
- **Availability.** The store may live on a host that sleeps; resolution would die with it, and a
  local cache to cover that is just a slower, staler config file.
- **No net benefit.** The client needs a local store *address* regardless, so the store only adds
  a fragile network hop + cache to do what a flat file read already does.

A shared memory store may still be useful for *memory* — this decision is narrowly about
*routing*, nothing else.

## Decision 2 — migration: **daemon dual-read, daemon-first**

The v2 daemon hard-requires the `0x00` magic; the deployed v1 client sends a digit first, so a
naive v2 deploy breaks every existing call on the many live hosts already running v1. Instead the
daemon **sniffs the first byte** — `0x00` → v2 parser, digit → v1-legacy parser — a
once-per-connection branch, ~20 lines, zero perf cost. Ship the v2 daemon everywhere first; roll
clients independently, no flag day.

## Decision 3 — the chain link is `sicd` itself (no `sic-run` binary)

An earlier draft proposed a separate `sic-run` helper as the per-hop peeler. It is **not built**:
`sicd` is already a pure v2 frame-peeler, so the inner-hop command is just `sicd`
(`incus exec <c> -- sicd`), and one binary deploys instead of two (a deployment-simplicity
decision). **Verified empirically, not assumed** — `sicd` invoking `sicd` was run against a
two-layer nested frame (`build sicd; frame("sicd", frame("cat", payload)) | sicd`): the payload
reached the inner hop, exit status propagated, stderr clean. sicd calling sicd peels correctly,
so nothing about a nested hop needs behaviour sicd lacks. The only prerequisite is that **sicd is
present in every container a chain traverses** (a packaging/rollout item, not new code).

## Backward compat & the two original bugs

- A **bare `--`** must survive into the innermost argv untouched (the payload is opaque bytes;
  no hop interprets it) — closes the double-dash bug (#1).
- **stdin must be forwarded** into/through the frame — closes the silent zero-byte-file bug.
- A **bare host** with no `/` stays a single v1-style hop (backward compatible).

## Escape hatch (documented, not built)

If redeploying clients on chain changes ever costs more than the trust of a live store,
distribute **signed manifests out-of-band** — deterministic like static config, updatable like a
live store, without the mutable-store poison vector. Future work; explicitly not today's problem.

## Consequences

- Chain changes require redeploying clients (accepted: moves are infrequent, rollouts controlled).
- No network dependency and no mutable store in the routing path — routing is deterministic and
  auditable.
- Next: implement `cmd/sic` (config read + recursive frame build + `--`/stdin fixes) and the
  daemon dual-read; ensure sicd is deployed into target containers (no `sic-run` binary); both
  are Go.
