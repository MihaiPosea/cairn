# Design notes

Written as the thing is built, not afterwards. Each entry records what was
chosen and — more usefully — what was rejected and why.

---

## Phase 1 — the graph

**Config format: JSON, not YAML.**
Rejected YAML to keep the dependency count at zero for now. `encoding/json` is
in the standard library; YAML is not. Revisit if configs get painful to write
by hand.

**Ordering: Kahn's algorithm, not depth-first search.**
Both produce a valid topological order and DFS gets cycle detection almost for
free. Chose Kahn's because phase 4's concurrent scheduler needs exactly the same
structure — a count of unmet dependencies per job, decremented as jobs finish —
so implementing it here means the scheduler is a small step rather than a
rewrite.

**Determinism: ties broken by config order.**
When several jobs are ready simultaneously, the order between them is arbitrary.
Left arbitrary, the build output changes between runs and becomes untestable.
Breaking ties by the order jobs appear in the config makes runs reproducible.

**Cycle errors name the loop.**
`cycle detected` tells the user nothing. Kahn's algorithm can only report *that*
jobs are stuck, so naming the loop needs a second depth-first pass over the
remaining jobs, tracking the path. That extra pass only runs on the error path,
so it costs nothing in the normal case.

**`deps` and `inputs` are separate fields.**
`deps` orders jobs; `inputs` lists files to hash. Merging them would be
convenient now and wrong from phase 3, when a job may depend on another job
without reading any of its outputs.

<!-- ## Phase 2 — execution
     Decision: fail-fast vs keep-going, and why.
     Decision: how command output is captured and interleaved. -->

---

## M0 — the pivot, and the graph core

**The front door changed. The graph did not.**
The original design was a caching build system: you write a config listing jobs and their
dependencies, cairn runs it and skips work it has already done. The engineering was sound and the
entry point was fatal — using it meant migrating your entire build, which nobody does for a
stranger's project.

Same graph, different source. Instead of reading a config you hand-wrote, cairn extracts the graph
from the code itself. Nothing to adopt, so anyone can try it in ten seconds.

Rejected: keeping both front doors from the start. The caching work only becomes *safe* once the
inferred graph is measured as correct — a missed edge is cosmetic when you're drawing a picture and
catastrophic when you're skipping a build step. Caching is deferred to M8, after the correctness
harness exists.

**Nodes carry a Kind, and `Unresolved` is one of them.**
The obvious design drops specifiers it can't resolve. That hides precisely the failures worth
knowing about. Keeping them as first-class nodes makes the unresolved rate a number the tool prints
about itself on every run.

**Edges carry a Kind, and type-only imports are separate.**
`import type { X } from './y'` disappears when the code compiles. Counting it as a real dependency
would keep dead files alive and inflate the cost of an import. Nearly every tool in this space
conflates the two; this is the cheapest way to be more correct than them.

**Edges carry the specifier text and line number.**
"`utils.ts` is reachable" is not an answer. "`app/page.tsx:3` imports `'./utils'`" is. Provenance is
cheap to record at parse time and impossible to reconstruct afterwards.

**Insertion order is stored, and everything ties against it.**
Traversal order, tie-breaking, and output ordering all resolve against `Graph.Order`. Two scans of an
unchanged repo produce byte-identical output. Without that, none of this is testable.

**Cycles are an error today and will not be at M4.**
A cycle in a build graph means nothing can start, so erroring is correct. A cycle in an *import*
graph is legal and common — JavaScript permits circular imports and real codebases are full of them.
M4 replaces the error with strongly-connected-component condensation, so a cycle becomes a finding
rather than a failure. The erroring version is built first because it's what makes the reason for the
other one obvious.

---

## M3 — the package graph

**Four lockfile formats, and none of them agree.**
npm writes JSON keyed by install path. bun writes JSONC with positional arrays. pnpm writes YAML
with peer-dependency suffixes glued onto version keys. yarn writes two different formats depending
on major version. All four are supported, plus a `node_modules` fallback for repos with none.

`bun.lock` turned out to be JSONC — trailing commas and all — so it reuses the byte scanner written
for `tsconfig.json`. That is the argument for having written the scanner rather than pulling in a
JSON5 dependency: the second use case arrived within a day.

