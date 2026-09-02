package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzResolve throws arbitrary specifiers at the resolver.
//
// The contract: never panic, and never return a file path that escapes the
// repository. The second half is the one worth guarding — a resolver that can
// be talked into pointing outside the root puts foreign paths into the graph,
// and every traversal downstream then treats them as project files.
func FuzzResolve(f *testing.F) {
	for _, s := range []string{
		"./a", "../a", "/abs", "@/lib/x", "react", "@scope/pkg", "node:fs", "fs",
		"", ".", "..", "./", "../../../../../../etc/passwd", "./a?raw", "./a#f",
		"@", "@/", "//", "./a/../../b", "\\\\server\\share", "C:\\x",
		strings.Repeat("../", 64) + "etc/passwd",
		strings.Repeat("a", 4096),
	} {
		f.Add(s)
	}

	root := f.TempDir()
	for _, p := range []string{"app/page.tsx", "lib/utils.ts", "components/index.ts"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(""), 0o644)
	}
	os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(`{"compilerOptions":{"paths":{"@/*":["./*"]}}}`), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(root), "outside.ts"), []byte(""), 0o644)

	r, err := New(root)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		got := r.Resolve("app/page.tsx", spec)

		if got.Kind == ToFile {
			if strings.HasPrefix(got.Path, "..") || filepath.IsAbs(got.Path) {
				t.Fatalf("resolved outside the repo: spec=%q path=%q", spec, got.Path)
			}
		}
		if got.Kind == Unresolved && got.Reason == "" {
			t.Fatalf("unresolved with no reason: spec=%q", spec)
		}
		if got.Kind == ToPackage && got.Package == "" {
			t.Fatalf("package result with an empty name: spec=%q", spec)
		}
	})
}

// FuzzStripJSONC checks the comment scanner against arbitrary input.
//
// The contract: whenever the input is already valid JSON, stripping must not
// change what it parses to. A scanner that mangles valid JSON would silently
// lose tsconfig aliases, and the only symptom would be a mysteriously high
// unresolved rate.
func FuzzStripJSONC(f *testing.F) {
	for _, s := range []string{
		`{"a":1}`,
		`{"a":"//not a comment"}`,
		`{"a":"/* nor this */"}`,
		`{ /* c */ "a": 1, }`,
		`{"a":"quote \" inside"}`,
		`{"a":[1,2,3,]}`,
		`{"a":"\\"}`,
		`//`,
		`/*`,
		`{"a":"trailing backslash \\"}`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		out := StripJSONC([]byte(src))

		var before any
		if json.Unmarshal([]byte(src), &before) != nil {
			return // input was not valid JSON; nothing is promised
		}
		var after any
		if err := json.Unmarshal(out, &after); err != nil {
			t.Fatalf("valid JSON became invalid after stripping\nin:  %q\nout: %q\nerr: %v", src, out, err)
		}
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if string(a) != string(b) {
			t.Fatalf("stripping changed the value\nin:  %q\nbefore: %s\nafter:  %s", src, a, b)
		}
	})
}
