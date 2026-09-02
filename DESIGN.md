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
