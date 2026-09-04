# Reproducing the numbers

Everything in the README was measured with these two scripts. They clone
nothing — point them at repositories you already have.

```
export REPOS=~/somewhere/with/cloned/repos     # each a git checkout
go build -o cairn ./cmd/cairn

python3 bench/direct.py                        # "who imports this file?"
python3 bench/headtohead.py vue-core excalidraw # "what breaks if I change it?"
```

`direct.py` compares one grep against one cairn call for the one-hop question.

`headtohead.py` is the honest one. It implements the transitive question the
way a careful agent would — grep the basename, read every hit, keep the ones
whose import actually resolves back, recurse on those — and counts every byte
it reads. Ground truth is cairn's own graph, which `cairn verify` separately
checks against the TypeScript compiler.

The result that matters is not the token ratio. It is that the grep route
terminates on its own, having found about 41% of the answer, with no way to
know it is missing anything. It cannot follow a barrel re-export, a tsconfig
path alias, or a workspace import, because resolving those is not string
manipulation — and implementing it properly is what cairn is.
