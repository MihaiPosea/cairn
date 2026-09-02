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

---

## M8 — the payoff: what needs re-running

The original plan was a build cache. Building the rest revealed a better shape with the same
insight and none of the migration cost: **`cairn affected`** takes what git says changed, walks the
graph backwards, and reports the files and tests that could possibly be impacted. Nothing to adopt,
nothing to configure, one line in CI.

Measured on `personal-website`: editing one file leaves **58% of the repo untouched**.

**This is where being wrong stops being cosmetic.**
A missed edge in a picture is a slightly wrong picture. A missed edge here is a test that should
have run and did not. So every condition that could make the answer unsound produces a **bail** with
a stated reason and the instruction to run everything:

- the repo contains `import()` with a computed path — files can be loaded invisibly
- the repo has any unresolved import — the graph is incomplete
- a changed file is not in the graph — a config change can affect anything

That last one fires on `tsconfig.json`, `next.config.mjs`, and every lockfile, which is correct:
change a tsconfig `paths` entry and every conclusion in this tool is void.

**The soundness limit is printed with every result**, not buried in documentation: this follows
import edges only, and tests that share a database, a fixture file, or global state are coupled in
ways no import graph can see.

**A bug found by using it.** The first real run reported `.cairn/parse-cache.gob` as a changed file
and bailed on cairn's own artifact. Fixed by writing `.gitignore` containing `*` inside the cache
directory, so it ignores itself rather than editing the user's `.gitignore`.

---

## Bug hunt

Eight bugs found by writing adversarial fixtures for cases real repos contain and the happy-path
tests never touched. Listed by how much damage each would have done.

**1. A library's entire contents reported as dead.**
Entry-point detection recognised framework conventions and nothing else. A published package's only
signal is its `package.json` — `main`, `module`, `exports`, `bin` — which was never read, so nothing
was an entry point, so every file was unreachable. The tool would have advised deleting the whole
codebase. Fixed by reading the manifest, and by refusing to answer at all when there are zero entry
points: reachability from an empty root set marks everything dead, and that is never a useful answer.

**2. Case-mismatched imports created phantom nodes.**
`import "./Utils"` when the file is `utils.ts` resolved through `os.Stat`, which is case-insensitive
on macOS and Windows. That produced a *second* node for the same file: the real one lost its inbound
edges, so it looked dead with a blast radius of zero, while the phantom took its place. It also hid
a bug that only surfaces on Linux CI. Fixed by replacing stat-per-candidate with cached directory
listings, which are case-exact — and 20% faster, since the ladder tries nine extensions per import
and one listing amortises across all of them. Mismatches are now reported.

**3. Imports escaping the repository root** created nodes with `../` paths that every traversal then
treated as project files.

**4. Bundler resource queries did not resolve.** `./shader.glsl?raw`, `./worker?worker`,
`./icon.svg#frag` — common in Vite and webpack, and silently inflating the unresolved rate.

**5. `import x = require("y")` was not extracted at all.** TypeScript's import-equals form nests its
string inside an `import_require_clause` rather than hanging it off the statement, so the
direct-child lookup missed it. Still common in older TypeScript and throughout `.d.ts` files.

**6. Specifiers containing escapes were truncated.** `"./with space"` became `"./with"` and then
failed to resolve for no visible reason — tree-sitter splits such a string into
fragment/escape/fragment and only the first was read.

**7. Aliased dependencies got the wrong name.** `"lodash-es": "npm:lodash@^4"` was keyed as
`lodash-es@npm:lodash`, a name nothing imports, so the package never joined to the code using it.
Splitting at the *last* `@` looks right and is wrong; the name ends at the first `@` that is not a
scope marker.

**8. "Unreadable lockfile" was the wrong words** for an npm v1 lockfile, which parses perfectly and
simply has no `packages` map. It sent people looking for a corrupt file.

**9 and 10. A typo'd path scanned "successfully".**
`filepath.WalkDir` reports a missing root through the callback, which ignores errors so that one
unreadable subdirectory cannot abort a whole scan. The consequence was that `cairn scan /typo/path`
printed "0 files, 0 imports, 0% unresolved" and exited 0 — indistinguishable from a clean scan of a
real repo. Same for passing a file instead of a directory. Both now fail before the walk starts.

