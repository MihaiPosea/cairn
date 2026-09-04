# cairn

**See what your software actually depends on — and give an agent a way to find it.**

Abstraction has always been how software gets built. AI writing your code is the newest layer of
it, and the thing underneath hasn't gone anywhere. Something still has to be true about how your
files connect, whoever or whatever wrote them.

cairn works that layer out from the code itself. Point it at a repo it has never seen. No config,
nothing to adopt.

```
cairn mcp                            serve the graph to a coding agent over stdio
cairn ladder <file>                  where this file sits: two levels up, two levels down
cairn grep <pattern> --from <file>   search, ordered by what is connected to that file
cairn scope <file>                   the only files a search must cover — pipe it into grep
cairn context <file>                 what to read before changing this file
cairn drift --base main              what a change did to the architecture

cairn scan .                         build the graph, summarise it
cairn blast lib/utils.ts             what breaks if you change this file
cairn dead                           files nothing reaches from an entry point
cairn why left-pad                   the path that dragged this package in
cairn cycles                         import cycles, as readable chains
cairn cost framer-motion             packages and bytes this one import pulls in
cairn affected --base main           what needs re-running after your changes
cairn verify                         check the graph against TypeScript's own resolver
cairn serve                          open the graph in a browser
cairn export graph.html              one file you can send anyone, no server needed
```

Every command takes `--json`.

---

## Two levels up, two levels down

"Understand two levels up and two levels down" is usually said as a maxim about
seniority. In a codebase it is not a metaphor: a level is a position in the import
graph, and two levels is two hops. Down is what a file stands on. Up is what stands
on it.

Editors solved down thirty years ago. Cmd-click a name and you are in the file it
came from — one hop, instantly, in every editor. **Nothing solved up.** Find-references
is symbol-level and noisy; it cannot say "one file needs this, and 166 more need
that one", and it certainly cannot do two levels.

So everyone knows what they are standing on, because they wrote the import, and
almost nobody knows what is standing on them.

```
$ cairn ladder packages/shared/src/looseEqual.ts

  ▲▲    166   two levels up — who needs the things that need this
  ▲       1   one level up — who needs this directly
              packages/shared/src/index.ts

  ●         packages/shared/src/looseEqual.ts

  ▼       1   one level down — what this stands on
  ▼▼      1   two levels down

  A foundation. It carries a large part of the repository, and a change here is
  felt a long way from where you make it.
  462 files above it in total, 2 below — 85% of the repository.
```

One direct importer. Every editor's find-references shows that single result and
implies the file is safe to change. It is 85% of Vue.

The verdict reads the *share* of the repository above a file rather than the count,
because thirty files above you is the whole world in a small repo and a corner of a
large one — with a floor under it, since in a two-file repository everything is half
of it.

The same view is in the browser: click any file and it becomes the centre, two rungs
above and two below, each one clickable to walk to.

---

## For an agent

Two things have to be true before an agent can use any of this, and the CLI
satisfies neither.

It has to be **fast enough to ask repeatedly**. Every CLI invocation rebuilds the
graph — 0.09s on a small repository, 1.34s on nx. Fine once, useless ten times
while an agent works something out.

It has to be **cheap enough to read**. One ladder rendered for a terminal is about
2,650 tokens, because it lists all 166 files on the far rung.

`cairn mcp` scans once and holds the graph:

| | CLI | MCP |
|---|---|---|
| per query, nx at 5,467 files | 1,340 ms | **0.6–3.7 ms** |
| tokens for one ladder | 2,652 | **208** |

```
claude mcp add cairn -- cairn mcp --dir /path/to/repo
```

Tools: `ladder`, `blast`, `context`, `scope`, `search`, `overview`, `rescan`.

The replies are deliberately compact — counts carry the meaning, and the file
lists are recoverable by asking again about whatever looked interesting.
`overview` is the one to read first in an unfamiliar codebase: the modules, their
sizes, what depends on what, and the cycle count.

Hand-rolled JSON-RPC over stdio. No new dependencies — the binary still has two.

---

## The search half

Every tool in the grep family has spent thirty years getting faster at one question: which files
contain these bytes. None of them can *order* the answer, because the file system does not know
which files matter — so hits come back in path order, an order chosen by whoever named the
directories.

That is fine for a person, who reads three results and stops. It is the central problem for an
agent, which cannot tell hit 3 from hit 180 without opening both, so it opens twenty and guesses.

Four files. `round` appears in three of them:

```
src/math.ts         export function round(...)          the real one
src/cart.ts         import { round } from "./math"      uses the real one
billing/legacy.ts   function round(x) { return x|0 }    a different round entirely
```

