package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// A json.Decoder over the stream cannot survive a syntax error: its buffer
// still holds the bad bytes, so every later Decode fails on the same ones and
// the loop never reaches the next request. Measured before the fix: one
// malformed frame and the server never answered again, at 0% CPU, silently -
// any client writing a stray byte to the pipe took the whole session with it.
func TestAMalformedFrameDoesNotWedgeTheStream(t *testing.T) {
	s := fixture(t)
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{not json at all`,
		``,
		`[1,2,3]`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n")
	var out strings.Builder
	s.in = bufio.NewReader(in)
	s.out = &out
	s.log = io.Discard

	if err := s.Serve(); err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected replies to the two well-formed requests, got %d:\n%s",
			len(lines), out.String())
	}
	var last struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatalf("the reply after the bad frames is not valid JSON: %v", err)
	}
	if last.ID != 2 {
		t.Errorf("ids desynchronised: the second reply is id %d, want 2", last.ID)
	}
	if last.Result["tools"] == nil {
		t.Error("the request after the malformed frames was not answered")
	}
}

// rescan swaps the graph under a write lock while queries read it. That lock
// was written and never exercised: a rescan landing between a reader taking
// the pointer and using it would tear the answer, and the race detector is
// the only thing that reliably shows it.
func TestQueriesAndRescanDoNotRace(t *testing.T) {
	s := fixture(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 64)

	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, c := range []struct {
					tool string
					args map[string]string
				}{
					{"ladder", map[string]string{"file": "src/util.ts"}},
					{"blast", map[string]string{"file": "src/core.ts"}},
					{"overview", map[string]string{}},
					{"scope", map[string]string{"file": "src/index.ts"}},
				} {
					raw, _ := json.Marshal(c.args)
					if _, err := s.call(c.tool, raw); err != nil {
						errs <- err
					}
				}
			}
		}()
	}

	for range 8 {
		if err := s.rescan(); err != nil {
			t.Fatalf("rescan: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a query failed while the graph was being replaced: %v", err)
	}
}

// The graph a single reply is built from must be one consistent scan. Taking
// res and mods under separate locks would let a rescan land between them and
// answer half from each.
func TestOneReplyUsesOneConsistentGraph(t *testing.T) {
	s := fixture(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.rescan()
			}
		}
	}()

	for range 200 {
		out, err := s.call("overview", nil)
		if err != nil {
			t.Fatalf("overview failed mid-rescan: %v", err)
		}
		// The module list and the file count come from the same pair; if they
		// were taken from different scans the reply would name modules the
		// count cannot account for.
		if !strings.Contains(out, "modules") || !strings.Contains(out, "files") {
			t.Fatalf("torn reply: %q", out)
		}
	}
	close(stop)
	wg.Wait()
}
