#!/usr/bin/env python3
"""Fifty repositories, cloned fresh, four questions each.

For every repo: does cairn scan it at all, does the MCP server answer quickly
and briefly, and - the real test - how much of the true answer does a careful
grep route find, and what does it cost to find it.

Ground truth is cairn's own graph. That is only legitimate because `cairn
verify` checks it against the TypeScript compiler separately; where a
disagreement turned up during development it was cairn that was wrong and got
fixed. Spot-checked by hand on the worst case in this run.

Clone, measure, delete. One at a time so disk stays bounded.
"""
import json, os, random, re, shutil, statistics as st, subprocess, sys, time

WORK = os.environ.get('BENCH_WORK', os.path.expanduser('~/cairn-bench'))
CAIRN = os.path.expanduser('~/repos/cairn/cairn')
OUT = os.path.join(WORK, 'results.jsonl')
IMPORT = re.compile(r'''(?:from|import|require\()\s*['"]([^'"]+)['"]''')
EXT = ('.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.vue', '.svelte')
READ_CAP = 3000

REPOS = [
 "sveltejs/svelte","vuejs/core","facebook/react","angular/angular","solidjs/solid",
 "preactjs/preact","vitejs/vite","rollup/rollup","withastro/astro","nuxt/nuxt",
 "remix-run/react-router","TanStack/query","TanStack/table","TanStack/router","TanStack/form",
 "mui/material-ui","chakra-ui/chakra-ui","radix-ui/primitives","mantinedev/mantine",
 "tailwindlabs/headlessui","adobe/react-spectrum","reduxjs/redux","pmndrs/zustand",
 "pmndrs/jotai","mobxjs/mobx","expressjs/express","fastify/fastify","nestjs/nest",
 "trpc/trpc","honojs/hono","drizzle-team/drizzle-orm","elysiajs/elysia","colinhacks/zod",
 "date-fns/date-fns","axios/axios","immerjs/immer","sindresorhus/ky","vitest-dev/vitest",
 "jestjs/jest","cypress-io/cypress","prettier/prettier","eslint/eslint",
 "storybookjs/storybook","excalidraw/excalidraw","tldraw/tldraw","facebook/lexical",
 "shadcn-ui/ui","vercel/turborepo","nrwl/nx","t3-oss/create-t3-app",
]

def run(a, cwd=None, timeout=600):
    return subprocess.run(a, cwd=cwd, capture_output=True, text=True, timeout=timeout)

class MCP:
    def __init__(self, root):
        self.p = subprocess.Popen([CAIRN,'mcp','--dir',root], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.n = 0; self.rpc('initialize')
    def rpc(self, m, params=None):
        self.n += 1
        self.p.stdin.write(json.dumps({"jsonrpc":"2.0","id":self.n,"method":m,
            **({"params":params} if params else {})}) + "\n")
        self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())
    def tool(self, n, a):
        t = time.time()
        r = self.rpc('tools/call', {"name": n, "arguments": a})
        return r['result']['content'][0]['text'], (time.time()-t)*1000
    def close(self):
        try: self.p.stdin.close(); self.p.wait(timeout=10)
        except Exception: self.p.kill()

def resolves(importer, spec, target):
    if not spec.startswith('.'): return False
    base = os.path.normpath(os.path.join(os.path.dirname(importer), spec))
    t = os.path.splitext(target)[0]
    return base == t or base == target or (
        base == os.path.dirname(t) and os.path.basename(t) == 'index')

