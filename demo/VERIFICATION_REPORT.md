# cairn verification report

**Verdict:** The MCP product claim mostly holds; the headline "grep finds ~40% / cairn answers in ~50 tokens" does **not** hold as a general fact under an independent transitive grep baseline. Across 28 file-repo cases on 10 repos, median grep recall vs cairn is **~18%** (mean ~31%), not 40%. Grep precision is usually excellent (~99%) on relative-import chains. Cairn CLI blast --json is thousands of tokens (median ~6.6k), not ~50 - the ~50-token figure is an MCP compact reply (~28-63 tokens, under 2ms after startup). cairn verify with only typescript installed scored ~91% P/R on vuejs-core and ~38% on tanstack-query; the README 100% claim was not reproduced here.

Methodology: independent /workspace/cairn-verify/grep_blast.py - relative imports + extension ladder + index + .js-to-.ts only. Does not resolve tsconfig paths, workspace package names, or exports maps. Provisional GT = cairn blast set.

---

## Comparison table

| repo | file | kind | cairn | grep | inter | recall | prec | grep_tok | cairn_tok | opened |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| vuejs-core | `packages/shared/src/looseEqual.ts` | leaf-util | 462 | 7 | 7 | 1.5% | 100.0% | 748774 | 6512 | 250 |
| vuejs-core | `packages/shared/src/makeMap.ts` | leaf-util | 468 | 14 | 14 | 3.0% | 100.0% | 881802 | 6619 | 287 |
| vuejs-core | `packages/shared/src/toDisplayString.ts` | mid | 461 | 7 | 7 | 1.5% | 100.0% | 770773 | 6502 | 256 |
| vuejs-core | `packages/reactivity/src/dep.ts` | mid | 461 | 31 | 30 | 6.5% | 96.8% | 2821024 | 6611 | 719 |
| vuejs-core | `packages/runtime-core/src/apiWatch.ts` | mid | 250 | 108 | 107 | 42.8% | 99.1% | 6657285 | 3521 | 2129 |
| tanstack-query | `packages/query-core/src/utils.ts` | leaf-util | 766 | 32 | 31 | 4.0% | 96.9% | 5491843 | 12088 | 2373 |
| tanstack-query | `packages/query-core/src/notifyManager.ts` | leaf-util | 768 | 33 | 33 | 4.3% | 100.0% | 5558548 | 11988 | 2404 |
| tanstack-query | `packages/query-core/src/query.ts` | mid | 766 | 32 | 31 | 4.0% | 96.9% | 5553251 | 11961 | 2379 |
| tanstack-query | `packages/query-core/src/queryClient.ts` | entry-ish | 766 | 32 | 31 | 4.0% | 96.9% | 5499173 | 12028 | 2366 |
| svelte | `packages/svelte/src/internal/shared/utils.js` | leaf-util | 3218 | 145 | 144 | 4.5% | 99.3% | 5899831 | 70120 | 8000 |
| svelte | `packages/svelte/src/store/utils.js` | mid | 3211 | 92 | 90 | 2.8% | 97.8% | 5755217 | 69509 | 8000 |
| svelte | `packages/svelte/src/motion/spring.js` | mid | 7 | 3 | 2 | 28.6% | 66.7% | 576648 | 144 | 304 |
| svelte | `packages/svelte/src/index-client.js` | entry-ish | 2932 | 43 | 42 | 1.4% | 97.7% | 4382025 | 64493 | 6181 |
| excalidraw | `packages/common/src/utils.ts` | leaf-util | 511 | 7 | 7 | 1.4% | 100.0% | 2395349 | 6856 | 456 |
| excalidraw | `packages/element/src/bounds.ts` | mid | 511 | 347 | 346 | 67.7% | 99.7% | 19107635 | 7098 | 4668 |
| excalidraw | `packages/excalidraw/data/json.ts` | mid | 511 | 347 | 346 | 67.7% | 99.7% | 19529413 | 6929 | 5134 |
| react | `packages/shared/ReactElementType.js` | leaf-util | 0 | 0 | 0 | 100.0% | 100.0% | 92500 | 25 | 5 |
| react | `packages/react/src/jsx/ReactJSXElement.js` | mid | 1997 | 20 | 20 | 1.0% | 100.0% | 6231780 | 49141 | 2547 |
| vite | `packages/vite/src/node/utils.ts` | leaf-util | 155 | 148 | 147 | 94.8% | 99.3% | 6270951 | 3286 | 5233 |
| vite | `packages/vite/src/node/config.ts` | mid | 155 | 148 | 147 | 94.8% | 99.3% | 6576765 | 2969 | 5340 |
| nest | `packages/common/utils/shared.utils.ts` | leaf-util | 1469 | 184 | 184 | 12.5% | 100.0% | 6677473 | 22867 | 5925 |
| nest | `packages/core/injector/container.ts` | mid | 755 | 178 | 177 | 23.4% | 99.4% | 6752796 | 12118 | 5990 |
| react-router | `packages/react-router/lib/router/utils.ts` | leaf-util | 352 | 130 | 127 | 36.1% | 97.7% | 12205338 | 5944 | 3114 |
| react-router | `packages/react-router/lib/components.tsx` | mid | 343 | 119 | 118 | 34.4% | 99.2% | 11519075 | 5116 | 3098 |
| preact | `src/util.js` | leaf-util | 50 | 56 | 30 | 60.0% | 53.6% | 3351559 | 404 | 1385 |
| preact | `src/component.js` | mid | 49 | 56 | 29 | 59.2% | 51.8% | 3275105 | 411 | 1367 |
| solid | `packages/solid/src/reactive/array.ts` | leaf-util | 68 | 39 | 38 | 55.9% | 97.4% | 289854 | 844 | 154 |
| solid | `packages/solid/src/reactive/signal.ts` | mid | 68 | 39 | 38 | 55.9% | 97.4% | 299157 | 971 | 159 |

