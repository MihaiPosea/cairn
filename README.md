# cairn

**See what your software actually depends on.**

Abstraction has always been how software gets built. AI writing your code is the newest layer of it —
and the thing underneath hasn't gone anywhere. Something still has to be true about how your files
connect, whoever or whatever wrote them.

cairn shows you that layer. Point it at a repo it has never seen. No config, no migration, nothing to
adopt.

```
cairn scan .                 build the graph, summarise it
cairn blast lib/utils.ts     what breaks if you change this file
cairn dead                   files nothing reaches from an entry point
cairn why left-pad           the path that dragged this package in
cairn cycles                 import cycles, as readable chains
cairn cost framer-motion     packages and bytes this one import pulls in
cairn affected --base main   what needs re-running after your changes
cairn verify                 check the graph against TypeScript's own resolver
cairn serve                  open the graph in a browser
cairn export graph.html      one file you can send anyone, no server needed
```

Every command takes `--json`.

## Why it exists

Every tool in this space does one half. `madge` and `dependency-cruiser` map the files you wrote.
`depcheck` and `knip` look at packages and dead code. Nothing joins the two, so nobody can answer the
question that actually matters: *this one import, in this one component, costs how much?*

Your `package.json` declares 13 dependencies. Your lockfile names 109. Your `node_modules` holds 51
of them and weighs 394 MB. cairn shows the path between those numbers, and which of your own files is
responsible for it.

## It has been checked on real repositories

Not fixtures — actual open-source projects, cloned and scanned:

| repo | files | imports | unresolved |
|---|---|---|---|
| excalidraw | 668 | 4,692 | **0.00%** |
| astro | 4,615 | 11,614 | 0.15% |
| nx | 5,440 | 21,120 | 0.25% |
| tanstack-query | 1,230 | 4,458 | 0.34% |
| turborepo | 1,284 | 3,255 | 1.01% |
| svelte | 8,060 | 7,725 | 1.24% |
| vue-core | 538 | 2,153 | 2.14% |
| create-t3-app | 240 | 768 | 2.60% |
| shadcn/ui | 3,947 | 19,895 | 23.24%\* |

\* shadcn is a *true finding*, not a failure: those imports name files its registry generates during
a build, and they exist nowhere in a fresh clone. cairn says so in one line rather than printing
4,623 identical errors:

```
4618 of 4623 (100%) have the same cause: alias or generated path that does
not exist in a fresh checkout
```

## And on every project shape

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

## Honesty

**Every scan prints an unresolved rate** — the share of specifiers cairn could not resolve. Resolution
in JavaScript is genuinely hard, and any tool claiming perfection is hiding its misses.

**Correctness is measured, not asserted.** `cairn verify` diffs the graph against **TypeScript's own
resolver**, specifier by specifier — 100% precision and recall across 10,442 imports. The harness was
tested by sabotage, because a verifier that cannot fail proves nothing: corrupting alias substitution
dropped precision to 2.83%.

**`verify` reports which rules the repo exercised.** A perfect score on a repo whose imports are all
relative says nothing about path aliases.

**Advice is labelled by confidence.** "Declared but never imported" is a hint, not a finding.
`cairn dead` refuses to answer when a repo has no entry points, rather than declaring every file
dead. `cairn affected` refuses when the graph is incomplete.

## Performance

| | 5,000 files | 50,000 files |
|---|---|---|
| cold scan | 1.2 s | 7.4 s (264 MB) |
| rescan, unchanged | 0.08 s | 1.1 s (122 MB) |
| rescan after one edit | 0.08 s | 1.1 s |

Parse results are cached by content hash — never mtime, which changes on a fresh checkout and does
not change when a file is restored from backup.

## Install

```
go install github.com/MihaiPosea/cairn/cmd/cairn@latest
```

One static binary. No C toolchain, no npm, cross-compiles anywhere Go does — the tree-sitter runtime
is pure Go. (`cairn verify` is the exception: it runs the real TypeScript compiler as its oracle, so
it needs `node` and a `typescript` install in the repo being checked.)

## Scope

JavaScript and TypeScript, done properly, before anything else. Python and Go arrive later as
additional resolvers behind the same interface. A tool that is right about one ecosystem beats one
that is vaguely right about five.

Deliberately out of scope: resolving *into* package internals — packages are single nodes, which is
why `exports` maps never needed implementing — and guessing at dynamic dependencies. Computed
`import()` calls are recorded and reported, never inferred.

**This is a learning project, not a supported product.** `DESIGN.md` records every decision, the
alternatives rejected, and all sixteen bugs found along the way — including the ones the tests caught
and the ones only real repositories did. Issues may go unanswered.

## Develop

```
go test ./...
go test -race ./...
go test -fuzz FuzzParse ./internal/lang/jsts/
go run ./cmd/cairn scan ~/some-repo
```