Confirmed correct and left alone: BOMs, CRLF line numbers, empty and binary files, import
attributes, JSX in `.js`, decorators, 200 KB lines, dotted directory names, symlinked sources,
self-imports, circular package dependencies, malformed lockfiles of every format, and scoped names
across all four.

One known false positive is recorded rather than fixed: `require` shadowed by a local parameter is
still treated as a module import, because cairn does no scope analysis.

---

## Testing beyond examples

Example-based tests only check the cases the author thought of, which is the same set of cases the
code was written for. Four techniques that do not share that blind spot:

**Fuzzing.** Go's native fuzzer found a real bug in six seconds: `import "0\000"` decodes an octal
escape to a NUL byte, and that specifier then flowed into filepath handling, a node ID, the JSON
output and the HTML page. Specifiers containing control characters are now reported as unanalyzable
rather than dropped — something genuinely is being imported.

After the fix: 76k parser executions, 8.2M resolver executions, 13.7M JSONC-scanner executions, all
clean.

The fuzzers assert *properties*, not outputs, because a fuzzer has no idea what the right answer is:

- no input may make the resolver return a path that escapes the repository
- stripping comments must never change what valid JSON parses to — a mangled tsconfig would silently
  lose path aliases, and the only symptom would be a mysteriously high unresolved rate
- a parsed specifier may never contain a NUL or a newline

**Property tests over random graphs.** 660 randomly generated DAGs, checking that `TopoOrder` emits
every node exactly once and always after its dependencies; that a deliberately closed loop is always
detected and the reported path is a real walk through the graph; and that repeated runs on the same
graph are identical. Plus a 400-node complete graph, the worst realistic shape, to confirm it
terminates.

**A mutation soak.** The incremental index is where a bug is most likely and least visible: a stale
entry produces a plausible graph that is quietly out of date, and nothing in the output would say so.
So the soak edits, adds, deletes, renames and reverts files across 40 rounds, and after every single
step compares the cached scan against one with the cache disabled — node for node, edge for edge.

**An injection tripwire.** File paths and import specifiers both come off disk and are embedded in
the exported page. `encoding/json` escapes `<`, `>` and `&` by default, which is the only thing
making that safe — and it is exactly the kind of protection someone removes while prettifying output
with `SetEscapeHTML(false)`. The test asserts that exactly one `</script>` survives in the rendered
document, with a comment saying why.

**Concurrency, all under `-race`.** Twelve simultaneous scans of one repo agree node for node and
leave the shared cache usable. The resolver and the index hold up under sixteen goroutines. And two
repos containing byte-identical files never share cache entries.

---

## Repo shapes

"Works on any repo" is only worth claiming if it has been checked against the layouts people
actually have, so there is a fixture for each and the test fails loudly rather than producing a thin
graph.

| shape | status |
|---|---|
| Next.js app router | ✅ |
| Next.js pages router (baseUrl + paths) | ✅ |
| Vite / React SPA | ✅ |
| pnpm / npm / yarn monorepo | ✅ *fixed* |
| Vue single-file components | ✅ *added* |
| Svelte | ✅ *added* |
| Astro | ✅ *added* |
| Node / CommonJS backend | ✅ |
| Library with src + dist | ✅ |
| React Native platform extensions | ✅ *fixed* |
| Nested per-package tsconfig | ✅ *fixed* |

Four things this found:

**A monorepo produced a graph with no edges at all.** Every cross-package import — `@acme/ui`
— resolved to an external package instead of the source file in the next folder, and every
per-package tsconfig alias became a phantom dependency on a package named `@`. Measured on a
three-package fixture: three file nodes, zero edges. That is worse than an error, because it looks
like a working answer for a small project. Fixed by discovering workspaces from `package.json`
`workspaces` and `pnpm-workspace.yaml`, and by resolving aliases against the tsconfig *nearest the
importing file*.

**`extends` broke per-package aliases.** A single `baseURL` per config is wrong: TypeScript resolves
`paths` against `baseUrl` when one is declared and against *the config file that declares the paths*
when one is not. With a child extending a parent and neither declaring `baseUrl`, a shared base sent
the child's aliases to the parent's directory. Each alias rule now carries its own base.