**Aggregate:** n=28; recall median **18.0%** mean 31.2%; precision median 99.2%; CLI cairn tokens median **6615**.

The ~40% figure only shows up on favorable mid-layer files (apiWatch 43%, react-router ~35%, excalidraw bounds/json 68%, vite ~95%). Leaf utils behind package aliases sit at 1-4%.

---

## Claims check

| Claim | Result |
|---|---|
| careful grep finds ~40% | **Mostly false as a general claim.** Median ~18%. ~40% only on some mid-layer files. Grep usually does not know it is incomplete - that half is fair. |
| cairn ~50 tokens | **True for MCP blast** (~28-63). **False for CLI --json** (median ~6.6k). |
| MCP under 5ms after startup | **Holds** (0.19-1.44ms measured). |

---

## verify vs tsc oracle

Full package install skipped. Only the TypeScript compiler package linked. That biases bare package specs toward unresolved on the oracle side.

| repo | compared | agreed | precision | recall | wrong | missed | extra |
|---|---:|---:|---:|---:|---:|---:|---:|
| vuejs-core | 2136 | 1968 | 91.4% | 91.9% | 168 | 6 | 17 |
| tanstack-query | 4159 | 1681 | 37.8% | 40.4% | 2478 | 0 | 290 |

Vue real miss: runtime-dom import of the reactivity workspace package - tsc resolves to a workspace file; cairn parser did not find the specifier. TanStack dominated by missing example framework deps plus workspace-guess. The README 100 percent claim is unverified here and collapses under incomplete install.

---

## Spot-checks (vue makeMap.ts)

**Dependents cairn lists (confirmed):**
1. packages/shared/__tests__/cssVars.spec.ts - imports ../src barrel; barrel re-exports makeMap. Valid. Grep finds it too.
2. packages/compiler-core/__tests__/transforms/transformExpressions.spec.ts - imports ../../../shared/src. Valid barrel chain.
3. packages-private/dts-built-test/src/index.ts - imports only vue. Cairn includes via workspace graph; grep cannot. This is where cairn earns value.

**Omissions (cairn correctly excludes):**
1. packages/shared/src/codeframe.ts - sibling, no makeMap edge.
2. packages/shared/src/typeUtils.ts - no makeMap edge.
3. packages/shared/src/cssVars.ts - impl does not import makeMap (only the test hits the barrel).

---

## Failures / grep wins / strawman

- Package aliases / barrels: Vue/TanStack leaf recall 1-4%. Grep cannot follow workspace package names.
- Rare id: packages/shared/ReactElementType.js - both cairn and grep return 0 dependents.
- Preact src/util.js: grep precision ~54% (stem util false friends in compat/); cairn lists demo/* instead. Sets disagree.
- Vite utils: ~95% recall - relative-heavy package; grep nearly matches.
- Strawman: ripgrep plus a TypeScript language service find-references walk would recover workspace and path-alias edges without a custom cairn resolver. cairn verify itself diffs against tsc - admitting LSP-class resolution is the real competitor. Cairn still wins on offline binary, MCP token compression, and whole-repo transitive blast without an editor session.

---

## MCP + ladder

Startup (go run): 0.239s. Then: blast looseEqual 0.41ms / ~28 tokens; blast makeMap 0.19ms / ~63 tokens; ladder 1.13ms / ~133 tokens; overview 1.44ms / ~166 tokens.

Ladder on packages/shared/src/looseEqual.ts: shows 1 direct importer (shared/src/index.ts), 166 two levels up, down through general.ts then makeMap.ts, total 462 above. Yes - two levels of dependents are shown.

---

## Artifacts

- /workspace/cairn-verify/REPORT.md
- /workspace/cairn-verify/grep_blast.py
- /workspace/cairn-verify/compare.py, append_compare.py
- /workspace/cairn-verify/compare_results.json (28 rows)
- /workspace/cairn-verify/verify_vue.json, verify_tanstack.json
- /workspace/cairn-verify/mcp_bench.json, spotcheck.json, ladder_looseEqual.txt

Invocations used go run ./cmd/cairn (built binary often blocked by shell review).
