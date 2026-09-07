# Viewer checks

The Go tests cover the payload. Nothing covered the page, and every viewer bug
in this project was found by looking at a screenshot: edges culled away from
the selection so it appeared connected to nothing, a click handler calling a
function that had been deleted, a six-pixel drag threshold that ate ordinary
trackpad clicks, a find-and-replace that corrupted the file.

`viewer_smoke.py` runs the page's own functions under node against a real
exported graph - no browser, no server - and asserts on what it computes:

- edge conservation: the five buckets sum to the edge total, so nothing is
  silently dropped
- every drawn node resolves to a box, and no edge points at a box that is not
  there
- columns are monotonic in depth, so left-to-right really is dependency order
- the ladder's first rung agrees with the payload's own edges
- descending into a folder and coming back leaves the same view
- fit produces a finite positive scale

```
REPOS=~/some/clones python3 test/viewer_smoke.py
REPOS=~/some/clones python3 test/viewer_smoke.py vue-core excalidraw
```
