#!/usr/bin/env python3
"""Answering "what breaks if I change this file?" two ways.

WITHOUT cairn: the agent greps the basename, then must open each hit to see
whether it is a real import of this file or a coincidence. Cost is the grep
plus every file it has to read. Its answer is "files that mention the name".

WITH cairn: one tool call. Its answer is the resolved import graph.

Ground truth is cairn's verified graph, so this measures whether the grep
route even arrives at the right set — not only what it costs to get there.
"""
import json, os, random, re, subprocess, statistics as st, sys, time

W = os.environ.get('REPOS', os.path.expanduser('~/cairn-bench-repos'))
CAIRN = os.path.expanduser('~/repos/cairn/cairn')

class MCP:
    def __init__(self, root):
        self.p = subprocess.Popen([CAIRN, 'mcp', '--dir', root],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.n = 0
        self.call('initialize')
    def call(self, method, params=None):
        self.n += 1
        self.p.stdin.write(json.dumps({"jsonrpc":"2.0","id":self.n,"method":method,
            **({"params":params} if params else {})}) + "\n")
        self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())
    def tool(self, name, args):
        t = time.time()
        r = self.call('tools/call', {"name": name, "arguments": args})
        return r['result']['content'][0]['text'], time.time()-t
    def close(self):
        self.p.stdin.close(); self.p.wait(timeout=10)

def truth(root, f):
    """Direct importers, from the verified graph."""
    out = subprocess.run([CAIRN,'ladder',f,'--dir',root,'--json'],
                         capture_output=True, text=True)
    d = json.loads(out.stdout)
    return set(d['up'][0]['files']), d['reach']

def grep_route(root, f):
    """What an agent gets by grepping the basename, and what it costs."""
    base = os.path.splitext(os.path.basename(f))[0]
    r = subprocess.run(['grep','-rl','--include=*.ts','--include=*.tsx',
                        '--include=*.js','--include=*.jsx', base, '.'],
                       cwd=root, capture_output=True, text=True)
    hits = [h[2:] if h.startswith('./') else h for h in r.stdout.split() if h]
    hits = [h for h in hits if h != f]
    # The agent must open each to tell a real import from a coincidence.
    tokens = 0
    for h in hits[:60]:
        try: tokens += os.path.getsize(os.path.join(root, h)) // 4
        except OSError: pass
    return set(hits), tokens, len(hits)

random.seed(19)
print(f"{'repo':<16}{'files':>7}{'grep reads':>12}{'grep tok':>10}{'cairn tok':>11}{'recall':>8}{'precision':>11}")
print('-'*76)
rows=[]
for repo in ['vue-core','excalidraw','tanstack-query','nx','turborepo','svelte']:
    root = os.path.join(W, repo)
    if not os.path.isdir(root): continue
    files = subprocess.run(['git','ls-files'],cwd=root,capture_output=True,text=True).stdout.split()
    files = [x for x in files if x.endswith(('.ts','.tsx')) and 'node_modules' not in x]
    if len(files) < 30: continue
    random.shuffle(files)
    m = MCP(root)
    per=[]
    for f in files[:12]:
        try: true_imp, reach = truth(root, f)
        except Exception: continue
        if not true_imp: continue
        got, gtok, gn = grep_route(root, f)
        txt, dt = m.tool('ladder', {'file': f})
        ctok = len(txt)//4
        tp = len(true_imp & got)
        recall = tp/len(true_imp)
        prec = tp/len(got) if got else 0.0
        per.append((gn, gtok, ctok, recall, prec))
    m.close()
    if not per: continue
    med = lambda i: st.median([x[i] for x in per])
    print(f"{repo:<16}{len(files):>7}{med(0):>12.0f}{med(1):>10,.0f}{med(2):>11.0f}"
          f"{med(3)*100:>7.0f}%{med(4)*100:>10.0f}%")
    rows += per
print('-'*76)
med = lambda i: st.median([x[i] for x in rows])
print(f"{'median, all':<16}{'':>7}{med(0):>12.0f}{med(1):>10,.0f}{med(2):>11.0f}"
      f"{med(3)*100:>7.0f}%{med(4)*100:>10.0f}%")
print(f"\n  {len(rows)} files measured")
print(f"  token cost of the grep route vs one cairn call: {med(1)/max(med(2),1):.0f}x")