**Vue, Svelte and Astro were invisible.** Not partially handled — the file types were skipped
outright, so a Vue app scanned as a handful of `.ts` utilities with no components, which reads as a
working scan of a much smaller project. Their imports are ordinary TypeScript wrapped in markup, so
the blocks are extracted and handed to the same grammar, with line offsets preserved so a reported
line points at the real line in the `.vue` file.

Rejected: adding three more tree-sitter grammars. One parser to keep correct beats four, and the
code inside a `<script>` really is just TypeScript.

**React Native platform extensions.** `./Button` resolving to `Button.ios.tsx` — every
platform-split component in an RN app was an unresolved import. Tried after the plain ladder so an
unqualified file always wins, which is what a bundler does too.

---

## Real repositories

Fixtures test what the author imagined; real repositories test what exists. Six were cloned and
scanned, and they immediately found what twelve hand-written fixtures had not.

| repo | files | imports | unresolved before | after |
|---|---|---|---|---|
| excalidraw | 668 | 4,692 | 0.32% | **0.00%** |
| svelte | 8,060 | 7,725 | 1.27% | 1.27% |
| vue-core | 538 | 2,153 | 2.42% | 2.14% |
| create-t3-app | 240 | 768 | 17.06% | **2.60%** |
| astro | 4,615 | 11,614 | 9.27% | **8.05%** |
| shadcn/ui | 3,947 | 19,895 | 28.98% | **23.24%** |

**A matched-but-missing alias short-circuited everything.**
shadcn maps `"react": ["./node_modules/@types/react"]`, and returning "unresolved" the moment an
alias matched meant `react` itself was reported as a broken import — 5,766 times. TypeScript
continues to `node_modules` when a path mapping finds no file, and so does cairn now. The phantom
`@` package this once guarded against is prevented instead by validating the package name, which is
where the check belonged.

**Framework virtual modules were reported as broken imports.**
`astro:content`, `virtual:uno.css`, `bun:sqlite`, `$app/stores`, `#imports`, `npm:`, `jsr:`,
`https:` — 546 in the Astro repo alone. They are recognised structurally, by the fact that `word:` is
not a file path, rather than by keeping a list of frameworks that would need updating.

**Bundler aliases live outside tsconfig.**
Vite, Rollup, webpack and Rspack declare them in JavaScript. Many projects mirror them into tsconfig
for the editor, which is why this hid for so long — the fixture that "passed" was only passing
because the alias silently became a phantom package. They are now read from the config's syntax
tree, evaluating far enough to take the last string literal, which covers `path.resolve(__dirname,
"./src")` and `fileURLToPath(new URL("./src", import.meta.url))` without executing anything.

### What is left is true

astro's remaining 935 and shadcn's 4,623 are **not cairn failing**. astro's point into `dist/`, which
does not exist in an unbuilt clone; shadcn's are `@/styles/base-nova/*`, files that repo generates
during its build. Both are correct findings about a fresh checkout.

But 23% unresolved reads as a broken tool, so the output now groups by shared cause and says so in
one line:

```
4618 of 4623 (100%) have the same cause: alias or generated path that does not
exist in a fresh checkout
```

Aggregating by category rather than by largest group is what makes that sentence true — astro's
spread across ninety-odd path prefixes, but 98% share one cause, and the biggest single group is
only 28%.

---

## The rest of the checklist

**Node subpath imports.** `#internal/*` declared in `package.json` `"imports"` were being written off
as virtual modules alongside `astro:` and `virtual:`. They are genuinely resolvable — the manifest
explains them — so they now resolve properly, using the nearest `package.json` and preferring
source-shaped conditions. A `#` specifier stays virtual only when no manifest accounts for it.

**Hostile filesystems.** A real machine has directories you cannot read, symlinks pointing nowhere,
symlinks pointing at themselves, and filenames nobody expected. All handled: an unreadable directory
does not abort a scan, symlink loops terminate (`filepath.WalkDir` does not follow them), and
unicode, emoji, spaces, quotes and semicolons in filenames all resolve. Sixty levels of nesting is
fine.

**Scale.** A generated 50,001-file repo with 74,843 imports:

| | |
|---|---|
| cold scan | 7.4 s, 264 MB peak |
| warm scan | 1.1 s, 122 MB peak |
| `cycles` | 1.1 s |
| `dead` | 1.0 s |
| `blast` | 2.1 s |
| `export` | **hung — over 2 minutes** |

