# contrib — companion helpers for driving remote tools through sic

Optional POSIX-sh helpers that pair `sic` with an **[lmcp](https://git.reauktion.de/marfrit/lmcp)**
server (a lightweight Lua MCP server). Install any of them on the hosts you reach with sic and
call them as `sic <host> <helper> ...`. None of these are required by sic itself.

## `lmcp-tool`
Call a host's lmcp (MCP) tools over a single stateless HTTP `tools/call`, with no persistent
MCP session:

```
sic <host> lmcp-tool list                          # discover the tools this host offers
sic <host> lmcp-tool web_search "query=today's news"   # call one, args as key=value
sic <host> lmcp-tool fetch url="https://example.com"
```

The `key=value` form is the important bit: a model never has to embed JSON in a shell string,
so apostrophes (`today's`, `what's`) ride safely inside double quotes and `lmcp-tool` does the
JSON-encoding. Raw JSON (`lmcp-tool <tool> '{"k":"v"}'`) still works. Host/port/token are read
from `$LMCP_HOST/$LMCP_PORT/$LMCP_TOKEN` or the local `lmcp.service` systemd unit. See the
[lmcp project](https://git.reauktion.de/marfrit/lmcp) for the server side.

## `sicwrite` / `sicedit`
`sicd` consumes stdin for the netstring frame, so a remote command can't be fed content via a
pipe. These pass content as **argv** instead (which sic delivers losslessly):

```
sic <host> sicwrite <path> <content>        # write a file
sic <host> sicedit  <path> <old> <new>      # replace one exact occurrence
```
