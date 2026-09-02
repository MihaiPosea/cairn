package pkgs

import (
	"os"
	"path/filepath"
	"testing"
)

func lockRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
	}
	return root
}

// SUSPECT: a monorepo workspace dependency, "workspace:*".
func TestWorkspaceProtocol(t *testing.T) {
	root := lockRepo(t, map[string]string{
		"package.json": `{"name":"root","dependencies":{"@app/ui":"workspace:*","react":"^19.0.0"}}`,
		"pnpm-lock.yaml": `lockfileVersion: '9.0'
packages:
  react@19.0.0: {}
snapshots:
  react@19.0.0: {}
`,
	})
	g, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source=%s declared=%v packages=%v", g.Source, g.Declared, g.Names())
	if _, ok := g.Declared["@app/ui"]; !ok {
		t.Errorf("BUG: a workspace: dependency vanished from Declared")
	}
}

// SUSPECT: an aliased dependency, "foo": "npm:bar@^1.0.0".
func TestAliasedDependency(t *testing.T) {
	root := lockRepo(t, map[string]string{
		"package.json": `{"dependencies":{"lodash-es":"npm:lodash@^4.0.0"}}`,
		"yarn.lock": `"lodash-es@npm:lodash@^4.0.0":
  version "4.17.21"
`,
	})
	g, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source=%s packages=%v", g.Source, g.Names())
	for _, n := range g.Names() {
		if n == "" {
			t.Errorf("BUG: an aliased dependency produced an empty package name")
		}
	}
}

// SUSPECT: an npm v1 lockfile, which has no "packages" map.
func TestNPMv1Lockfile(t *testing.T) {
	root := lockRepo(t, map[string]string{
		"package.json":      `{"dependencies":{"react":"^18.0.0"}}`,
		"package-lock.json": `{"lockfileVersion":1,"dependencies":{"react":{"version":"18.3.1"}}}`,
		"node_modules/react/package.json": `{"name":"react","version":"18.3.1"}`,
	})
	g, err := Load(root)
	if err != nil {
		t.Fatalf("BUG: a v1 lockfile should degrade, not fail: %v", err)
	}
	t.Logf("source=%s packages=%v", g.Source, g.Names())
	if len(g.Packages) == 0 {
		t.Errorf("BUG: should have fallen back to node_modules, got nothing")
	}
}

// SUSPECT: a truncated or malformed lockfile.
func TestMalformedLockfiles(t *testing.T) {
	for name, body := range map[string]string{
		"package-lock.json": `{"lockfileVersion":3,"packages":{`,
		"bun.lock":          `{"packages": {"a": [`,
		"pnpm-lock.yaml":    "packages:\n  - [unbalanced\n",
		"yarn.lock":         "\x00\x01\x02 not a lockfile",
	} {
		root := lockRepo(t, map[string]string{
			"package.json": `{"dependencies":{"react":"^19.0.0"}}`,
			name:           body,
			"node_modules/react/package.json": `{"name":"react","version":"19.0.0"}`,
		})
		g, err := Load(root)
		if err != nil {
			t.Errorf("BUG: malformed %s returned an error instead of degrading: %v", name, err)
			continue
		}
		t.Logf("%-20s -> source=%q packages=%d", name, g.Source, len(g.Packages))
		if len(g.Packages) == 0 {
			t.Errorf("BUG: malformed %s should fall back to node_modules", name)
		}
	}
}

// SUSPECT: no package.json at all.
func TestNoPackageJSON(t *testing.T) {
	root := lockRepo(t, map[string]string{"src/a.ts": ""})
	g, err := Load(root)
	t.Logf("err=%v source=%q", err, g.Source)
	if g == nil {
		t.Fatal("BUG: Load returned a nil graph")
	}
}

// SUSPECT: scoped package names in every lockfile format.
func TestScopedNamesSurviveEveryFormat(t *testing.T) {
	cases := map[string]map[string]string{
		"npm": {
			"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/@scope/pkg":{"version":"1.0.0"}}}`,
		},
		"bun": {
			"bun.lock": `{"packages":{"@scope/pkg":["@scope/pkg@1.0.0","",{},"sha512-x"],}}`,
		},
		"pnpm": {
			"pnpm-lock.yaml": "packages:\n  '@scope/pkg@1.0.0': {}\n",
		},
		"yarn": {
			"yarn.lock": "\"@scope/pkg@^1.0.0\":\n  version \"1.0.0\"\n",
		},
	}
	for name, files := range cases {
		all := map[string]string{"package.json": `{"dependencies":{"@scope/pkg":"^1.0.0"}}`}
		for k, v := range files {
			all[k] = v
		}
		g, err := Load(lockRepo(t, all))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		t.Logf("%-5s -> source=%-22s packages=%v", name, g.Source, g.Names())
		found := false
		for _, n := range g.Names() {
			if n == "@scope/pkg" {
				found = true
			}
		}
		if !found {
			t.Errorf("BUG: %s lost the scoped name, got %v", name, g.Names())
		}
	}
}

// Regression: an aliased dependency must be keyed by the name code imports.
func TestAliasedDependencyKeyedByLocalName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"lodash-es@npm:lodash@^4.0.0", "lodash-es"},
		{"@scope/pkg@^1.0.0", "@scope/pkg"},
		{"@scope/pkg@npm:@other/pkg@^1.0.0", "@scope/pkg"},
		{"react@^18.0.0", "react"},
		{"react", "react"},
	} {
		if got := yarnDescriptorName(tc.in); got != tc.want {
			t.Errorf("yarnDescriptorName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitPnpmKeyHandlesScopesAndPeers(t *testing.T) {
	for _, tc := range []struct{ in, name, ver string }{
		{"/react@19.0.0", "react", "19.0.0"},
		{"@scope/pkg@1.2.3", "@scope/pkg", "1.2.3"},
		{"react@19.0.0(supports-color@8.1.1)", "react", "19.0.0"},
		{"/@babel/core@7.0.0(peer@1)", "@babel/core", "7.0.0"},
	} {
		name, ver := splitPnpmKey(tc.in)
		if name != tc.name || ver != tc.ver {
			t.Errorf("splitPnpmKey(%q) = %q,%q want %q,%q", tc.in, name, ver, tc.name, tc.ver)
		}
	}
}