**Lockfile and disk are different questions, so both are reported.**
Measured on this machine: `personal-website` declares 13 packages, its lockfile names 109, and 51 are
actually installed. The 58-package gap is optional and platform-specific dependencies. Most tools
pick one number and print it without saying which. cairn reports all three and names the source it
read, because "how many dependencies do I have" has three defensible answers.

Rejected: choosing the lockfile as canonical. It is the more precise statement, but it describes
what *should* be installed. When someone is debugging why a build works locally and fails in CI, the
gap between the two is the answer, and hiding it would remove the most useful thing here.

**Packages are single units; their internals never enter the file graph.**
This was originally scoped to include `exports` map resolution — conditions, subpath patterns, the
whole swamp. Building it revealed the work was unnecessary: `exports` maps only matter if you resolve
*into* a package's source files, and the design treats each package as one node. Dropping it removed
the single largest chunk of planned work with no loss of capability.

**"Declared but never imported" is labelled a hint, not a finding.**
A package can be needed by a config file, a build plugin, or something loading it by name at runtime.
Reporting it as proof would make the tool confidently wrong in a way users would discover the hard
way. Verified against `personal-website`: the three it flags have zero references anywhere in source.

**"Imported but not declared" is reported as a real bug**, because it is one — the code works only
because something else happened to install the package, and it will break for the next person.

---

## M4 — the five answers

**Every question is a traversal; the work is choosing which edges count.**
The algorithms are textbook. Being careful about edge kinds is not, and it is where tools in this
space get people hurt:

- *Blast radius* follows **every** edge including type-only, because changing a type breaks the
  compile of everything importing it. The question is "what must I re-check", not "what ships".
- *Dead files* also follows type-only edges, for the same reason inverted: a file imported only for
  its types is not dead, and deleting it breaks the build.
- *Cost* follows only package-to-package edges.

**Entry-point detection is the real work in dead-code analysis.**
Reachability is trivial. Knowing where to start is not. Nothing "imports" a Next.js page — the
framework loads it by filename convention — so a naive implementation declares an entire app dead.
cairn recognises the app-router and pages-router conventions, middleware, instrumentation, config
files, ambient declarations, scripts and tests, and reports the reason each file was treated as an
entry point rather than asserting it silently.

**Confidence is lowered when the repo contains computed imports.**
`import(routeFor(slug))` cannot be followed statically, and the file it loads looks exactly like a
dead one. When any such call exists, every dead-file result says so.

**Tarjan's algorithm is iterative, not recursive.**
The textbook form recurses once per node; a 60,000-deep dependency chain overflows the goroutine
stack. There is a test for exactly that depth. Crashing on a large repo is the one failure this tool
cannot afford, because large repos are the ones that need it.

**`why` falls back from your code to package.json.**
Asking why `scheduler` is installed originally returned "nothing reaches it", which is true and
useless — nothing in your code imports it, but `react-dom` does. The query now tries your own files
first (the actionable answer) and falls back to what package.json declares, reporting which of the
two it used.

**A determinism bug the tests caught.**
Parse results arrive from the worker pool in whatever order the scheduler and disk decide, so nodes
discovered during resolution were added to the graph in a different order every run. The graph was
always equivalent, never identical — which silently breaks every golden test and makes two scans of
an unchanged repo diff against each other. Fixed by collecting results and processing them in sorted
path order: parsing stays parallel, graph construction becomes deterministic. The property was
claimed in the M0 notes and was not actually true until now.

---

## Performance — parser pooling

Measured on a generated 5,000-file repo with 10,442 imports:

| | wall clock | CPU time |
|---|---|---|
| parser per file | 5.60 s | 41.3 s |
| pooled per language | **1.52 s** | **6.5 s** |

A tree-sitter parser is expensive to construct relative to parsing one small
file, and the original code built one per file across eight workers. A
`sync.Pool` per language reuses them; parser instances are not safe to share
concurrently, but a pool hands each worker its own and takes it back.

The number matters more than the change: 41 s of CPU for 5,000 small files was
never plausible, and the only reason it was visible at all is that the target
(under 3 s cold) had been written down before the code was.

---

## M5 — the incremental index

Measured on the generated 5,000-file repo:

| | wall clock | cache |
|---|---|---|
| cold, empty cache | 1.53 s | 5,001 misses |
| warm, nothing changed | **0.075 s** | 5,001 hits |
| one file edited | **0.083 s** | 5,000 hits, 1 miss |