def grep_bfs(root, start):
    """The transitive dependents, found the way a careful agent would."""
    found, done, q = set(), set(), [start]
    tokens = reads = greps = 0
    while q and reads < READ_CAP:
        cur = q.pop(0)
        if cur in done: continue
        done.add(cur)
        base = os.path.splitext(os.path.basename(cur))[0]
        greps += 1
        r = run(['grep','-rl','--include=*.ts','--include=*.tsx','--include=*.js',
                 '--include=*.jsx', base, '.'], cwd=root, timeout=120)
        for h in (x[2:] if x.startswith('./') else x for x in r.stdout.split()):
            if h == cur or reads >= READ_CAP: continue
            try: src = open(os.path.join(root,h), encoding='utf8', errors='replace').read()
            except OSError: continue
            reads += 1; tokens += len(src)//4
            if any(resolves(h,s,cur) for s in IMPORT.findall(src)):
                if h not in found: found.add(h); q.append(h)
    return found, tokens, reads, greps, reads >= READ_CAP

def measure(root, rng):
    files = run(['git','ls-files'], cwd=root).stdout.split()
    files = [f for f in files if f.endswith(EXT) and 'node_modules/' not in f]
    if len(files) < 30: return None
    rng.shuffle(files)

    m = MCP(root)
    cases, mcp_ms, mcp_tok = [], [], []
    picked = 0
    for f in files:
        if picked >= 4: break
        try:
            d = json.loads(run([CAIRN,'blast',f,'--dir',root,'--json'], timeout=300).stdout or '{}')
        except Exception: continue
        true = set(d.get('affected') or []); n = d.get('count') or 0
        if not (5 <= n <= 500): continue
        picked += 1
        got, gtok, reads, greps, capped = grep_bfs(root, f)
        txt, ms = m.tool('ladder', {'file': f})
        mcp_ms.append(ms); mcp_tok.append(len(txt)//4)
        cases.append({'file': f, 'true': n, 'found': len(got & true), 'grepHits': len(got),
                      'reads': reads, 'grepTokens': gtok, 'cairnTokens': len(txt)//4,
                      'capped': capped, 'recall': len(got & true)/max(n,1)})
    m.close()
    if not cases: return None
    return {'cases': cases,
            'recall': st.median([c['recall'] for c in cases]),
            'grepTokens': st.median([c['grepTokens'] for c in cases]),
            'cairnTokens': st.median([c['cairnTokens'] for c in cases]),
            'reads': st.median([c['reads'] for c in cases]),
            'mcpMs': st.median(mcp_ms), 'mcpTokens': st.median(mcp_tok),
            'zeroRecall': sum(1 for c in cases if c['recall'] == 0)}

def main():
    done = set()
    if os.path.exists(OUT):
        for l in open(OUT):
            try: done.add(json.loads(l)['repo'])
            except Exception: pass
    for i, slug in enumerate(REPOS, 1):
        if slug in done: continue
        d = os.path.join(WORK, slug.replace('/','_'))
        shutil.rmtree(d, ignore_errors=True)
        rec = {'repo': slug}; t0 = time.time()
        try:
            if run(['git','clone','--depth','1','--quiet',
                    f'https://github.com/{slug}.git', d], timeout=900).returncode != 0:
                rec['status'] = 'clone_failed'; raise RuntimeError
            t = time.time()
            j = json.loads(run([CAIRN,'scan','--json',d], timeout=900).stdout)
            rec.update(status='ok', files=j['files_scanned'], imports=j['imports'],
                       unresolvedRate=j['unresolved_rate'], scanSec=round(time.time()-t,2))
            got = measure(d, random.Random(41))
            if got is None: rec['status'] = 'too_small'
            else: rec.update(got)
        except subprocess.TimeoutExpired: rec['status'] = 'timeout'
        except Exception as e:
            rec.setdefault('status','error'); rec['detail'] = str(e)[:120]
        finally:
            rec['seconds'] = round(time.time()-t0, 1)
            shutil.rmtree(d, ignore_errors=True)
            with open(OUT,'a') as fh: fh.write(json.dumps(rec)+'\n')
            print(f"[{i}/{len(REPOS)}] {slug:<28} {rec.get('status'):<12} "
                  f"files={rec.get('files','-')} recall={rec.get('recall')}", flush=True)

if __name__ == '__main__':
    main()
