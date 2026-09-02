// Ground truth for cairn's resolver: TypeScript's own module resolution.
//
// Reads {root, files} as JSON on stdin and writes, for every import specifier
// in every file, what TypeScript resolves it to. cairn's answers are then
// diffed against these.
//
// Using the real compiler rather than a second reimplementation is the point:
// two implementations by the same author share the same misunderstandings.
"use strict";

const fs = require("fs");
const path = require("path");

function readStdin() {
  return JSON.parse(fs.readFileSync(0, "utf8"));
}

function loadTypeScript(root) {
  for (const dir of [root, process.cwd()]) {
    try {
      return require(path.join(dir, "node_modules", "typescript"));
    } catch (_) {}
  }
  try {
    return require("typescript");
  } catch (_) {}
  return null;
}

function readTSConfig(ts, root) {
  const configPath =
    ts.findConfigFile(root, ts.sys.fileExists, "tsconfig.json") ||
    ts.findConfigFile(root, ts.sys.fileExists, "jsconfig.json");
  if (!configPath) return { options: {}, configPath: null };
  const read = ts.readConfigFile(configPath, ts.sys.readFile);
  const parsed = ts.parseJsonConfigFileContent(
    read.config || {},
    ts.sys,
    path.dirname(configPath)
  );
  return { options: parsed.options, configPath };
}

function main() {
  const input = readStdin();
  const root = input.root;

  const ts = loadTypeScript(root);
  if (!ts) {
    process.stdout.write(
      JSON.stringify({ available: false, reason: "typescript is not installed in this repo" })
    );
    return;
  }

  const { options, configPath } = readTSConfig(ts, root);
  const cache = ts.createModuleResolutionCache
    ? ts.createModuleResolutionCache(root, (x) => x, options)
    : undefined;

  const results = [];
  for (const rel of input.files) {
    const abs = path.join(root, rel);
    let text;
    try {
      text = fs.readFileSync(abs, "utf8");
    } catch (_) {
      continue;
    }

    const pre = ts.preProcessFile(text, true, true);
    const imports = [];
    for (const ref of pre.importedFiles) {
      const spec = ref.fileName;
      let resolved = null;
      try {
        const r = ts.resolveModuleName(spec, abs, options, ts.sys, cache);
        if (r && r.resolvedModule) resolved = r.resolvedModule.resolvedFileName;
      } catch (_) {}
      imports.push({ specifier: spec, resolved });
    }
    results.push({ file: rel, imports });
  }

  process.stdout.write(
    JSON.stringify({
      available: true,
      tsVersion: ts.version,
      configPath: configPath ? path.relative(root, configPath) : null,
      files: results,
    })
  );
}

main();
