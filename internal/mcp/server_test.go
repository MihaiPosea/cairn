package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/scan"
)

func server(t *testing.T, files map[string]string) *Server {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scan.RunWith(root, scan.Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{root: root, res: res, mods: modules.Build(res)}
}

func fixture(t *testing.T) *Server {
	return server(t, map[string]string{
		"package.json": `{"name":"p","main":"src/index.ts"}`,
		"src/index.ts": `import "./core"; import "./util";`,
		"src/core.ts":  `import "./util"; export const c = 1;`,
		"src/util.ts":  `export const parseThing = 1;`,
		"other/off.ts": `const parseThing = 2; export default parseThing;`,
	})
}

func call(t *testing.T, s *Server, name string, args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := s.call(name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// A client that cannot discover the tools cannot use any of them.
func TestHandshakeAndToolDiscovery(t *testing.T) {
	s := fixture(t)
	res, err := s.dispatch(request{Method: "initialize", ID: json.RawMessage("1")})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion is %v", m["protocolVersion"])
	}
	if _, ok := m["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability must be advertised or no client will list them")
	}

	list, err := s.dispatch(request{Method: "tools/list", ID: json.RawMessage("2")})
	if err != nil {
		t.Fatal(err)
	}
	tools := list.(map[string]any)["tools"].([]any)
	seen := map[string]bool{}
	for _, x := range tools {
		tm := x.(map[string]any)
		name := tm["name"].(string)
		seen[name] = true
		// A tool with no schema or no description is one the model will
		// either call wrongly or never call at all.
		if tm["inputSchema"] == nil {
			t.Errorf("%s has no inputSchema", name)
		}
		if d, _ := tm["description"].(string); len(d) < 30 {
			t.Errorf("%s has a description too thin to choose it by: %q", name, d)
		}
	}
	for _, want := range []string{"ladder", "blast", "context", "scope", "search", "overview"} {
		if !seen[want] {
			t.Errorf("tool %q is not advertised", want)
		}
	}
}

// The replies exist to be read by a model under a context budget. The CLI
// rendering of one ladder is ~2,650 tokens because it lists every file on the
// far rung; that is the thing this layer exists to avoid.
func TestRepliesStaySmall(t *testing.T) {
	s := fixture(t)
	for _, c := range []struct {
		tool string
		args any
	}{
		{"ladder", map[string]string{"file": "src/util.ts"}},
		{"blast", map[string]string{"file": "src/util.ts"}},
		{"overview", map[string]string{}},
		{"context", map[string]string{"file": "src/core.ts"}},
	} {
		out := call(t, s, c.tool, c.args)
		if tokens := len(out) / 4; tokens > 900 {
			t.Errorf("%s replied with ~%d tokens; the point of this layer is that it does not", c.tool, tokens)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s replied with nothing", c.tool)
		}
	}
}

// The verdict is the sentence a model acts on, so it has to be there.
func TestLadderCarriesTheVerdict(t *testing.T) {
	s := fixture(t)
	out := call(t, s, "ladder", map[string]string{"file": "src/util.ts"})
	if !strings.Contains(out, "src/util.ts") {
		t.Error("the ladder should name the file")
	}
	low := strings.ToLower(out)
	verdicts := []string{"foundation", "load-bearing", "leaf", "ordinary",
		"entry point", "nothing imports"}
	found := false
	for _, v := range verdicts {
		if strings.Contains(low, v) {
			found = true
		}
	}
	if !found {
		t.Errorf("no verdict in the reply:\n%s", out)
	}
}

// Ordering is the entire value of search; an unconnected match must not
// outrank a connected one.
func TestSearchPutsConnectedMatchesFirst(t *testing.T) {
	s := fixture(t)
	out := call(t, s, "search", map[string]string{
		"pattern": "parseThing", "from": "src/util.ts",
	})
	iUtil := strings.Index(out, "src/util.ts")
	iOff := strings.Index(out, "other/off.ts")
	if iUtil < 0 || iOff < 0 {
		t.Fatalf("expected both files in the reply:\n%s", out)
	}
	if iOff < iUtil {
		t.Errorf("the unconnected match ranked above the connected one:\n%s", out)
	}
}

// A bad path must come back as something the model can act on, not as a
// protocol error the client swallows before the model sees it.
func TestAnUnknownFileIsReportedAsToolContent(t *testing.T) {
	s := fixture(t)
	args, _ := json.Marshal(map[string]any{
		"name": "ladder", "arguments": map[string]string{"file": "nope.ts"},
	})
	res, err := s.dispatch(request{Method: "tools/call", ID: json.RawMessage("1"), Params: args})
	if err != nil {
		t.Fatalf("a bad path should not be a protocol error: %v", err)
	}
	m := res.(map[string]any)
	if m["isError"] != true {
		t.Error("the reply should be flagged as an error")
	}
	text := m["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "nope.ts") {
		t.Errorf("the message should name the path it could not find: %q", text)
	}
}

func TestUnknownMethodAndToolAreRejected(t *testing.T) {
	s := fixture(t)
	if _, err := s.dispatch(request{Method: "nonsense", ID: json.RawMessage("1")}); err == nil {
		t.Error("an unknown method should be an error")
	}
	if _, err := s.call("nonsense", nil); err == nil {
		t.Error("an unknown tool should be an error")
	}
}

// The overview is what a model reads first in an unfamiliar repository, so it
// has to name the parts rather than only count them.
func TestOverviewNamesTheModules(t *testing.T) {
	s := fixture(t)
	out := call(t, s, "overview", map[string]string{})
	for _, want := range []string{"files", "modules"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview does not mention %q:\n%s", want, out)
		}
	}
	// The repository-root module's id is a bare "g:", which trimmed to nothing
	// and printed dependency edges with no left-hand side.
	if strings.Contains(out, "\n   → ") || strings.Contains(out, "  → ") &&
		strings.Contains(out, "\n  →") {
		t.Errorf("a module edge is missing one side:\n%s", out)
	}
}
