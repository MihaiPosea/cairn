// Package mcp serves the graph to a coding agent over stdio.
//
// Two things have to be true before an agent can use any of this, and running
// the CLI satisfies neither.
//
// It has to be fast enough to ask repeatedly. Every CLI invocation rebuilds the
// graph - 0.09s on a small repository and 1.34s on nx, which is fine once and
// useless ten times in a row while an agent is working something out. The graph
// is scanned once here and held, so every question after the first is answered
// from memory.
//
// And it has to be cheap enough to read. The ladder for one file, rendered as
// the CLI renders it, is about 2,650 tokens because it lists all 166 files on
// the far rung. An agent wants the shape and the verdict, not the census; the
// counts are what carry the meaning and the paths are recoverable on request.
// The replies here are deliberately small.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/MihaiPosea/cairn/internal/agent"
	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

const protocolVersion = "2024-11-05"

// Server holds one scan of one repository and answers questions about it.
type Server struct {
	root string
	mu   sync.RWMutex
	res  *scan.Result
	mods *modules.Map
	in   *bufio.Reader
	out  io.Writer
	log  io.Writer
}

// New scans the repository and returns a server ready to answer.
func New(root string) (*Server, error) {
	s := &Server{
		root: root,
		in:   bufio.NewReaderSize(os.Stdin, 1<<20),
		out:  os.Stdout,
		// Anything written to stdout is protocol, so progress and errors go to
		// stderr. A stray print on stdout desynchronises the client and the
		// failure looks like the server hanging.
		log: os.Stderr,
	}
	if err := s.rescan(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) rescan() error {
	res, err := scan.Run(s.root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res, s.mods = res, modules.Build(res)
	return nil
}

// ── protocol ────────────────────────────────────────────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests until stdin closes.
func (s *Server) Serve() error {
	fmt.Fprintf(s.log, "cairn: %d files, %d imports - ready\n",
		s.res.FilesScanned, s.res.ImportsFound)

	// Line-delimited frames, read and parsed one at a time.
	//
	// A json.Decoder over the stream cannot survive a syntax error. Its buffer
	// still holds the bad bytes, so every later Decode fails on the same ones
	// and the loop can never reach the next request - measured: one malformed
	// frame and the server never answered again, at 0% CPU, silently. Any
	// client that writes a stray byte to the pipe would take the whole agent
	// session with it.
	//
	// Reading a line at a time makes the damage exactly one request wide.
	for {
		line, err := s.in.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			fmt.Fprintf(s.log, "cairn: skipping unparseable frame: %v\n", err)
			continue
		}
		// A notification has no id and takes no reply.
		if len(req.ID) == 0 {
			continue
		}
		result, err := s.dispatch(req)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if err != nil {
			resp.Error = &rpcError{Code: -32603, Message: err.Error()}
		} else {
			resp.Result = result
		}
		if err := json.NewEncoder(s.out).Encode(resp); err != nil {
			return err
		}
	}
}

func (s *Server) dispatch(req request) (any, error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "cairn", "version": "1"},
		}, nil

	case "tools/list":
		return map[string]any{"tools": toolList()}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		text, err := s.call(p.Name, p.Arguments)
		if err != nil {
			// Reported as tool content rather than a protocol error: the model
			// can act on "that path is not in the graph", and a JSON-RPC error
			// is usually swallowed by the client before it sees it.
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
				"isError": true,
			}, nil
		}
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		}, nil
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

// ── tools ───────────────────────────────────────────────────────────────────