`billing/legacy.ts` has its own private `round` and no relationship to the others. grep shows all
five matching lines and cannot tell you which is which.

```
$ cairn grep round --from src/math.ts

src/math.ts:1  the file itself
    export function round(n: number) { return Math.round(n); }
src/cart.ts:1  1 hop up — breaks if you change it
    import { round } from "./math";

— not connected to src/math.ts —

billing/legacy.ts:2
    function round(x: number) { return x | 0; }
```

The ordering comes from resolving `"./math"` to an actual file — which needs the extension ladder,
`index` files, tsconfig `paths`, workspace links, `exports` maps — and then counting hops through
the resulting graph. Files that import the anchor rank above files the anchor imports, because a
file that breaks when you change something is more urgent than one you merely use. Every ranked
hit carries the import chain that justifies its position, so the order can be checked rather than
trusted.

**Neither half works alone.** The graph has no idea what any file means. grep has no idea which
file the import on line three refers to.

### Measured on 50 repositories

Cloned fresh, 194 questions of the form *"list every file that breaks if I change this
one"*. The grep route is implemented the way a careful agent would work it — grep the
basename, read every hit, keep the ones whose import actually resolves back, recurse —
and every byte it reads is counted. Ground truth is cairn's graph, which `cairn verify`
checks separately against the TypeScript compiler.

| | grep, done carefully | cairn |
|---|---|---|
| recall, median | **14%** | 100% |
| recall, mean | 30% | 100% |
| questions where it found nothing at all | **42 of 194** | — |
| tokens read, total | **507,826,801** | **30,523** |
| per answer | — | 149 tokens |
| latency over MCP | — | 0.5 ms |

What separates the repositories is not size — it is how they write imports.

| grep does well | grep finds nothing |
|---|---|
| immer 100%, axios 97% | shadcn 0%, astro 0% |
| tldraw 91%, fastify 85% | nest 0%, zod 0% |
| **rollup 82%, at 12,750 files** | solid 0%, vitest 0% |

Rollup is 12,750 files and grep recovers 82% of the answer; shadcn is 3,946 and it
recovers none. Relative imports (`./utils`) are followable by string manipulation.
Aliases, barrel re-exports and dynamic imports (`@/x`, `export *`, `await import(...)`)
are not — and grep does not report that it could not follow them. It returns a short
answer and stops.

Three things to hold against these numbers. Twenty of the 194 runs hit a 3,000-file read
cap, so those recalls are floors rather than final. The grep route resolves relative
paths only; one that parsed `tsconfig.json` would score better, though writing it means
writing a resolver. And one repository, mui/material-ui, failed to measure for harness
reasons and is excluded — 49 of 50 counted.

Also across those 50: **0 scan failures**, 423,697 imports, 0.12% median unresolved.

`bench/fifty.py` reproduces all of it; `bench/fifty-results.jsonl` is the raw output.

### Measured on 34 repositories

React, Angular, Vue, Svelte, Solid, Preact, Vite, Rollup, Astro, Nuxt, React Router, TanStack
(query/table/router), MUI, Chakra, Radix, Mantine, Redux, Zustand, Jotai, MobX, Express, Fastify,
NestJS, tRPC, Hono, Zod, date-fns, Vitest, Prettier, Excalidraw, tldraw, Lexical — cloned fresh,
searched with identifiers taken from the files themselves. 111,463 files, 301,749 imports, ~1,700
searches, no failures.

| | median |
|---|---|
| hits a plain grep returns | 63 lines |
| of those, within one hop | **4 lines** |
| **ranked below the fold** | **91%** |
| share of the repo a search must cover | **6.0%** |
| read-set from `cairn context` | 3 files, ~1,700 tokens |

The extremes matter more than the median:

| repo | grep returns | actually connected | search scope |
|---|---|---|---|
| react | 1,188 lines | **2** | 0.1% |
| fastify | 1,400 lines | 12 | 0.7% |
| rollup | 177 lines | 2 | 0.0% |
| vue | 47 lines | 12 | 24% |
| **excalidraw** | 28 lines | 13 | **90%** |

Excalidraw is the honest result. Its files really can reach almost all of each other, so there is
nothing for the graph to rank away — and it scored worst on both measures independently. **This
works in proportion to how modular the code already is**, and the number it reports when it does
not help is itself the most useful thing it can tell you.

---

## The change half

`git diff` answers "what text changed". Nothing answers "what did that do to the shape of the
program" — and that is where the damage happens. A change that adds a cycle, couples two parts
that were independent, or strands a file nothing reaches, does not look like anything in a line
diff. Every individual line is reasonable.