That last one was a real bug. The web payload computed a *transitive* blast radius for every node in
order to decide which to draw — O(nodes × edges), or 50,000 × 74,843. Ranking now uses direct
dependents, one pass over the edges, and the exact transitive figure is computed only for the ≤1,200
nodes that survive, always against the full graph so the number stays true. Export went from a hang
to 7 seconds.

**TypeScript project references** needed no change, which was worth confirming rather than assuming:
a composite monorepo imports across packages by package name, which workspace resolution already
handles. There is a fixture proving it.

**SvelteKit's generated `./$types`** is written into `.svelte-kit` by `svelte-kit sync`, so it is
absent from a fresh checkout and looked like a broken relative import. It was most of what remained
in the TanStack Query repo.

## Nine real repositories

| repo | files | imports | unresolved |
|---|---|---|---|
| excalidraw | 668 | 4,692 | **0.00%** |
| nx | 5,440 | 21,120 | 0.25% |
| tanstack-query | 1,230 | 4,458 | 0.36% |
| turborepo | 1,284 | 3,255 | 1.20% |
| svelte | 8,060 | 7,725 | 1.27% |
| vue-core | 538 | 2,153 | 2.14% |
| create-t3-app | 240 | 768 | 2.60% |
| astro | 4,615 | 11,614 | 8.05% |
| shadcn/ui | 3,947 | 19,895 | 23.24% |

75,680 imports across nine repositories. The two high numbers are true findings about an unbuilt
checkout, not failures — and the tool now says which, in one line.

---

## Imports of unbuilt output

A monorepo package routinely imports its own compiled output —
`../../../dist/core/errors/index.js` — which does not exist until the repo is built. In the Astro
repo that was 917 imports, 98% of everything unresolved, and the first instinct was to call it
unfixable: cairn does not run builds, and never should.

But the compiled file is a build of a source file that *is* present, and for a dependency graph the
source is the better endpoint — it is the same edge, and it is a file someone can open. So when a
path lands in a build directory with nothing in it, the source twin is tried:

```
packages/astro/dist/cli/check/index.js  →  packages/astro/src/cli/check/index.ts
```

Only ever after the literal path fails, so a repo that *has* been built resolves to its real output.

**Astro went from 8.05% unresolved to 0.15%.**

**Anchoring on the nearest build directory is wrong**, which cost 92 imports to notice.
`dist/types/public/common.js` contains two names from the table, and rewriting the inner one gives
`dist/src/public/common.js` — nonsense. Rewriting the outer one gives `src/types/public/common.ts`,
the real file. Outermost first.

### What genuinely cannot be resolved

shadcn/ui's 4,623 remain, and should. Its `@/styles/base-nova/*` imports name files the registry
produces during a build; `apps/v4/styles/` contains a README and nothing else. There is no source
twin because there is no source — resolving them would mean running the repo's build, which means
executing a stranger's code, which is not a trade this tool makes.

That is the honest line between the two: **a compiled file has a source you can find; a generated
file does not exist yet.** cairn resolves the first and reports the second.

---

## Driving the rate down

Nine real repositories, 75,680 imports. Each round of "what is still unresolved and why" found a
resolution rule that was missing rather than a repo that was broken.

| fix | effect |
|---|---|
| `.d.ts` in the extension ladder | astro 18→13, nx 52→30, vue 46→27, tanstack 15→5 |
| unbuilt output → source twin | astro 935→18 |
| package's own bundle → package entry | vue-core 27→1 |
| relative path into `node_modules` → the package | astro, nx |
| root-absolute `/x.svg` → nearest project's `public/` | turborepo |

**Declaration files were the largest single miss.** A repo importing `./utils` where only
`utils.d.ts` exists is entirely normal — ambient typings, generated declarations, `.d.ts`-only test
suites. They go last in the ladder, because an implementation should always beat its declaration.

**A package importing its own bundle** — `./dist/compiler-core.cjs.prod.js` — has no per-file source
twin, because the bundle is built from all of `src`. The package's own entry point is the right
endpoint: the import means "this package", and that is where its code starts.

**Static assets are relative to the project, not the repository.** `/typescript.svg` imported from
`examples/with-vite-react/apps/web/src/main.tsx` lives in that app's `public/`, not the monorepo's.
The nearest project root wins, the same way the nearest tsconfig does.

