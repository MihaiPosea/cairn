package scan

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// repoShape is one realistic project layout.
type repoShape struct {
	name  string
	files map[string]string
	// wantFiles is how many source files should be scanned. 0 means don't check.
	wantFiles int
}

func writeShape(t *testing.T, s repoShape) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range s.files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRepoShapes measures cairn against every common project layout.
//
// The claim "it works on any repo" is only worth making if it has been checked
// against the shapes people actually have. This is that check, and it is
// expected to fail loudly for shapes not yet supported rather than quietly
// producing a thin graph.
func TestRepoShapes(t *testing.T) {
	shapes := []repoShape{
		{
			name: "next-app-router",
			files: map[string]string{
				"package.json":  `{"dependencies":{"next":"15.0.0","react":"19.0.0"}}`,
				"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["./*"]}}}`,
				"app/page.tsx":  `import {B} from "@/components/Button"; import "./globals.css"; export default () => <B/>;`,
				"app/layout.tsx": `export default ({children}) => children;`,
				"app/globals.css": `body{}`,
				"components/Button.tsx": `import {u} from "@/lib/utils"; export const B = () => null;`,
				"lib/utils.ts":          `export const u = 1;`,
			},
			wantFiles: 4,
		},
		{
			name: "next-pages-router",
			files: map[string]string{
				"package.json":       `{"dependencies":{"next":"14.0.0"}}`,
				"tsconfig.json":      `{"compilerOptions":{"baseUrl":"src","paths":{"~/*":["./*"]}}}`,
				"src/pages/index.tsx": `import {H} from "~/components/Header"; export default () => <H/>;`,
				"src/pages/_app.tsx":  `export default ({Component}) => <Component/>;`,
				"src/components/Header.tsx": `export const H = () => null;`,
			},
			wantFiles: 3,
		},
		{
			name: "vite-react-spa",
			files: map[string]string{
				"package.json":   `{"dependencies":{"react":"19.0.0"},"devDependencies":{"vite":"6.0.0"}}`,
				"index.html":     `<script type="module" src="/src/main.tsx"></script>`,
				"vite.config.ts": `import {defineConfig} from "vite"; export default defineConfig({});`,
				"src/main.tsx":   `import App from "./App"; import "./index.css";`,
				"src/App.tsx":    `import logo from "./logo.svg?url"; export default () => null;`,
				"src/index.css":  `body{}`,
				"src/logo.svg":   `<svg/>`,
			},
			wantFiles: 3,
		},
		{
			name: "vite-alias-in-config-not-tsconfig",
			files: map[string]string{
				"package.json": `{"devDependencies":{"vite":"6.0.0"}}`,
				"vite.config.ts": `import {defineConfig} from "vite";
import path from "node:path";
export default defineConfig({resolve:{alias:{"@": path.resolve(__dirname, "./src")}}});`,
				"src/main.ts": `import {u} from "@/utils"; console.log(u);`,
				"src/utils.ts": `export const u = 1;`,
			},
			wantFiles: 3,
		},
		{
			name: "pnpm-monorepo",
			files: map[string]string{
				"package.json":        `{"name":"root","private":true,"workspaces":["packages/*"]}`,
				"pnpm-workspace.yaml": "packages:\n  - 'packages/*'\n",
				"packages/ui/package.json":   `{"name":"@acme/ui","main":"./src/index.ts"}`,
				"packages/ui/src/index.ts":   `export const Button = 1;`,
				"packages/app/package.json":  `{"name":"@acme/app","dependencies":{"@acme/ui":"workspace:*"}}`,
				"packages/app/tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["./src/*"]}}}`,
				"packages/app/src/main.ts":   `import {Button} from "@acme/ui"; import {h} from "@/helpers";`,
				"packages/app/src/helpers.ts": `export const h = 1;`,
			},
			wantFiles: 3,
		},
		{
			name: "vue-sfc",
			files: map[string]string{
				"package.json": `{"dependencies":{"vue":"3.5.0"}}`,
				"src/main.ts":  `import App from "./App.vue"; import {u} from "./utils";`,
				"src/App.vue": `<template><div/></template>
<script setup lang="ts">
import { Child } from "./Child.vue";
import { u } from "./utils";
</script>`,
				"src/Child.vue": `<template><span/></template>`,
				"src/utils.ts":  `export const u = 1;`,
			},
			wantFiles: 4,
		},
		{
			name: "svelte",
			files: map[string]string{
				"package.json":  `{"devDependencies":{"svelte":"5.0.0"}}`,
				"src/main.ts":   `import App from "./App.svelte";`,
				"src/App.svelte": `<script lang="ts">
  import Child from "./Child.svelte";
  import { u } from "./utils";
</script>
<div/>`,
				"src/Child.svelte": `<div/>`,
				"src/utils.ts":     `export const u = 1;`,
			},
			wantFiles: 4,
		},
		{
			name: "astro",
			files: map[string]string{
				"package.json": `{"dependencies":{"astro":"5.0.0"}}`,
				"src/pages/index.astro": `---
import Layout from "../layouts/Layout.astro";
import { u } from "../lib/utils";
---
<Layout/>`,
				"src/layouts/Layout.astro": `<slot/>`,
				"src/lib/utils.ts":         `export const u = 1;`,
			},
			wantFiles: 3,
		},
		{
			name: "node-commonjs-backend",
			files: map[string]string{
				"package.json":   `{"name":"api","main":"src/server.js","dependencies":{"express":"4.0.0"}}`,
				"src/server.js":  `const express = require("express"); const routes = require("./routes");`,
				"src/routes/index.js": `const db = require("../db"); module.exports = {};`,
				"src/db.js":      `module.exports = {};`,
			},
			wantFiles: 3,
		},
		{
			name: "library-src-dist",
			files: map[string]string{
				"package.json": `{"name":"lib","main":"./dist/index.js","module":"./dist/index.mjs","types":"./dist/index.d.ts","exports":{".":{"import":"./src/index.ts"}}}`,
				"src/index.ts":   `export * from "./client";`,
				"src/client.ts":  `import {h} from "./helpers"; export const c = h;`,
				"src/helpers.ts": `export const h = 1;`,
				"dist/index.js":  `"use strict";`,
			},
			wantFiles: 3,
		},
		{
			name: "react-native-platform-extensions",
			files: map[string]string{
				"package.json":    `{"main":"index.js","dependencies":{"react-native":"0.76.0"}}`,
				"index.js":        `import App from "./src/App";`,
				"src/App.tsx":     `import {Btn} from "./Button"; export default () => null;`,
				"src/Button.ios.tsx":     `export const Btn = 1;`,
				"src/Button.android.tsx": `export const Btn = 2;`,
			},
			wantFiles: 4,
		},
		{
			name: "nested-tsconfig-per-directory",
			files: map[string]string{
				"package.json":  `{"name":"root"}`,
				"tsconfig.json": `{"compilerOptions":{"paths":{"@root/*":["./*"]}}}`,
				"apps/web/tsconfig.json": `{"extends":"../../tsconfig.json","compilerOptions":{"paths":{"@web/*":["./src/*"]}}}`,
				"apps/web/src/index.ts":  `import {u} from "@web/utils"; export const x = u;`,
				"apps/web/src/utils.ts":  `export const u = 1;`,
			},
			wantFiles: 2,
		},
	}

	type result struct {
		name       string
		files      int
		imports    int
		unresolved int
		details    []string
	}
	var results []result

	for _, s := range shapes {
		s := s
		t.Run(s.name, func(t *testing.T) {
			root := writeShape(t, s)
			res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
			if err != nil {
				t.Fatalf("%s: scan failed: %v", s.name, err)
			}

			r := result{name: s.name, files: res.FilesScanned, imports: res.ImportsFound, unresolved: len(res.Unresolved)}
			for _, u := range res.Unresolved {
				r.details = append(r.details, u.File+":"+u.Specifier)
			}
			sort.Strings(r.details)
			results = append(results, r)

			t.Logf("files=%d imports=%d unresolved=%d", r.files, r.imports, r.unresolved)
			for _, d := range r.details {
				t.Logf("    UNRESOLVED %s", d)
			}
			if s.wantFiles > 0 && res.FilesScanned < s.wantFiles {
				t.Errorf("scanned %d files, expected at least %d — a whole file type is being skipped",
					res.FilesScanned, s.wantFiles)
			}
			if len(res.Unresolved) > 0 {
				t.Errorf("%d unresolved imports in a well-formed %s repo", len(res.Unresolved), s.name)
			}
		})
	}
}
