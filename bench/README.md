# Reproducing the numbers

Everything in the README was measured with these two scripts. They clone
nothing - point them at repositories you already have.

```
export REPOS=~/somewhere/with/cloned/repos     # each a git checkout
go build -o cairn ./cmd/cairn

python3 bench/direct.py                        # "who imports this file?"
python3 bench/headtohead.py vue-core excalidraw # "what breaks if I change it?"
```

`direct.py` compares one grep against one cairn call for the one-hop question.

`headtohead.py` is the honest one. It implements the transitive question the
way a careful agent would - grep the basename, read every hit, keep the ones
whose import actually resolves back, recurse on those - and counts every byte
it reads. Ground truth is cairn's own graph, which `cairn verify` separately
checks against the TypeScript compiler.

The token ratio is the least interesting result. What matters is that the grep
route stops on its own, having found a median of 14% of the answer across 50
repositories, and reports nothing to say it fell short. It cannot follow a
barrel re-export, a tsconfig path alias or a workspace import, because none of
those can be resolved by manipulating strings.