## Where it stops, and why

**Four imports out of 75,680 remain unexplained**, and each was checked by hand against the disk:
a Svelte playground referencing an `App.svelte` that is not there, an Nx `globals.d.ts` pointing at a
removed directory, a Turborepo docs page importing a JSON file that does not exist. They are broken
imports in those repositories.

Everything else that does not resolve is *named*:

| cause | meaning |
|---|---|
| test fixtures | the import is meant to fail; that is the test |
| scaffolding templates | the file appears when the template is used |
| codegen output | written by a framework or generator, never committed |
| an entire directory tree is absent | produced by a build — nobody typos the same path 4,611 times |
| native binaries | for platforms other than this one |
| build output with no source | run the repo's build |

The threshold for "systematic absence" is ten imports sharing a prefix. Set it lower and real broken
imports get excused; at ten, a genuine mistake still stands out as itself. There is a test for both
directions.

**Zero is the wrong target.** Astro's repository contains
`e2e/fixtures/errors/src/pages/import-not-found.astro`, whose entire purpose is to import something
that is not there. Resolving it would mean the tool had started lying, and every number above it
would stop meaning anything.

---

## Fifty-four repositories

Twelve hand-written fixtures test what the author imagined. Nine real repositories test what a few
real projects do. Fifty-four test the ecosystem — and every one of them found something the previous
tier could not.

Each repo is cloned shallow, scanned, recorded, and deleted, so the sweep runs in bounded disk.

### What it found

**The worst bug in the project, and it was a false positive.**

```js
export default 'hello vite'
```

read as a re-export of a module named `hello vite`. The string is a direct child of the export
statement either way; only the `from` keyword separates a re-export from a plain exported value, and
that check was missing — since M1. An invented edge is worse than a missing one: a missing edge
understates the graph, a fabricated one puts a node in it that no code mentions. Removing it deleted
**105 phantom imports from the Vite repository alone**.

**Glob imports were dropping edges silently.** Parcel and Vite let one specifier match many files —
`../intl/*.json` pulls in every locale beside it. Treated as a single path it resolved to nothing,
so React Spectrum's dependency on all of those files was simply absent. A glob now becomes one edge
per match, because that is what the import depends on.

**The clustering key included the filename**, which split one shared cause into one group per file.
tldraw's auto-generated asset manifest imports 179 images from `./embed-icons/`, none of which exist
in a fresh clone — each became a group of one, so the systematic-absence rule never fired and all 179
read as separate mistakes.

**Five framework conventions**, each invisible until a repo that used it showed up:

| convention | repo that revealed it |
|---|---|
| `starters/` as a scaffolding directory | Qwik |
| `@qwik-router-config` — a bare `@name`, which npm forbids as a package | Qwik |
| `./+types/route` — React Router v7 typegen | React Router |
| `<sveltekit:generated>/server.js` — a build-time placeholder | SvelteKit |
| `*.gen.ts`, `*.generated.ts`, `gen/` | Cypress |

**Playground code got its own category.** A playground really runs, so "this is demo code" is a
different statement from "this import is meant to fail", and folding them together would hide real
breakage in a directory people read.


### Results

Fifty-four repositories, cloned fresh and scanned with one frozen binary:

| | |
|---|---|
| repositories | **54 scanned, 0 failures** |
| files | 175,152 |
| imports | 556,388 |
| unresolved | 3,043 (**0.55%**) |
| unexplained | 186 (**0.033%**) |
| repositories with zero unexplained | **29 of 54** |
| parse failures | **0** |
| total scan time | 143 s |

Every repository, largest first:

