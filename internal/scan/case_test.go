package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
)

func TestCaseMismatchCreatesDuplicateNodes(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/page.tsx": `import { a } from "../lib/Utils";`, // capital U
		"lib/utils.ts": `export const a = 1;`,               // lowercase on disk
	}
	for p, b := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(b), 0o644)
	}

	res, err := RunWith(root, Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	var fileNodes []string
	for _, id := range res.Graph.IDs() {
		if res.Graph.Nodes[id].Kind == graph.File {
			fileNodes = append(fileNodes, id)
		}
	}
	t.Logf("file nodes: %v", fileNodes)
	t.Logf("unresolved: %d", len(res.Unresolved))
	if len(fileNodes) > 2 {
		t.Errorf("BUG: %d file nodes for 2 files — a case-mismatched import made a duplicate", len(fileNodes))
	}
}
