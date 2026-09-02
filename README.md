# cairn

**See what your software actually depends on.**

Point it at a repo it has never seen. No config, no migration, nothing to adopt.

Your `package.json` declares 13 dependencies. Your lockfile names 109. Your `node_modules` holds 51
of them and weighs 394 MB. cairn shows you the path between those numbers — and which of your own
files is responsible for it.

(Those are real figures from a small Next.js site. The gap between "locked" and "on disk" is optional
and platform-specific packages; most tools pick one of the two numbers silently.)

```
cairn scan .                 build the graph, summarise it
cairn blast lib/utils.ts     what breaks if you change this file
cairn dead                   files nothing reaches from an entry point
cairn why left-pad           the path that dragged this package in
cairn cycles                 import cycles, as readable chains
cairn cost framer-motion     packages and bytes this one import pulls in
cairn verify                 check the graph against TypeScript's own resolver
cairn serve                  open the graph in a browser
cairn export graph.html      one file you can send anyone, no server needed
```

## Why it exists

Every tool in this space does one half. `madge` and `dependency-cruiser` map the files you wrote.
`depcheck` and `knip` look at packages and dead code. Nothing joins the two, so nobody can answer the
question that actually matters: *this one import, in this one component, costs how much?*

It's also a learning project — written to understand how dependency resolution, incremental
indexing, and graph analysis really work. That part is not a disclaimer; it's the point.

## Honesty

Every scan prints an **unresolved rate**: the share of import specifiers cairn could not resolve to a
file, a package, or a builtin. Resolution in JavaScript is genuinely hard — `exports` maps, path
aliases, conditional entry points, four competing lockfile formats — and any tool claiming perfection
is hiding its misses. cairn reports its own.

Correctness is measured, not asserted. `cairn verify` diffs the graph against **TypeScript's own
resolver**, specifier by specifier — 100% precision and recall across 10,442 imports so far. It also
reports which resolution rules the repo actually exercised, because a perfect score on a repo that
never uses path aliases says nothing about path aliases.

**This is not a supported product.** Issues may go unanswered.

## Status

- [x] M0 — graph core: nodes, edges, traversal
- [x] M1 — parse TS/JS with a real grammar
- [x] M2 — resolution: tsconfig paths, extension ladder, ESM TypeScript, index files
- [x] M3 — package graph from lockfiles (bun · npm · pnpm · yarn) and node_modules
- [x] M4 — the join, and the five answers
- [x] M5 — incremental index: 1.53s cold, 75ms warm on 5,000 files
- [x] M6 — verified against TypeScript's own resolver: 100% precision and recall on 10,442 imports
- [x] M7 — self-contained web view (`cairn serve`, `cairn export`)

## Scope

JavaScript and TypeScript, done properly, before anything else. Python and Go arrive later as
additional resolvers behind the same interface. A tool that is right about one ecosystem beats one
that is vaguely right about five.

## Develop

```
go test ./...
go run ./cmd/cairn scan ~/personal-website
```