| repo | files | imports | unresolved | unexplained |
|---|---|---|---|---|
| n8n-io/n8n | 21,560 | 104,491 | 0.01% | 7 |
| mui/material-ui | 27,741 | 102,524 | 0.19% | 12 |
| calcom/cal.com | 5,061 | 26,098 | 0.04% | 6 |
| adobe/react-spectrum | 3,947 | 24,431 | 0.18% | 11 |
| TanStack/router | 8,890 | 24,391 | 0.80% | 12 |
| angular/angular | 7,109 | 23,309 | 0.26% | 15 |
| prisma/prisma | 4,551 | 22,903 | 0.03% | 0 |
| storybookjs/storybook | 4,973 | 19,149 | 0.74% | 14 |
| mantinedev/mantine | 5,483 | 18,752 | 0.02% | 3 |
| webpack/webpack | 14,102 | 17,327 | 5.21% | 11 |
| cypress-io/cypress | 4,897 | 14,629 | 2.58% | 21 |
| TanStack/table | 3,913 | 13,675 | 0.00% | 0 |
| ant-design/ant-design | 2,876 | 12,026 | 0.01% | 1 |
| tldraw/tldraw | 2,783 | 11,313 | 1.60% | 3 |
| facebook/react | 4,440 | 9,805 | 0.22% | 12 |
| rollup/rollup | 12,750 | 9,556 | 3.05% | 1 |
| QwikDev/qwik | 2,348 | 7,789 | 0.64% | 9 |
| chakra-ui/chakra-ui | 2,752 | 7,763 | 0.36% | 7 |
| microsoft/playwright | 1,556 | 6,860 | 0.29% | 5 |
| vitest-dev/vitest | 2,382 | 6,663 | 0.11% | 0 |
| drizzle-team/drizzle-orm | 971 | 6,360 | 0.00% | 0 |
| nestjs/nest | 1,914 | 6,281 | 0.05% | 0 |
| facebook/lexical | 1,375 | 6,229 | 0.02% | 1 |
| date-fns/date-fns | 1,633 | 5,326 | 0.00% | 0 |
| parcel-bundler/parcel | 2,948 | 4,525 | 1.15% | 1 |
| jestjs/jest | 2,272 | 4,522 | 0.55% | 9 |
| nuxt/nuxt | 1,490 | 4,022 | 0.10% | 0 |
| vitejs/vite | 1,568 | 3,840 | 0.29% | 0 |
| prettier/prettier | 5,857 | 3,413 | 4.10% | 0 |
| trpc/trpc | 903 | 3,381 | 0.03% | 1 |
| lit/lit | 1,107 | 3,181 | 0.57% | 9 |
| sveltejs/kit | 2,449 | 2,624 | 0.30% | 0 |
| remix-run/react-router | 698 | 2,532 | 0.36% | 0 |
| eslint/eslint | 1,485 | 2,373 | 1.60% | 4 |
| vuejs/core | 538 | 2,153 | 0.05% | 0 |
| tailwindlabs/headlessui | 417 | 1,644 | 0.06% | 0 |
| colinhacks/zod | 510 | 1,424 | 0.07% | 0 |
| honojs/hono | 385 | 1,230 | 0.08% | 0 |
| fastify/fastify | 295 | 1,203 | 12.88% | 0 |
| radix-ui/primitives | 330 | 1,187 | 0.00% | 0 |
| elysiajs/elysia | 242 | 751 | 0.00% | 0 |
| axios/axios | 237 | 704 | 0.00% | 0 |
| preactjs/preact | 241 | 695 | 1.44% | 2 |
| pmndrs/jotai | 181 | 644 | 0.00% | 0 |
| reduxjs/redux | 199 | 564 | 0.00% | 0 |
| mobxjs/mobx | 199 | 445 | 0.90% | 0 |
| expressjs/express | 141 | 403 | 0.00% | 0 |
| tannerlinsley/react-charts | 118 | 393 | 0.00% | 0 |
| solidjs/solid | 113 | 274 | 3.28% | 9 |
| sindresorhus/ky | 54 | 186 | 0.00% | 0 |
| pmndrs/zustand | 51 | 163 | 0.00% | 0 |
| immerjs/immer | 55 | 111 | 4.50% | 0 |
| lodash/lodash | 27 | 79 | 0.00% | 0 |
| testing-library/react-testing-library | 35 | 72 | 0.00% | 0 |

**What is still unresolved is named.** Of the 3,043:

| | |
|---|---|
| test fixtures whose imports are meant to fail | 1,725 |
| an entire directory tree absent, produced by a build | 485 |
| codegen output | 373 |
| scaffolding templates | 143 |
| build output with no source equivalent | 97 |
| native binaries for other platforms | 18 |
| playground and example apps | 16 |
| **unexplained** | **186** |

The 186 are 165 relative paths with no matching file, spread across 25 repositories — roughly seven
per repository, in half a million imports. Spot-checked by hand: Solid imports `./jsx.js` from nine
files and no such file exists anywhere in the package. They are broken imports in those repositories,
which is the floor a tool that refuses to invent resolutions can reach.