The failure mode is old. Generated code makes it constant, because something writing plausible
code very fast, with no memory of why a boundary was there, will cross it whenever crossing is the
shortest path.

Appending one import to Vue's `reactivity/src/index.ts` — a two-line diff:

```
$ cairn drift --base main

  ✗ new import cycle: compiler-core/ast.ts → codegen.ts → … (104 files)
     104 files now import each other in a loop. It swallowed 3 smaller cycles
     covering 89 files, which were separate before — so this is one loop where
     there were 3
  ✗ a change now reaches 32% of the repository, up from 30%
```

Exits non-zero on regressions only, so it works as a CI gate without failing a build when things
improve. A cycle that disappeared because a bigger one swallowed it is not reported as an
improvement — that would turn a clear regression into a wash.

---

## Correctness

**Every scan prints an unresolved rate** — the share of specifiers cairn could not resolve.
Resolution in JavaScript is genuinely hard, and any tool claiming perfection is hiding its misses.

**`cairn verify` diffs the graph against TypeScript's own resolver**, specifier by specifier: 100%
precision and recall across 10,442 imports. The harness was tested by sabotage, because a verifier
that cannot fail proves nothing — corrupting alias substitution dropped precision to 2.83%.

**Advice is labelled by confidence.** `cairn dead` refuses to answer when a repo has no entry
points, rather than declaring every file dead. `cairn affected` refuses when the graph is
incomplete. Computed `import()` calls are recorded and reported, never inferred.

Across 54 repositories: 175,152 files, 556,388 imports, 0.55% unresolved, **0.033% unexplained**,
29 of 54 at zero, 0 scan failures, 0 parse failures. Almost every unresolved specifier is a test
fixture asserting that an import *fails*, a scaffolding template, or a binary for another
platform. cairn names each cause.

Driving that to zero would mean inventing resolutions for files that do not exist, which is the
one thing a tool like this must never do.

## Project shapes it handles

| | |
|---|---|
| Next.js — app router · pages router | ✅ |
| Vite · React SPA | ✅ |
| Monorepos — pnpm · npm · yarn · Turborepo · Nx | ✅ |
| TypeScript project references | ✅ |
| Vue · Svelte · Astro single-file components | ✅ |
| Node · CommonJS backends | ✅ |
| React Native platform extensions | ✅ |
| Libraries (`src` + `dist`, `exports` maps) | ✅ |
| Deno · Bun — `npm:` `jsr:` `https:` `bun:` | ✅ |
| Node subpath imports (`#internal/*`) | ✅ |
| Framework virtual modules (`astro:` `virtual:` `$app/`) | ✅ |

## The viewer

`cairn serve` opens the graph in a browser; `cairn export graph.html` writes the same thing as one
file that reaches out to nothing at all.

It opens where the architecture is rather than at the filesystem root — for a monorepo that is
usually one level in — and shows one level at a time in numbered dependency columns, so the first
column is what nothing imports and the last is the foundation. Click a box for what it is and what
depends on it, double-click to go inside. A **whole repo** tab draws every file at once, coloured
by module. A **findings** tab lists cycles, unreachable files and blast radius as named findings
rather than counts.

## Performance

| | 5,000 files | 50,000 files |
|---|---|---|
| cold scan | 1.2 s | 7.4 s (264 MB) |
| rescan, unchanged | 0.08 s | 1.1 s (122 MB) |

Parse results are cached by content hash — never mtime, which changes on a fresh checkout and does
not change when a file is restored from backup.

## Install

```
go install github.com/MihaiPosea/cairn/cmd/cairn@latest
```

One static binary. Two dependencies. No C toolchain, no npm, cross-compiles anywhere Go does — the
tree-sitter runtime is pure Go. (`cairn verify` is the exception: it runs the real TypeScript
compiler as its oracle.)

## Scope

JavaScript and TypeScript, done properly, before anything else. Python and Go arrive later as
additional resolvers behind the same interface. A tool that is right about one ecosystem beats one
that is vaguely right about five.

Deliberately out of scope: resolving *into* package internals, and guessing at dynamic
dependencies.

**This is a learning project, not a supported product.** `DESIGN.md` records every decision, the
alternatives rejected, and the bugs found along the way — including the ones only real
repositories found, such as `build/` being skipped everywhere until a 34-repo sweep showed 95
files of hand-written source silently missing.

## Develop

```
go test ./...
go test -race ./...
go run ./cmd/cairn scan ~/some-repo
```