**Keyed by content hash, never by mtime.**
mtime changes on a fresh checkout, a `touch`, or clock skew without the file
changing, and does *not* change when a file is restored from backup. It is
wrong in both directions. Hashing costs one read, and the read has to happen
anyway — parsing is what actually costs.

**The extension is part of the key.**
The same bytes parsed as `.ts` and as `.tsx` produce different trees, because
`<T>x` is a type assertion in one and JSX in the other. Keying on content alone
would serve one file's parse for the other.

**A format version, checked on load.**
Without it, upgrading cairn silently serves parse results produced by the old
parser — a stale cache indistinguishable from a correct one. On any mismatch,
corruption, or decode failure the whole cache is discarded. That costs one slow
scan; trusting a bad cache costs a wrong answer.

**Written via temp file and rename**, because rename is atomic and a
half-written cache is permanent silent corruption.

**Entries for deleted files are kept.** They cost a few hundred bytes each and
make branch switching free: checking out an old branch finds its files already
cached. The whole 5,000-file cache is 528 KB.

Rejected: SQLite. The access pattern is "load everything at startup, save
everything at exit" — that is a file, not a database. A single gob file needs
no dependency and no schema migration.

---

## M6 — measuring whether the graph is true

**The oracle is TypeScript's own resolver, not a second implementation.**
`internal/verify` runs `ts.preProcessFile` and `ts.resolveModuleName` over the same files and diffs
the answers specifier by specifier. A reimplementation written by the same author would share the
same misunderstandings and agree for the wrong reasons.

Results, against TypeScript 5.9.3:

| repo | imports compared | precision | recall |
|---|---|---|---|
| personal-website | 18 | 100% | 100% |
| travel-site | 57 | 100% | 100% |
| Personal Portfolio | 25 | 100% | 100% |
| generated, 5,001 files | 10,442 | 100% | 100% |

**Three differences are counted as agreement, and all three are listed anyway** so the number cannot
hide behind them: non-code assets (`./globals.css`, which TypeScript refuses to resolve and cairn
resolves on purpose), Node builtins in files outside the tsconfig program, and cases where both
resolvers failed on the same specifier.

**A verifier that cannot fail proves nothing, so it was tested by sabotage.**
Corrupting the tsconfig alias substitution dropped precision from 100% to **2.83%** with 10,146
disagreements on the alias-heavy repo. The harness detects real breakage.

**But the same sabotage was invisible on `travel-site`** — which has a `@/*` alias configured in its
tsconfig and not one import that uses it. That is the important finding, and it is why
`verify` now reports **which rules the repo actually exercised**:

```
bare                 12
node-prefix          12
relative             33
```

No `tsconfig-paths` line. A perfect score on that repo says nothing whatsoever about alias handling.
Reporting coverage turns "we passed" into "we passed, on these rules" — which is the only version of
the claim that survives someone checking it.

---

## M7 — the view

**One self-contained HTML file, not a Next.js app.**
The plan called for a separate frontend. Building it revealed that a single embedded page does the
job better: it keeps cairn a single Go binary with no npm anywhere, and it makes `cairn export
graph.html` and `cairn serve` the *same artifact* rather than two implementations of one view. The
exported file is 19 KB for a 28-node repo, opens with a double click, and needs no server.

**Layered by depth, never force-directed.**
Every dependency visualiser that reaches for a force-directed layout produces the same hairball, and
the hairball is what people mean when they call these tools useless. Nodes sit in columns by their
*longest* path from an entry point — longest, not shortest, or a file reached both directly and
through five hops lands in the wrong column and its edges point backwards.

Depth uses `TopoOrder`, so the graph package's own algorithm does the work. When the graph has a
cycle `TopoOrder` correctly refuses, and layout falls back to bounded relaxation: inside a cycle
there is no correct depth, only a consistent one, and the bound is what stops it spinning.

**Truncation is stated, not silent.**
Above 1,200 nodes the view keeps the ones with the largest blast radius and says how many it left
out. A truncated view of the load-bearing files beats a complete view of nothing legible, but only
if the reader knows it happened.

**Clicking a node dims everything that is not upstream or downstream of it.** That is the whole
interaction: the blast radius made visible rather than printed as a number.

Verified in a real browser, not assumed: rendered, clicked a node, confirmed the panel showed
`imported by app/page.tsx:2` and the highlight followed the actual dependency chain.
