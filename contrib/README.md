# contrib — companion helpers for driving remote tools through sic

Optional POSIX-sh helpers that pair `sic` with an **[lmcp](https://git.reauktion.de/marfrit/lmcp)**
server (a lightweight Lua MCP server). Install any of them on the hosts you reach with sic and
call them as `sic <host> <helper> ...`. None of these are required by sic itself.

## `lmcp-tool` (moved)
`lmcp-tool` used to live here. It is an lmcp client, so it moved to the lmcp project:
canonical source **[marfrit/lmcp `bin/lmcp-tool`](https://git.reauktion.de/marfrit/lmcp/src/branch/master/bin/lmcp-tool)**,
installed as `/usr/bin/lmcp-tool` by the `lmcp` package (>= 2.1.0). Usage is unchanged:

```
sic <host> lmcp-tool list                          # discover the tools this host offers
sic <host> lmcp-tool web_search "query=today's news"   # call one, args as key=value
```

## `sicwrite` / `sicedit`
`sicd` consumes stdin for the netstring frame, so a remote command can't be fed content via a
pipe. These pass content as **argv** instead (which sic delivers losslessly):

```
sic <host> sicwrite <path> <content>        # write a file
sic <host> sicedit  <path> <old> <new>      # replace one exact occurrence
```