---

## M9 — the scoped navigator

**The view had no ceiling, and that was the whole problem.**
M7 chose containers over a breadcrumb so you could see where you were without reading one.
That was right about orientation and wrong about everything else: folders could be opened
anywhere, the whole repository stayed laid out around whatever was open, and drilling in *added*
detail without ever removing any. Measured on excalidraw — default 66 boxes and 301 edges;
after opening five nested folders, **268 boxes and 1,159 edges across a canvas 15,084 pixels
wide**. Someone who opened a folder to read four files got 1,159 lines across the screen.

The reader is now always inside exactly one container and only its immediate children are
drawn. Containers are gone, so a breadcrumb does the orientation job after all. Worst case
across nine repositories is now 253 boxes and **no** background lines; the typical level is
under 40.

**Three mechanisms, because one was not enough — and each was measured before it was chosen.**

*Scoping* bounds the lines. Root levels came out at 4–14 boxes and 1–25 edges on every repo.

*Quiet-collapse* bounds the boxes, which scoping does not: `svelte/tests/runtime-legacy/samples/`
is 1,209 children with zero edges among them, and `apps/v4/examples/base/` is 513. Children
nothing in the scope connects to fold into one box. It fires on five of the nine repos and costs
nothing on the others — `nx/src/command-line/` stays 33 boxes, `runtime-core/src/` stays 37.

*A threshold* bounds what is drawn. Dense levels survive both rules above:
`packages/excalidraw/components/` is 165 boxes and 362 edges. 362 strokes say strictly less than
none plus a sentence saying how many there are, so past 120 the level draws none and says so.
Selection still draws its own, which was the only legible thing there anyway.

**Edges are classified into five counted buckets, not filtered.**
Inside, self, crossing in, crossing out, wholly outside. Counted rather than inferred, so their
sum can be checked against the edge total — an edge belonging to no bucket is a connection the
reader never learns about, and that failure is silent by nature. Conservation is asserted at
every level of every repository. Whatever crosses the boundary goes to rails on either side
rather than being dropped.

**Rejected: splitting the dominant module and merging the tail.**
It over-split. Excalidraw's largest module became `packages/astro/test/fixtures/`-shaped noise
and its edge count went from 7 to 81. The rule that survived splits the largest module holding
at least a fifth of the repository, repeatedly, while the total stays within twelve — so a
monorepo shows its real top-level shape rather than either one box labelled `packages/` or nx's
fifty-seven workspace packages.

**Workspaces name modules; they do not choose the level.**
The resolver had been discovering them to resolve cross-package imports and throwing them away.
They are the truest boundary a repo declares about itself, so they supply names and entry
points — but grouping by them directly gives nx 57 boxes, which is not a picture.

**A correction to M7.** That section argued for *longest*-path depth so edges never point
backwards. True, and it is still longest path — but the raw numbers are not usable as labels:
on excalidraw depth reached **648**, not because anything is 648 imports deep but because that
is the longest chain the repository can string together. Depths are now ranks among the depths
that occur, which is monotonic and so preserves the property longest path was chosen for.
Within a scope they are recomputed over the few dozen boxes present, because a folder's global
depth is the minimum over everything inside it — at the root, where every module contains
something shallow, that collapsed the entire axis into one column.

**Counts left the header for a report.**
`cycles 5 · unreachable 14` says something is wrong without saying what, where, or whether it
matters. Each row now names the thing and what it costs — *change `packages/math/src/types.ts`
and 531 files are downstream of it* — and clicking one goes and looks at it. Module cycles lead,
because a loop between two files is often deliberate while a loop between two modules means the
boundary is not real.

**Bugs this cost, all caught by measurement rather than by reading the code.**
Edges touching the selection were culled with everything else, so selecting a node the camera was
not already on hid all of its connections — 98 edges drawn, 0 of them the selected node's own.
Selecting did not move the camera, leaving the selection 4,300px off-screen. The halo drew the
entire reachable set, 216 lines around a node with 10 real connections. And the scope's child
list was declared `const` while quiet-collapse reassigns it, which only fires on repositories
with large unconnected directories — neither excalidraw nor nx ever reached it.
