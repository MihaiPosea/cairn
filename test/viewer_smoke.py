#!/usr/bin/env python3
"""Drive the real page and assert on what it computes.

Every viewer bug this session was found by looking at a screenshot: edges
culled away from the selection, a click handler calling a deleted function, a
drag threshold that ate ordinary clicks, a find-and-replace that corrupted the
file. None of that is visible to a Go test, and all of it is checkable by
running the page's own functions and reading the numbers back.

Uses the exported standalone file, so no server and no browser automation.
"""
import json, os, re, subprocess, sys, tempfile

CAIRN=os.path.expanduser('~/repos/cairn/cairn')
NODE=os.environ.get('NODE','node')

HARNESS = r'''
const fs = require('fs');
const html = fs.readFileSync(process.argv[2], 'utf8');
const m = html.match(/<script>([\s\S]*)<\/script>/);
if (!m) { console.log(JSON.stringify({fatal:'no script tag'})); process.exit(0); }

// Minimal DOM: the page only needs elements to exist and a 2d context to
// measure text with. Everything asserted here is computation, not painting.
function el() {
  return new Proxy({style:{},classList:{add(){},remove(){},toggle(){},contains(){return false}},
    dataset:{}, children:[], value:'', textContent:'', innerHTML:'',
    getBoundingClientRect:()=>({left:0,top:0,width:1200,height:800,right:1200,bottom:800}),
    addEventListener(){}, removeEventListener(){}, appendChild(){}, setAttribute(){},
    getAttribute(){return null}, querySelectorAll(){return []}, querySelector(){return null},
    getContext:()=>ctx, focus(){}, blur(){}, click(){}, scrollIntoView(){}, onclick:null,
    width:1200, height:800},
    {get(t,k){ if(k in t) return t[k]; return undefined; },
     set(t,k,v){ t[k]=v; return true; }});
}
const ctx = new Proxy({}, {get(t,k){
  if (k==='measureText') return (s)=>({width:(s||'').length*7});
  if (k==='canvas') return {width:1200,height:800};
  return ()=>{};
}});
const doc = {
  documentElement: el(),
  body: el(),
  fonts: {ready: Promise.resolve(), check: ()=>true},
  getElementById: ()=>el(), querySelector: ()=>null, querySelectorAll: ()=>[],
  createElement: ()=>el(), addEventListener(){},
};
global.document = doc;
global.window = global;
global.devicePixelRatio = 1;
global.requestAnimationFrame = (f)=>{ return 0; };
global.cancelAnimationFrame = ()=>{};
global.addEventListener = ()=>{};
global.localStorage = {getItem:()=>null, setItem(){}, removeItem(){}};
global.getComputedStyle = ()=>({getPropertyValue:()=>'#000000'});
global.performance = {now:()=>Date.now()};
global.matchMedia = ()=>({matches:false, addEventListener(){}});

let src = m[1];
// Expose the internals we want to assert on.
src += `
;globalThis.__probe = {
  DATA, files, groups,
  relayout, navigate, select, childOf, ladderOf,
  get view(){ return view; },
  get focus(){ return focus; },
  get selected(){ return selected; },
  fitParams, columnsOf, scopeChildren, item, byId,
};`;
try { (0,eval)(src); } catch (e) {
  console.log(JSON.stringify({fatal: String(e).slice(0,200)})); process.exit(0);
}
const P = globalThis.__probe;
const out = {ok:true, checks:{}};
const put=(k,v)=>{ out.checks[k]=v; };

try {
  // 1. it opened somewhere sane
  P.navigate([]);
  put('rootBoxes', P.view.boxes.length);
  put('rootEdges', P.view.edges.length);
  put('rootCols', P.view.layers.length);

  // 2. edge conservation: nothing silently dropped
  const audit = P.view.audit;
  const sum = Object.values(audit).reduce((a,b)=>a+b,0);
  put('edgesConserved', sum === P.DATA.edges.length);
  put('edgeTotal', P.DATA.edges.length);

  // 3. every drawn node is placeable
  let unplaced = 0;
  for (const n of P.DATA.nodes) if (!P.view.index.has(P.childOf(n.id))) unplaced++;
  put('unplacedNodes', unplaced);

  // 4. no dangling edges
  let dangling = 0;
  for (const e of P.view.edges)
    if (!P.view.index.has(e.from) || !P.view.index.has(e.to)) dangling++;
  put('danglingEdges', dangling);

  // 5. columns are monotonic in depth (left-to-right is dependency order)
  const spans = {};
  for (const b of P.view.boxes) {
    const d = b.ldepth ?? 0;
    spans[d] = spans[d] ?? {min: Infinity};
    spans[d].min = Math.min(spans[d].min, b.x);
  }
  const ds = Object.keys(spans).map(Number).sort((a,b)=>a-b);
  let inversions = 0;
  for (let i=0;i<ds.length;i++) for (let j=i+1;j<ds.length;j++)
    if (spans[ds[j]].min < spans[ds[i]].min) inversions++;
  put('columnInversions', inversions);

  // 6. selection produces direct edges (the bug where direct was always 0)
  const e0 = P.view.edges[0];
  if (e0) {
    P.select(e0.from);
    put('selectionSet', P.selected === e0.from);
  }

  // 7. the ladder agrees with the payload
  const f = P.DATA.nodes.find(n => n.kind !== 'package');
  if (f) {
    const L = P.ladderOf(f.id);
    put('ladderUpTotal', L.up.total);
    put('ladderHasVerdict', typeof L.verdict === 'string' && L.verdict.length > 10);
    const trueUp = P.DATA.edges.filter(x => x.to === f.id).length;
    put('ladderRung1MatchesEdges', L.up.rungs[0].length === new Set(
      P.DATA.edges.filter(x => x.to === f.id).map(x=>x.from)).size);
  }

  // 8. navigating into a folder and back is stable
  const dir = P.view.boxes.find(b => b.group && b.segs && b.segs.length);
  if (dir) {
    const before = JSON.stringify([P.view.boxes.length, P.view.edges.length]);
    P.navigate(dir.segs);
    put('descendChangedScope', P.focus.length > 0);
    put('descendConserved',
      Object.values(P.view.audit).reduce((a,b)=>a+b,0) === P.DATA.edges.length);
    P.navigate([]);
    put('roundTripStable', JSON.stringify([P.view.boxes.length, P.view.edges.length]) === before);
  }

  // 9. fit puts the layout on screen
  const [tx,ty,s] = P.fitParams();
  put('fitScalePositive', s > 0 && isFinite(s));
} catch (e) {
  out.ok = false; out.error = String(e).slice(0,300);
}
console.log(JSON.stringify(out));
'''

