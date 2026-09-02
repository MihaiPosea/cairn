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

## It has been checked on 54 real repositories

Not fixtures — actual open-source projects, cloned fresh and scanned with one frozen binary:

| | |
|---|---|
| repositories | **54 scanned, 0 failures** |
| files | 175,152 |
| imports | 556,388 |
| unresolved | 0.55% |
| **unexplained** | **0.033%** |
| repositories with zero unexplained | **29 of 54** |
| parse failures | **0** |

React, Angular, Vue, Svelte, Solid, Qwik, Preact, Lit, Next, Nuxt, SvelteKit, Vite, Webpack, Rollup,
Parcel, MUI, Chakra, Radix, Ant Design, Mantine, Headless UI, React Spectrum, Redux, Zustand, Jotai,
MobX, TanStack, Express, Fastify, NestJS, Prisma, tRPC, Hono, Drizzle, Elysia, Vitest, Playwright,
Jest, Cypress, Testing Library, Zod, Axios, Immer, date-fns, Lodash, tldraw, Lexical, n8n, cal.com,
ESLint, Prettier, Storybook, Turborepo, Nx.

"Unresolved" counts every specifier cairn could not point at a file. Almost none are mistakes: they
are test fixtures asserting that an import *fails*, scaffolding templates, codegen output, and
binaries for other platforms. cairn names each cause. What is left — **186 imports out of 556,388** —
was checked by hand and is genuinely absent from those repositories.

A smaller reference set, with the same measurement:

| repo | files | imports | unresolved | unexplained |
|---|---|---|---|---|
| excalidraw | 668 | 4,692 | 0.00% | **0** |
| vue-core | 538 | 2,153 | 0.05% | **0** |
| tanstack-query | 1,230 | 4,458 | 0.09% | **0** |
| astro | 4,615 | 11,614 | 0.10% | **0** |
| create-t3-app | 240 | 768 | 2.60% | **0** |
| shadcn/ui | 3,947 | 19,895 | 23.24% | **0** |
| nx | 5,440 | 21,120 | 0.14% | 2 |
| svelte | 8,060 | 7,725 | 0.94% | 1 |
| turborepo | 1,284 | 3,255 | 0.86% | 1 |
| **total** | **26,022** | **75,680** | **6.3%** | **4  (0.005%)** |

The second column is the one that matters. "Unresolved" counts every specifier cairn could not
point at a file — but in a healthy repository almost none of those are mistakes. They are test
fixtures asserting that an import *fails*, scaffolding templates whose files appear later, codegen
output, and binaries for other platforms. cairn names each cause, and what is left over —
**four imports out of 75,680** — was checked by hand and is genuinely absent from those repos.

That is the floor. Driving it to zero would mean inventing resolutions for files that do not exist,
which is the one thing a tool like this must never do.

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
