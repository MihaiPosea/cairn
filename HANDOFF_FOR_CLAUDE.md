# Handoff for Claude (Mihai's agent)

Take over **MCP blast demo wiring** for cairn. This branch ships a portable demo client, shell runner, `.mcp.json` template, verification artifacts, and this note. Do not invent new product claims — wire what is here, run it, and report plainly.

## Style (Mihai)

Explain like Mihai:

- **X better when** / **Y better when** / **agents useful because**
- Prefer **arrow flows** over paragraphs of metaphor
- **No heavy analogies**

Example shape:

```
grep better when: you already know the symbol and the repo is small
cairn blast better when: you need transitive dependents without walking imports yourself
agents useful because: MCP returns compact dependent lists into context without an IDE
```

## What this branch is

```
demo/blast_client.py  -->  go run ./cmd/cairn mcp --dir <repo>  (or --cairn-bin)
                      -->  initialize + tools/list
                      -->  blast x2 on same file
                      -->  print startup / timings / ~tokens

demo/run_demo.sh <REPO> [FILE]   # FILE default: packages/shared/src/makeMap.ts
demo/.mcp.json                   # template: REPLACE_WITH_TARGET_REPO_PATH + REPLACE_WITH_CAIRN_REPO_ROOT
demo/README.md                   # arrow flow
demo/VERIFICATION_REPORT.md      # full verify writeup
demo/compare_results.json        # raw numbers — keep
```

`blast_client.py` sets `CAIRN_ROOT = Path(__file__).resolve().parents[1]` so it works from any clone, not only `/workspace/...`.

## Verification summary (do not oversell)

Independent verify (see `demo/VERIFICATION_REPORT.md`):

| Fact | Number |
|------|--------|
| Repos | 10 |
| File-repo cases | 28 |
| Median grep recall vs cairn | ~18% (not ~40%) |
| MCP blast payload | ~28–63 tokens, sub-ms after warm |
| CLI `blast --json` | median ~6.6k tokens |
| Verify completeness | Vue ~91%, TanStack ~38% (incomplete install) |
| Vs LSP / madge / knip | **not groundbreaking** as a dep-graph invention |

**Pitch that holds:**

- Agent **MCP without IDE** — graph as tools over stdio
- Not inventing dependency graphs; value is **blast / MCP / drift** into agent context, **greater than ranked search alone**
- Warm MCP blast is tiny vs dumping CLI JSON into the prompt

**X / Y / agents:**

- **Grep better when** you have a precise symbol and shallow fan-out
- **Cairn better when** you need transitive dependents (or drift) as a single tool result
- **Agents useful because** they can call MCP without opening VS Code / without pasting 6k-token JSON

## Next steps for you

1. **Run the demo** on a real JS/TS repo path you have locally
2. **Wire `.mcp.json`** — replace `REPLACE_WITH_*` paths; point Claude Code / your MCP host at this cairn clone
3. Optional: **better verify** (finish installs where TanStack-style cases were incomplete; do not claim 40% grep recall)
4. Report timings/tokens back to Mihai in the X/Y/agents style

## Commands

```sh
# from cairn repo root
go test ./...

# demo (from demo/ or via path)
./demo/run_demo.sh /path/to/target/repo
./demo/run_demo.sh /path/to/vuejs-core packages/shared/src/makeMap.ts

python3 demo/blast_client.py \
  --repo /path/to/repo \
  --file packages/shared/src/makeMap.ts

# optional prebuilt binary
python3 demo/blast_client.py \
  --repo /path/to/repo \
  --file path/to/file.ts \
  --cairn-bin /path/to/cairn
```

## Process

- Branch: `grok/mcp-demo-handoff`
- Commit message used: `demo: MCP blast client + Claude handoff`
- **No merge unless Mihai says**
- Do not push cloned JS fixture repos; do not force-push `main`

## Ownership

You own: MCP host wiring, running the demo end-to-end, tightening verify if asked.

You do not own: merging this branch, rewriting the product story past the numbers above, or shipping unrelated fixture trees.
