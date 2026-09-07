# cairn MCP blast demo

Portable stdio client that starts `cairn mcp`, lists tools, and calls `blast` twice
on the same file so you can see graph reuse (blast2 << blast1).

## Arrow flow

```
agent / Claude
    |
    |  MCP JSON-RPC over stdio (one JSON line per message)
    v
cairn mcp --dir <repo>     # parse once, hold graph in RAM
    |
    |  tools/call  name=blast  arguments={"file":"..."}
    v
dependents (compact text)  -->  agent context
    |
    |  tools/call blast again (same server process)
    v
same answer, lookup only   -->  blast2 much faster / same ~tokens
```

## Run

```sh
# from demo/
./run_demo.sh /path/to/target/repo
# optional second arg overrides default file:
./run_demo.sh /path/to/vuejs-core packages/shared/src/makeMap.ts
```

Or:

```sh
python3 blast_client.py --repo /path/to/repo --file packages/shared/src/makeMap.ts
# optional prebuilt binary:
python3 blast_client.py --repo /path/to/repo --file path/to/file.ts --cairn-bin /path/to/cairn
```

Prints: startup ms, tool names, blast1/blast2 timing + ~token estimate (`len(text)//4`).

## Claude Code wiring

Copy `.mcp.json` into the project (or merge into your Claude MCP config).
Replace:

- `REPLACE_WITH_TARGET_REPO_PATH` - repo cairn should index
- `REPLACE_WITH_CAIRN_REPO_ROOT` - absolute path to this cairn clone (cwd for `go run`)

## Artifacts

- `VERIFICATION_REPORT.md` - independent verify summary
- `compare_results.json` - raw compare numbers (keep as-is)
