#!/usr/bin/env python3
"""Head to head, no extrapolation.

Question: list every file that breaks if I change X.

Route A - grep, done properly. BFS outward: grep the basename, read each hit,
keep the ones whose import actually resolves back to the file, recurse on
those. This is what a careful agent does, and every byte it reads is counted.

Route B - one cairn call.

Ground truth is cairn's graph, which is separately checked against the
TypeScript compiler, so route A is scored against a verified answer.
"""
import json, os, random, re, subprocess, statistics as st, time, sys

W = os.environ.get('REPOS', os.path.expanduser('~/cairn-bench-repos'))
CAIRN=os.path.expanduser('~/repos/cairn/cairn')
IMPORT=re.compile(r'''(?:from|import|require\()\s*['"]([^'"]+)['"]''')
CAP_READS = 4000   # a real agent gives up long before this

def resolves_to(root, importer, spec, target):
    """Does this import specifier in `importer` point at `target`?"""
    if not spec.startswith('.'):
        return False
    base = os.path.normpath(os.path.join(os.path.dirname(importer), spec))
    tnorm = os.path.splitext(target)[0]
    if base == tnorm or base == target:
        return True
    return base == os.path.dirname(tnorm) and os.path.basename(tnorm) == 'index'

def grep_bfs(root, start, budget_reads=CAP_READS):
    """Find the transitive dependents using only grep + reading files."""
    found, seen_targets = set(), set()
    queue = [start]
    reads = 0; tokens = 0; greps = 0
    while queue and reads < budget_reads:
        cur = queue.pop(0)
        if cur in seen_targets: continue
        seen_targets.add(cur)
        base = os.path.splitext(os.path.basename(cur))[0]
        greps += 1
        r = subprocess.run(['grep','-rl','--include=*.ts','--include=*.tsx',base,'.'],
                           cwd=root, capture_output=True, text=True)
        hits = [h[2:] if h.startswith('./') else h for h in r.stdout.split() if h]
        for h in hits:
            if h == cur or reads >= budget_reads: continue
            p = os.path.join(root, h)
            try:
                src = open(p, encoding='utf8', errors='replace').read()
            except OSError:
                continue
            reads += 1; tokens += len(src)//4
            if any(resolves_to(root, h, s, cur) for s in IMPORT.findall(src)):
                if h not in found:
                    found.add(h); queue.append(h)
    return found, tokens, reads, greps, (reads >= budget_reads)

class MCP:
    def __init__(self, root):
        self.p=subprocess.Popen([CAIRN,'mcp','--dir',root],stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,bufsize=1); self.n=0
        self.rpc('initialize')
    def rpc(self,m,params=None):
        self.n+=1
        self.p.stdin.write(json.dumps({"jsonrpc":"2.0","id":self.n,"method":m,
            **({"params":params} if params else {})})+"\n"); self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())
    def tool(self,n,a):
        r=self.rpc('tools/call',{"name":n,"arguments":a})
        return r['result']['content'][0]['text']
    def close(self): self.p.stdin.close(); self.p.wait(timeout=10)

def truth(root,f):
    out=subprocess.run([CAIRN,'blast',f,'--dir',root,'--json'],capture_output=True,text=True)
    d=json.loads(out.stdout or '{}')
    aff = d.get('affected') or []
    return set(aff), d.get('count') or 0

random.seed(31)
repos = sys.argv[1:] or ['vue-core','excalidraw','tanstack-query']
print(f"{'repo':<15}{'file':<30}{'true':>6}{'grep found':>11}{'reads':>7}{'grep tok':>11}{'cairn tok':>10}{'gave up':>9}")
print('-'*100)
rows=[]
for repo in repos:
    root=os.path.join(W,repo)
    if not os.path.isdir(root): continue
    files=subprocess.run(['git','ls-files'],cwd=root,capture_output=True,text=True).stdout.split()
    files=[x for x in files if x.endswith(('.ts','.tsx')) and 'node_modules' not in x]
    random.shuffle(files)
    m=MCP(root); picked=0
    for f in files:
        if picked>=3: break
        tset, tcount = truth(root,f)
        if not (5 <= tcount <= 400): continue
        picked+=1
        t0=time.time()
        got, gtok, reads, greps, capped = grep_bfs(root, f)
        gsec=time.time()-t0
        txt = m.tool('blast', {'file': f})
        ctok=len(txt)//4
        rows.append((tcount,len(got),reads,gtok,ctok,capped,gsec))
        print(f"{repo:<15}{f[-29:]:<30}{tcount:>6}{len(got):>11}{reads:>7}{gtok:>11,}{ctok:>10}{'  yes' if capped else '   no':>9}")
    m.close()
print('-'*100)
if rows:
    md=lambda i: st.median([r[i] for r in rows])
    print(f"median   true={md(0):.0f}  grep found={md(1):.0f}  reads={md(2):.0f}  "
          f"grep tokens={md(3):,.0f}  cairn tokens={md(4):.0f}")
    print(f"token ratio: {md(3)/max(md(4),1):,.0f}x")
    print(f"grep recall: {st.median([r[1]/max(r[0],1) for r in rows])*100:.0f}%   "
          f"runs that hit the read cap: {sum(1 for r in rows if r[5])}/{len(rows)}")
    print(f"grep wall time (median): {md(6):.1f}s")
