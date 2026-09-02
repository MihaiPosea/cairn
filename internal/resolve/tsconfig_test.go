package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	in := []byte(`{
  // a line comment with a "quote" and a } brace
  "a": 1, /* block
             comment */
  "b": "keeps // this and /* this */ inside the string",
  "c": [1, 2, 3,],
}`)
	var out map[string]any
	if err := json.Unmarshal(StripJSONC(in), &out); err != nil {
		t.Fatalf("stripped output is not valid JSON: %v\n%s", err, StripJSONC(in))
	}
	if out["b"] != "keeps // this and /* this */ inside the string" {
		t.Errorf("comment stripping damaged a string literal: %q", out["b"])
	}
	if len(out["c"].([]any)) != 3 {
		t.Errorf("trailing comma handling changed the array: %v", out["c"])
	}
}

func TestTSConfigExtendsChainChildWins(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./base/*"],"~/*":["./tilde/*"]}}}`)
	write("tsconfig.json", `{"extends":"./base.json","compilerOptions":{"paths":{"@/*":["./child/*"]}}}`)

	cfg, err := LoadTSConfig(root)
	if err != nil {
		t.Fatalf("LoadTSConfig: %v", err)
	}
	if got := cfg.Paths["@/*"]; len(got) != 1 || got[0] != "./child/*" {
		t.Errorf("child should override parent, got %v", got)
	}
	if got := cfg.Paths["~/*"]; len(got) != 1 || got[0] != "./tilde/*" {
		t.Errorf("inherited alias lost, got %v", got)
	}
}

func TestNoTSConfigIsNotAnError(t *testing.T) {
	cfg, err := LoadTSConfig(t.TempDir())
	if err != nil {
		t.Fatalf("a repo with no tsconfig is normal, got error: %v", err)
	}
	if len(cfg.Paths) != 0 {
		t.Errorf("expected no aliases, got %v", cfg.Paths)
	}
}
