# cairn

Maps how a JavaScript or TypeScript codebase is wired together.

## What this is

I built this to learn how dependency graphs work. `madge`, `dependency-cruiser`,
`knip` and every language server already do this. Nothing here is new.

The hard part turned out to be resolution: turning a string like `"./utils"` into a real
file on disk. That took most of the time. The rest went on trying to break it and
writing down where it lost.

Most of my numbers got worse once I measured them properly:

| I said | it is actually | measured on |
|---|---|---|
| `verify` at 100% precision/recall | 91.82% / 90.79% | 11 repos, 18,199 specifiers |
| grep finds ~40% of the answer | 14% | 50 repos, 194 questions |
| answers cost ~50 tokens | true over MCP, ~6,600 for CLI JSON | both labelled now |

Four bugs I only found because I measured instead of using it:

- a directory called `build/` was skipped everywhere, hiding 95 files of hand-written source
- an `exports` map fell through to Go's randomised map iteration, so 319 edges in
  tanstack-query resolved differently between two runs of the same repo
- one malformed byte on the MCP pipe wedged the server. Silent, 0% CPU, looked like a hang
- a package importing itself by name resolved to an external package instead of its own file

## Running it

```
go install github.com/MihaiPosea/cairn/cmd/cairn@latest
cd ~/your-repo
cairn scan .
```

No config. Point it at a repo it has never seen.

```
cairn serve                          open the graph in a browser
cairn blast lib/utils.ts             what breaks if you change this file
cairn ladder <file>                  two levels up, two levels down
cairn grep <pattern> --from <file>   search, ordered by what is connected
cairn scope <file>                   the files a search has to cover
cairn context <file>                 what to read before changing this file
cairn drift --base main              what a change did to the architecture
cairn dead                           files nothing reaches from an entry point
cairn why left-pad                   the path that dragged this package in
cairn cycles                         import cycles, as readable chains
cairn cost framer-motion             packages and bytes one import pulls in
cairn affected --base main           what to re-run after your changes
cairn verify                         check the graph against TypeScript's resolver
cairn export graph.html              one file you can send anyone
cairn mcp                            serve the graph to a coding agent
```

Every command takes `--json`.

---

## Two levels up, two levels down

Down is what a file uses. Up is what uses it.

Editors solved down thirty years ago. Cmd-click a name and you land in the file it came
from, instantly, everywhere. Up never got solved. Find-references works on symbols, comes
back noisy, and cannot tell you that one file imports this and 166 files import that one.

So you know what you are standing on, because you wrote the import. You usually have no
idea what is standing on you.

```
$ cairn ladder packages/shared/src/looseEqual.ts

  ▲▲    166   two levels up, who needs the things that need this
  ▲       1   one level up, who needs this directly
              packages/shared/src/index.ts

  ●         packages/shared/src/looseEqual.ts

  ▼       1   one level down, what this stands on
  ▼▼      1   two levels down

  A foundation. It carries a large part of the repository, and a change here is
  felt a long way from where you make it.
  462 files above it in total, 2 below, 85% of the repository.
```

One direct importer. Find-references shows that one result and it looks safe to touch.
It is 85% of Vue.

The verdict uses the share of the repo above a file rather than the raw count. Thirty
files above you means something different in a 40-file repo than in a 4,000-file one.
There is a floor under it so that a two-file repo does not report everything as critical.

Same view in the browser. Click a file and it becomes the centre, two rungs up and two
down, each clickable.

---

## For an agent

The CLI is the wrong shape for this. Every invocation rebuilds the graph, which is 0.09s
on a small repo and 1.34s on nx. And one ladder rendered for a terminal is about 2,650
tokens, because it prints all 166 files on the far rung.

`cairn mcp` scans once and keeps the graph in memory:

| | CLI | MCP |
|---|---|---|
| per query, nx at 5,467 files | 1,340 ms | 0.6 to 3.7 ms |
| tokens for one ladder | 2,652 | 208 |