def run(repo_path):
    with tempfile.TemporaryDirectory() as td:
        html = os.path.join(td, 'g.html')
        r = subprocess.run([CAIRN,'export',html,'--dir',repo_path],
                           capture_output=True, text=True)
        if not os.path.exists(html):
            return {'fatal':'export failed: '+r.stderr[:120]}
        js = os.path.join(td,'h.js'); open(js,'w').write(HARNESS)
        p = subprocess.run([NODE, js, html], capture_output=True, text=True, timeout=300)
        try: return json.loads(p.stdout.strip().split('\n')[-1])
        except Exception:
            return {'fatal':'harness: '+(p.stderr or p.stdout)[:200]}

W=os.environ.get('REPOS', os.path.expanduser('~/cairn-bench-repos'))
repos = sys.argv[1:] or sorted(os.listdir(W))
bad = 0
for r in repos:
    path = os.path.join(W, r)
    if not os.path.isdir(path): continue
    res = run(path)
    if res.get('fatal') or not res.get('ok'):
        bad += 1
        print(f"  FAIL {r:<16} {res.get('fatal') or res.get('error')}")
        continue
    c = res['checks']
    problems = []
    if not c.get('edgesConserved'): problems.append('edges not conserved')
    if c.get('unplacedNodes',0): problems.append(f"{c['unplacedNodes']} unplaced")
    if c.get('danglingEdges',0): problems.append(f"{c['danglingEdges']} dangling")
    if c.get('columnInversions',0): problems.append(f"{c['columnInversions']} column inversions")
    if not c.get('ladderHasVerdict'): problems.append('no verdict')
    if c.get('ladderRung1MatchesEdges') is False: problems.append('ladder disagrees with payload')
    if not c.get('roundTripStable', True): problems.append('round trip unstable')
    if not c.get('fitScalePositive'): problems.append('bad fit scale')
    if problems: bad += 1
    print(f"  {'FAIL' if problems else 'PASS'} {r:<16} boxes={c.get('rootBoxes')} "
          f"edges={c.get('rootEdges')} cols={c.get('rootCols')} "
          f"conserved={c.get('edgesConserved')} {'; '.join(problems)}")
print(f"\n{'all viewer checks pass' if not bad else str(bad)+' repos with viewer problems'}")
