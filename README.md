# cairn

**See what your software actually depends on.**

Point it at a repo it has never seen. No config, no migration, nothing to adopt.

Your `package.json` declares 13 dependencies. Your lockfile names 109. Your `node_modules` holds 51
of them and weighs 394 MB. cairn shows you the path between those numbers — and which of your own
files is responsible for it.

*(Real figures from a small Next.js site. The gap between "locked" and "on disk" is optional and
platform-specific packages. Most tools pick one of the three numbers and print it without saying
which.)*

```
cairn scan .                 build the graph and summarise it
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
`depcheck` and `knip` look at packages and dead code. Nothing joins the two, so nobody can answer
the question that actually matters: *this one import, in this one component, costs how much?*

It is also a learning project — written to understand how dependency resolution, incremental
indexing, and graph analysis really work. That is not a disclaimer; it is the point. `DESIGN.md`
records every decision and the alternatives rejected, including the bugs found along the way.

## Honesty

**Every scan prints an unresolved rate** — the share of import specifiers cairn could not resolve to
a file, a package, or a builtin. Resolution in JavaScript is genuinely hard: `exports` maps, path
aliases, four competing lockfile formats, an ESM convention where `./x.js` means `x.ts`. Any tool
claiming perfection is hiding its misses.

**Correctness is measured, not asserted.** `cairn verify` diffs the graph against TypeScript's own
resolver, specifier by specifier:

| repo | imports compared | precision | recall |
|---|---|---|---|
| personal-website | 18 | 100% | 100% |
| travel-site | 57 | 100% | 100% |
| Personal Portfolio | 25 | 100% | 100% |
| generated, 5,001 files | 10,442 | 100% | 100% |

The harness was tested by sabotage, because a verifier that cannot fail proves nothing: corrupting
alias substitution dropped precision to 2.83%.

**`verify` also reports which rules the repo exercised.** A perfect score on a repo whose imports
are all relative says nothing about path aliases — and that is not hypothetical. `travel-site` has a
`@/*` alias configured and not one import that uses it.

**Advice is labelled by confidence.** "Declared but never imported" is a hint, not a finding, because
config files and plugins load packages by name. `cairn affected` refuses to answer at all when the
graph is incomplete, and says why.

## Performance

Generated 5,000-file repo, 10,442 imports:

| | wall clock |
|---|---|
| cold scan | 1.53 s |
| rescan, nothing changed | 0.075 s |
| rescan after editing one file | 0.083 s |

Parse results are cached by content hash — never mtime, which changes on a fresh checkout and does
not change when a file is restored from backup.

## Install

```
go install github.com/MihaiPosea/cairn/cmd/cairn@latest
```

One static binary, no C toolchain, no npm. Cross-compiles to any target Go supports — the tree-sitter
runtime is pure Go. (`cairn verify` is the one exception: it needs `node` and a `typescript` install
in the repo being checked, because it runs the real compiler as its oracle.)

## Scope

JavaScript and TypeScript, done properly, before anything else. Python and Go arrive later as
additional resolvers behind the same interface. A tool that is right about one ecosystem beats one
that is vaguely right about five.

Deliberately out of scope: resolving *into* package internals (packages are single nodes, which is
why `exports` maps never needed implementing), and dynamic dependency discovery. Computed
`import()` calls are recorded and reported, never guessed at.

**This is not a supported product.** Issues may go unanswered.

## Status

- [x] M0 — graph core: nodes, edges, traversals
- [x] M1 — parse TS/JS with a real grammar
- [x] M2 — resolution: tsconfig paths, extension ladder, ESM TypeScript, index files
- [x] M3 — package graph from lockfiles (bun · npm · pnpm · yarn) and node_modules
- [x] M4 — the join, and the five answers
- [x] M5 — incremental index: 1.53 s cold, 75 ms warm on 5,000 files
- [x] M6 — verified against TypeScript's own resolver: 100% on 10,442 imports
- [x] M7 — self-contained web view (`cairn serve`, `cairn export`)
- [x] M8 — `cairn affected`: what needs re-running after a change

## Develop

```
go test ./...
go test -race ./...
go run ./cmd/cairn scan ~/some-repo
```