```
claude mcp add cairn -- cairn mcp --dir /path/to/repo
```

Tools: `ladder`, `blast`, `context`, `scope`, `search`, `overview`, `rescan`.

Replies are short on purpose. The counts carry the meaning, and you can ask again about
anything that looked interesting. `overview` is the one to run first in a codebase you do
not know: modules, sizes, what depends on what, cycle count.

Every token figure here is an MCP reply. The CLI's `--json` is much bigger. `blast --json`
runs to a median of about 6,600 tokens because it lists every affected path, which is fine
in a pipeline and useless in a context window.

JSON-RPC over stdio, written by hand, so the binary still has two dependencies.

---

## Search, ordered by the graph

grep and its descendants have spent thirty years getting faster at one question: which
files contain these bytes. None of them can order the answer. Hits come back in path
order, which is whatever order the directories happen to be named in.

Fine for a person, who reads three results and stops. Bad for an agent, which cannot tell
hit 3 from hit 180 without opening both, so it opens twenty.

Four files. `round` is in three of them:

```
src/math.ts         export function round(...)          the real one
src/cart.ts         import { round } from "./math"      uses the real one
billing/legacy.ts   function round(x) { return x|0 }    a different round entirely
```

`billing/legacy.ts` has its own private `round` and nothing to do with the others. grep
shows all five matching lines and cannot say which is which.

```
$ cairn grep round --from src/math.ts

src/math.ts:1  the file itself
    export function round(n: number) { return Math.round(n); }
src/cart.ts:1  1 hop up, breaks if you change it
    import { round } from "./math";

not connected to src/math.ts

billing/legacy.ts:2
    function round(x: number) { return x | 0; }
```

The order comes from resolving `"./math"` to a file and counting hops. Files that import
the anchor rank above files the anchor imports, since something that breaks when you
change the anchor matters more than something you merely call. Each hit carries the import
chain that put it there, so you can check the ranking instead of trusting it.

The graph does not know what any file means. grep does not know which file the import on
line three points at. You want both.

### 50 repositories

Cloned fresh, 194 questions of the form *"list every file that breaks if I change this
one"*. The grep route is written the way a careful agent would do it: grep the basename,
read every hit, keep the ones whose import resolves back, recurse. Every byte it reads is
counted. Ground truth is cairn's graph, which `cairn verify` checks separately against the
TypeScript compiler.

| | grep, done carefully | cairn |
|---|---|---|
| recall, median | 14% | 100% |
| recall, mean | 30% | 100% |
| questions where it found nothing | 42 of 194 | - |
| tokens read, total | 507,826,801 | 30,523 |
| per answer, over MCP | - | 149 tokens |
| latency over MCP | - | 0.5 ms |

Size is not what separates the repos. How they write imports is.

| grep does well | grep finds nothing |
|---|---|
| immer 100%, axios 97% | shadcn 0%, astro 0% |
| tldraw 91%, fastify 85% | nest 0%, zod 0% |
| rollup 82%, at 12,750 files | solid 0%, vitest 0% |

Rollup is 12,750 files and grep gets 82% of the answer. shadcn is 3,946 files and it gets
none. Relative imports like `./utils` can be followed with string manipulation. Aliases,
barrel re-exports and dynamic imports cannot, and grep does not tell you it failed to
follow them. It just returns a short answer.

Three caveats. Twenty of the 194 runs hit a 3,000-file read cap, so those recalls are
floors. The grep route only resolves relative paths, and one that parsed `tsconfig.json`
would score better, though writing that means writing a resolver. And mui/material-ui
failed to measure for harness reasons, so 49 of 50 counted.

Also across those 50: 0 scan failures, 423,697 imports, 0.12% median unresolved.

`bench/fifty.py` reproduces it. `bench/fifty-results.jsonl` is the raw output.

### 34 repositories