func toolList() []any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	file := map[string]any{
		"type":       "object",
		"properties": map[string]any{"file": str("repo-relative path")},
		"required":   []string{"file"},
	}
	return []any{
		map[string]any{
			"name": "ladder",
			"description": "Where a file sits: what needs it (two levels up) and what it " +
				"stands on (two levels down), with a verdict on how much care a change " +
				"deserves. Answers 'is this file safe to touch'. Editors only show one " +
				"level, and only downward.",
			"inputSchema": file,
		},
		map[string]any{
			"name": "blast",
			"description": "Every file that transitively depends on this one - what breaks " +
				"if you change it.",
			"inputSchema": file,
		},
		map[string]any{
			"name": "context",
			"description": "The minimal ranked set of files to read before changing this " +
				"one, with a reason for each. Use before editing an unfamiliar file.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file":   str("repo-relative path"),
					"budget": map[string]any{"type": "integer", "description": "token budget"},
				},
				"required": []string{"file"},
			},
		},
		map[string]any{
			"name": "scope",
			"description": "The only files that could be involved with this one - the set a " +
				"search can be restricted to instead of searching the whole repository.",
			"inputSchema": file,
		},
		map[string]any{
			"name": "search",
			"description": "Search the repository, ordered by what is connected to an anchor " +
				"file rather than by path. Matches in files that import the anchor rank " +
				"above matches that merely share a word.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": str("regular expression"),
					"from":    str("anchor file - results are ordered by distance from it"),
				},
				"required": []string{"pattern"},
			},
		},
		map[string]any{
			"name": "overview",
			"description": "The shape of the repository: its modules, how many files each " +
				"holds, what depends on what, and any import cycles. Read this first in an " +
				"unfamiliar codebase.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		map[string]any{
			"name":        "rescan",
			"description": "Rebuild the graph after files have changed on disk.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func (s *Server) call(name string, raw json.RawMessage) (string, error) {
	var a struct {
		File    string `json:"file"`
		Pattern string `json:"pattern"`
		From    string `json:"from"`
		Budget  int    `json:"budget"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}

	s.mu.RLock()
	res, mods := s.res, s.mods
	s.mu.RUnlock()

	switch name {
	case "ladder":
		l, err := agent.BuildLadder(res, mods, a.File)
		if err != nil {
			return "", err
		}
		return renderLadder(l), nil

	case "blast":
		id := graph.NodeID(graph.File, a.File)
		if res.Graph.Nodes[id] == nil {
			return "", &agent.NotFound{Path: a.File}
		}
		b := query.BlastRadius(res.Graph, id)
		var direct []string
		for _, d := range b.Direct {
			direct = append(direct, strings.TrimPrefix(d, "file:"))
		}
		return fmt.Sprintf("%s\n%d files depend on this transitively, %d directly.\n\n%s",
			a.File, len(b.Affected)-1, len(direct), sample(direct, 15)), nil

	case "context":
		c, err := agent.Build(res, mods, a.File, agent.Options{Budget: a.Budget})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s - %d files depend on it\nread these %d files (~%d tokens):\n",
			c.Target, c.Blast, len(c.Read), c.Tokens)
		for _, f := range c.Read {
			fmt.Fprintf(&b, "  %s - %s\n", f.Path, f.Why)
		}
		if c.Omitted > 0 {
			fmt.Fprintf(&b, "%d more related files did not fit the budget.\n", c.Omitted)
		}
		return b.String(), nil

	case "scope":
		c, err := agent.Build(res, mods, a.File, agent.Options{})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d of %d files could be involved with %s.\nRestrict any search "+
			"to these:\n\n%s", len(c.Scope), countFiles(res), a.File,
			strings.Join(c.Scope, "\n")), nil

	case "search":
		r, err := agent.Grep(res, a.Pattern, agent.SearchOptions{Anchor: a.From})
		if err != nil {
			return "", err
		}
		return renderSearch(r), nil

	case "overview":
		return renderOverview(res, mods), nil

	case "rescan":
		if err := s.rescan(); err != nil {
			return "", err
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return fmt.Sprintf("rescanned: %d files, %d imports",
			s.res.FilesScanned, s.res.ImportsFound), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// ── rendering ───────────────────────────────────────────────────────────────
//
// Compact on purpose. The counts carry the meaning; the full lists are large
// and recoverable by asking again for the thing that looked interesting.

func renderLadder(l *agent.Ladder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", l.File)
	fmt.Fprintf(&b, "  %4d  two levels up - need the things that need this\n", l.Up[1].Count)
	fmt.Fprintf(&b, "  %4d  one level up - need this directly\n", l.Up[0].Count)
	for _, f := range firstN(l.Up[0].Files, 5) {
		fmt.Fprintf(&b, "          %s\n", f)
	}
	fmt.Fprintf(&b, "     ●  %s\n", l.File)
	fmt.Fprintf(&b, "  %4d  one level down - what it stands on\n", l.Down[0].Count)
	for _, f := range firstN(l.Down[0].Files, 5) {
		fmt.Fprintf(&b, "          %s\n", f)
	}
	fmt.Fprintf(&b, "  %4d  two levels down\n\n", l.Down[1].Count)
	fmt.Fprintf(&b, "%s\n%d files above it, %d below", l.Verdict, l.Reach, l.Depends)
	if l.Share >= 0.02 {
		fmt.Fprintf(&b, " - %.0f%% of the repository", l.Share*100)
	}
	return b.String() + "."
}

func renderSearch(r *agent.SearchResult) string {
	var b strings.Builder
	if r.Anchor == "" {
		fmt.Fprintf(&b, "%d matches. Pass `from` to order them by what is connected to a file.\n\n",
			len(r.Hits))
	} else {
		fmt.Fprintf(&b, "%d connected to %s, %d unrelated. Connected first.\n\n",
			r.Connected, r.Anchor, r.Unrelated)
	}
	shown := 0
	for _, h := range r.Hits {
		if shown >= 40 {
			fmt.Fprintf(&b, "… %d more\n", len(r.Hits)-shown)
			break
		}
		tag := ""
		switch {
		case h.Hops == 0:
			tag = "  [the file itself]"
		case h.Hops > 0 && h.Direction == "upstream":
			tag = fmt.Sprintf("  [%d up - breaks if you change it]", h.Hops)
		case h.Hops > 0:
			tag = fmt.Sprintf("  [%d down]", h.Hops)
		case h.Hops < 0:
			tag = "  [not connected]"
		}
		fmt.Fprintf(&b, "%s:%d%s\n    %s\n", h.Path, h.Line, tag, h.Text)
		shown++
	}
	return b.String()
}

func renderOverview(res *scan.Result, mods *modules.Map) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d files, %d imports, %.2f%% unresolved.\n\n",
		res.Root, res.FilesScanned, res.ImportsFound, res.UnresolvedRate()*100)

	fmt.Fprintf(&b, "%d modules:\n", len(mods.Modules))
	for i, m := range mods.Modules {
		if i >= 12 {
			fmt.Fprintf(&b, "  … and %d more\n", len(mods.Modules)-12)
			break
		}
		line := fmt.Sprintf("  %-28s %4d files", m.Name, m.Files)
		if m.Desc != "" {
			line += "  " + m.Desc
		}
		fmt.Fprintln(&b, line)
	}

	// By name, not by trimmed id: the repository-root module's id is bare
	// "g:", which trims to nothing and printed an edge with no left-hand side.
	name := map[string]string{}
	for _, m := range mods.Modules {
		name[m.ID] = m.Name
	}
	label := func(id string) string {
		if n, ok := name[id]; ok && n != "" {
			return n
		}
		return strings.TrimPrefix(id, "g:")
	}

	if len(mods.Edges) > 0 {
		fmt.Fprintf(&b, "\ndependencies between them:\n")
		for i, e := range mods.Edges {
			if i >= 15 {
				fmt.Fprintf(&b, "  … and %d more\n", len(mods.Edges)-15)
				break
			}
			fmt.Fprintf(&b, "  %s → %s (%d imports)\n",
				label(e.From), label(e.To), e.Count)
		}
	}
	if n := len(query.Cycles(res.Graph, query.AllEdges)); n > 0 {
		fmt.Fprintf(&b, "\n%d import cycles. Ask for `cycles` detail if it matters.\n", n)
	}
	return b.String()
}

func countFiles(res *scan.Result) int {
	n := 0
	for _, x := range res.Graph.Nodes {
		if x.Kind == graph.File {
			n++
		}
	}
	return n
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func sample(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, "\n")
	}
	return strings.Join(xs[:n], "\n") + fmt.Sprintf("\n… and %d more", len(xs)-n)
}
