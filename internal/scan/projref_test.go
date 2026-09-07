package scan

import "testing"

// TypeScript project references: a composite monorepo where packages point at
// each other through tsconfig `references` rather than through paths.
func TestTypeScriptProjectReferences(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"package.json":  `{"name":"root","workspaces":["packages/*"]}`,
		"tsconfig.json": `{"files":[],"references":[{"path":"./packages/core"},{"path":"./packages/app"}]}`,

		"packages/core/package.json":  `{"name":"@acme/core","main":"./src/index.ts"}`,
		"packages/core/tsconfig.json": `{"compilerOptions":{"composite":true,"rootDir":"src","outDir":"dist"}}`,
		"packages/core/src/index.ts":  `export const core = 1;`,

		"packages/app/package.json": `{"name":"@acme/app","dependencies":{"@acme/core":"*"}}`,
		"packages/app/tsconfig.json": `{"compilerOptions":{"composite":true},
			"references":[{"path":"../core"}]}`,
		"packages/app/src/main.ts": `import { core } from "@acme/core";
export const app = core;`,
	}
	for p, body := range files {
		mustWrite(t, root, p, body)
	}

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range res.Unresolved {
		t.Errorf("unresolved %s:%d %q - %s", u.File, u.Line, u.Specifier, u.Reason)
	}
	if res.Graph.EdgeCount() == 0 {
		t.Error("a project-references monorepo produced no edges")
	}
	t.Logf("files=%d imports=%d edges=%d", res.FilesScanned, res.ImportsFound, res.Graph.EdgeCount())
}