React, Angular, Vue, Svelte, Solid, Preact, Vite, Rollup, Astro, Nuxt, React Router,
TanStack, MUI, Chakra, Radix, Mantine, Redux, Zustand, Jotai, MobX, Express, Fastify,
NestJS, tRPC, Hono, Zod, date-fns, Vitest, Prettier, Excalidraw, tldraw, Lexical. Cloned
fresh, searched with identifiers taken from the files themselves. 111,463 files, 301,749
imports, ~1,700 searches, no failures.

| | median |
|---|---|
| hits a plain grep returns | 63 lines |
| of those, within one hop | 4 lines |
| ranked below the fold | 91% |
| share of the repo a search must cover | 6.0% |
| read-set from `cairn context` | 3 files, ~1,700 tokens |

The extremes say more than the median:

| repo | grep returns | actually connected | search scope |
|---|---|---|---|
| react | 1,188 lines | 2 | 0.1% |
| fastify | 1,400 lines | 12 | 0.7% |
| rollup | 177 lines | 2 | 0.0% |
| vue | 47 lines | 12 | 24% |
| excalidraw | 28 lines | 13 | 90% |

Excalidraw is where this does not work. Its files really can reach nearly all of each
other, so there is nothing to rank away, and it came last on both measures independently.
This helps in proportion to how modular the code already is. When it does not help, the
number it reports is the useful part.

---

## What a change did to the shape

`git diff` tells you what text changed. It cannot tell you that the change added a cycle,
coupled two parts that were separate, or stranded a file nothing reaches. Every individual
line looks reasonable.

This gets worse with generated code, which writes plausible code quickly with no memory of
why a boundary was there.

Appending one import to Vue's `reactivity/src/index.ts`, a two-line diff:

```
$ cairn drift --base main

  ✗ new import cycle: compiler-core/ast.ts → codegen.ts → … (104 files)
     104 files now import each other in a loop. It swallowed 3 smaller cycles
     covering 89 files, which were separate before, so this is one loop where
     there were 3
  ✗ a change now reaches 32% of the repository, up from 30%
```

Exits non-zero only on regressions, so it works as a CI gate without failing builds when
things get better. A cycle that vanished because a bigger one absorbed it is not counted
as an improvement.

---

## How correct is it

Every scan prints an unresolved rate: the share of specifiers cairn could not resolve.
Resolution in JavaScript is genuinely hard and any tool claiming perfection is hiding its
misses.

`cairn verify` diffs the graph against the TypeScript compiler, specifier by specifier.
Eleven repos with their real dependencies installed, 18,199 specifiers:

| | precision | recall | specifiers |
|---|---|---|---|
| ky | 98.93% | 98.93% | 187 |
| vuejs/core | 97.77% | 98.27% | 2,136 |
| zod | 97.11% | 97.31% | 1,411 |
| jotai | 96.43% | 96.43% | 644 |
| hono | 95.86% | 95.47% | 1,232 |
| mobx | 91.82% | 90.79% | 440 |
| rollup | 85.01% | 72.81% | 9,273 |
| solid | 83.39% | 83.09% | 271 |
| axios | 82.79% | 82.67% | 703 |
| fastify | 79.83% | 79.64% | 1,205 |
| preact | 49.64% | 49.64% | 697 |
| median | 91.82% | 90.79% | |

An earlier reading of 100% came from a smaller set and does not reproduce. A ~91% reading
from a run with only the compiler installed is wrong in the other direction. These are the
numbers.

### What the low scores actually are

Preact is the instructive one, so I went through all 351 of its disagreements by hand:

| | count | who is right |
|---|---|---|
| cairn picks `.js`, tsc picks `.d.ts` | 294 | cairn, for dependencies |
| tsc gave up, cairn resolved it | 46 | cairn |
| cairn gave up, tsc resolved it | 11 | tsc |

Preact ships `src/index.js`, 423 bytes of real code, next to `src/index.d.ts`, 10,742
bytes of types. Eleven such pairs across the repo. Asked to resolve `../src/index`, cairn
says `index.js` and tsc says `index.d.ts`. Both are right about their own question. tsc
resolves types; a `.d.ts` has no runtime behaviour, so changing one breaks nothing.

The 11 in the last row were a real bug. `import "../../hooks"` points at a directory that
has its own `package.json`. Node reads that manifest and follows `main`/`types` before
falling back to `index.*`. cairn went straight to `index.*`, so the dependency on that
whole package was missing from the graph. Fixed, and preact now has zero unresolved
specifiers that tsc can resolve.

So the score measures agreement with a type resolver. Where a repo ships declarations next
to implementations the two disagree by construction. That is the oracle's limit, not a
defect the number is hiding, and it is printed unfiltered.

Also fixed while measuring: a package importing itself by name, `import "preact/compat"`
inside preact, resolved to an external package instead of the local file. Node allows
self-reference whenever the manifest has an `exports` field, and libraries with subpath
exports do it constantly.

I tested the harness by sabotage. Corrupting alias substitution dropped precision to
2.83%, which is what a verifier needs to be able to do before its passing scores mean
anything.

### Where it says it does not know

`cairn dead` refuses to answer when a repo has no entry points instead of calling every
file dead. `cairn affected` refuses when the graph is incomplete. Computed `import()`
calls are recorded and reported, never guessed at.

Across 54 repos: 175,152 files, 556,388 imports, 0.55% unresolved, 0.033% unexplained, 29
of 54 at zero, 0 scan failures, 0 parse failures. Nearly every unresolved specifier is a
test fixture asserting that an import fails, a scaffolding template, or a binary for
another platform. cairn names the cause for each.

Getting that to zero would mean inventing resolutions for files that do not exist.

## What it handles

| | |
|---|---|
| Next.js, app router and pages router | yes |
| Vite, React SPA | yes |
| Monorepos: pnpm, npm, yarn, Turborepo, Nx | yes |
| TypeScript project references | yes |
| Vue, Svelte, Astro single-file components | yes |
| Node, CommonJS backends | yes |
| React Native platform extensions | yes |
| Libraries (`src` + `dist`, `exports` maps) | yes |
| Deno and Bun: `npm:` `jsr:` `https:` `bun:` | yes |
| Node subpath imports (`#internal/*`) | yes |
| Framework virtual modules (`astro:` `virtual:` `$app/`) | yes |

## The viewer

`cairn serve` opens the graph in a browser. `cairn export graph.html` writes the same
thing as a single file that loads nothing from the network.

It opens where the architecture is rather than at the filesystem root, which in a monorepo
is usually one level in. One level at a time, in numbered dependency columns: the first
column is what nothing imports, the last is the foundation. Click a box for what it is and
what depends on it, double-click to go inside. A **whole repo** tab draws every file at
once, coloured by module. A **findings** tab lists cycles, unreachable files and blast
radius by name rather than as counts.

## Speed

| | 5,000 files | 50,000 files |
|---|---|---|
| cold scan | 1.2 s | 7.4 s (264 MB) |
| rescan, unchanged | 0.08 s | 1.1 s (122 MB) |

Parse results are cached by content hash, not mtime. mtime changes on a fresh checkout and
does not change when a file is restored from a backup.

## Install

```
go install github.com/MihaiPosea/cairn/cmd/cairn@latest
```

One static binary, two dependencies. No C toolchain, no npm, and it cross-compiles
anywhere Go does, because the tree-sitter runtime is pure Go. `cairn verify` is the
exception, since it runs the real TypeScript compiler as its oracle.

## Scope

JavaScript and TypeScript first, done properly. Python and Go would come later as more
resolvers behind the same interface.

Out of scope on purpose: resolving into package internals, and guessing at dynamic
dependencies.

It is a side project I built to learn, not something I support. The measuring was the
point.

## Develop

```
go test ./...
go test -race ./...
go run ./cmd/cairn scan ~/some-repo
```
