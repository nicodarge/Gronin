// Usage: TOOLCHAIN_PREFIX=<install prefix> node --test .github/release/check-toolchain.test.mjs
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { after, describe, test } from "node:test";
import { fileURLToPath } from "node:url";

const prefix = resolve(process.env.TOOLCHAIN_PREFIX ?? "");
const script = process.env.CHECK_TOOLCHAIN_SCRIPT ?? join(dirname(fileURLToPath(import.meta.url)), "check-toolchain.mjs");
assert.ok(process.env.TOOLCHAIN_PREFIX, "set TOOLCHAIN_PREFIX to the directory the toolchain was installed into");

const work = mkdtempSync(join(tmpdir(), "check-toolchain-test-"));
after(() => rmSync(work, { recursive: true, force: true }));

let counter = 0;
function freshDir(label) {
  const dir = join(work, `${label}-${counter++}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

// A fake npm that records its arguments, one per line, in argv.log next to it.
function binDir(npmExit) {
  const dir = freshDir("bin");
  if (npmExit !== undefined) {
    writeFileSync(join(dir, "npm"), `#!/bin/sh\nprintf '%s\\n' "$@" > "${join(dir, "argv.log")}"\nexit ${npmExit}\n`);
    chmodSync(join(dir, "npm"), 0o755);
  }
  return dir;
}

const STEPS = "{ verifyConditions: async () => {}, analyzeCommits: async () => {} }";
const cjs = (body) => `module.exports = ${body};\n`;

// A repository directory with a .releaserc and fake packages: { name: { "package.json": {...}, "index.js": "..." } }.
function repo(config, packages = {}) {
  const dir = freshDir("repo");
  writeFileSync(join(dir, ".releaserc"), typeof config === "string" ? config : JSON.stringify(config));
  for (const [name, files] of Object.entries(packages)) {
    const pkgDir = join(dir, "node_modules", name);
    mkdirSync(pkgDir, { recursive: true });
    writeFileSync(join(pkgDir, "package.json"), JSON.stringify({ name, version: "1.0.0", main: "index.js", ...files["package.json"] }));
    for (const [file, content] of Object.entries(files)) {
      if (file !== "package.json") {
        writeFileSync(join(pkgDir, file), content);
      }
    }
  }
  return dir;
}

const plugin = (body) => ({ "index.js": body });

function check(dir, { npm = 0, limitMs = 60_000, toolchain = prefix } = {}) {
  const bin = binDir(npm === "missing" ? undefined : npm);
  const run = spawnSync(process.execPath, [script, toolchain, dir], {
    cwd: dir,
    encoding: "utf8",
    env: { PATH: `${bin}:/usr/bin:/bin`, HOME: work, CHECK_TOOLCHAIN_LIMIT_MS: String(limitMs) },
    timeout: 120_000,
  });
  return { status: run.status, stdout: run.stdout, lines: run.stderr.split("\n").filter((line) => line !== ""), bin };
}

function passes(result) {
  assert.deepEqual(result.lines, []);
  assert.equal(result.status, 0);
}

// The check fails with exactly one line per pattern, in order.
function fails(result, ...patterns) {
  assert.equal(result.status, 1, result.lines.join("\n"));
  assert.equal(result.lines.length, patterns.length, `expected ${patterns.length} line(s), got:\n${result.lines.join("\n")}`);
  patterns.forEach((pattern, index) => assert.match(result.lines[index], pattern));
}

describe("what the tool accepts", () => {
  test("a plugin that provides two steps", () => {
    const result = check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) }));
    passes(result);
    assert.match(result.stdout, /Loaded plugin "analyzeCommits" from "ct-test-ok"/);
  });

  test("the default analyzer is loaded when no plugin provides the step", () => {
    const result = check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs("{ verifyConditions: async () => {} }")) }));
    passes(result);
    assert.match(result.stdout, /from "@semantic-release\/commit-analyzer"/);
  });

  test("a YAML configuration, a single string, a path object, a pair and a step key", () => {
    const packages = { "ct-test-ok": plugin(cjs(STEPS)), "ct-test-more": plugin(cjs("{ prepare: async () => {} }")) };
    passes(check(repo("plugins:\n  - ct-test-ok\n", packages)));
    passes(check(repo({ plugins: "ct-test-ok" }, packages)));
    passes(check(repo({ plugins: { path: "ct-test-ok" } }, packages)));
    passes(check(repo({ plugins: [["ct-test-ok", { a: 1 }], ["ct-test-more", null]] }, packages)));
    passes(check(repo({ plugins: ["ct-test-ok"], prepare: ["ct-test-more"] }, packages)));
  });

  test("a package that offers a require entry next to its import entry", () => {
    const both = { "package.json": { exports: { import: "./i.mjs", require: "./i.cjs" } }, "i.mjs": `export default ${STEPS};\n`, "i.cjs": cjs(STEPS) };
    passes(check(repo({ plugins: ["ct-test-both"] }, { "ct-test-both": both })));
  });

  test("stdout written while loading is passed on", () => {
    const result = check(repo({ plugins: ["ct-test-talk"] }, { "ct-test-talk": plugin(`console.log("hello from a plugin");\nmodule.exports = ${STEPS};\n`) }));
    passes(result);
    assert.match(result.stdout, /hello from a plugin/);
  });
});

describe("what the tool rejects", () => {
  test("a plugin that does not exist", () => {
    fails(check(repo({ plugins: ["ct-test-ok", "missing-plugin"] }, { "ct-test-ok": plugin(cjs(STEPS)) })), /Cannot find module 'missing-plugin'/);
  });

  test("a package that only offers an import condition", () => {
    const importOnly = { "package.json": { main: undefined, exports: { import: "./i.mjs" } }, "i.mjs": `export const verifyConditions = async () => {};\n` };
    fails(check(repo({ plugins: ["ct-test-import-only"] }, { "ct-test-import-only": importOnly })), /import-only/);
  });

  test("a plugin whose export is not a plain object", () => {
    fails(check(repo({ plugins: ["ct-test-fn"] }, { "ct-test-fn": plugin(cjs("async () => {}")) })), /EPLUGINSCONF/);
  });

  test("entries the tool does not accept", () => {
    for (const entry of [5, null, [], ["ct-test-ok", "options"], ["ct-test-ok", {}, {}]]) {
      fails(check(repo({ plugins: [entry] }, { "ct-test-ok": plugin(cjs(STEPS)) })), /EPLUGINSCONF/);
    }
  });

  test("a step key naming a missing plugin, and one naming a plugin without the step", () => {
    const packages = { "ct-test-other": plugin(cjs("{ prepare: async () => {} }")) };
    fails(check(repo({ plugins: ["ct-test-other"], verifyConditions: ["missing-plugin"] }, packages)), /Cannot find module 'missing-plugin'/);
    fails(check(repo({ plugins: ["ct-test-other"], verifyConditions: ["ct-test-other"] }, packages)), /EPLUGIN .*ct-test-other/);
  });

  test("an inline object that names a step", () => {
    fails(check(repo({ plugins: [{ verifyConditions: "x" }] })), /./);
  });

  test("extends: a configuration that does not exist and one naming a missing plugin", () => {
    fails(check(repo({ extends: "no-such-config", plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) })), /Cannot find module 'no-such-config'/);
    const shared = plugin(cjs('{ "verifyConditions": ["missing-plugin"] }'));
    fails(check(repo({ extends: "ct-test-shared", plugins: ["ct-test-ok"] }, { "ct-test-shared": shared, "ct-test-ok": plugin(cjs(STEPS)) })), /Cannot find module 'missing-plugin'/);
  });

  test("a plugin name that looks like an option reaches no node option", () => {
    const result = check(repo({ plugins: ["--version"] }));
    fails(result, /Cannot find module '--version'/);
    assert.doesNotMatch(result.stdout, /^v\d+\.\d+/m);
  });
});

describe("what only this check rejects", () => {
  test("a plugin that exits while loading", () => {
    fails(check(repo({ plugins: ["ct-test-quit"] }, { "ct-test-quit": plugin("process.exit(0);\n") })), /ended before the configuration had loaded/);
  });

  test("a plugin whose import never settles", () => {
    fails(check(repo({ plugins: ["ct-test-stuck"] }, { "ct-test-stuck": plugin("await new Promise(() => {});\n") })), /unsettled top-level await/);
  });

  test("a plugin that writes to stderr, with or without a final newline", () => {
    for (const text of ["noise\\n", "noise"]) {
      const result = check(repo({ plugins: ["ct-test-noisy"] }, { "ct-test-noisy": plugin(`process.stderr.write("${text}");\nmodule.exports = ${STEPS};\n`) }));
      fails(result, /^noise$/, /wrote to stderr while loading/);
    }
  });

  test("a plugin that emits a warning, even from a later tick", () => {
    fails(check(repo({ plugins: ["ct-test-warn"] }, { "ct-test-warn": plugin(`process.nextTick(() => process.emitWarning("late"));\nmodule.exports = ${STEPS};\n`) })), /Warning: late/, /\(Use /, /wrote to stderr while loading/);
  });

  test("work that fails after the import", () => {
    const later = (code) => plugin(`${code}\nmodule.exports = ${STEPS};\n`);
    fails(check(repo({ plugins: ["ct-test-late"] }, { "ct-test-late": later('setTimeout(() => { throw new Error("late throw"); }, 600);') })), /after loading: .*late throw/);
    fails(check(repo({ plugins: ["ct-test-late"] }, { "ct-test-late": later('setTimeout(() => Promise.reject(new Error("late reject")), 600);') })), /after loading: .*late reject/);
  });

  test("a plugin that keeps the process alive", () => {
    const keeper = plugin(`setInterval(() => {}, 1000);\nmodule.exports = ${STEPS};\n`);
    fails(check(repo({ plugins: ["ct-test-keeper"] }, { "ct-test-keeper": keeper }), { limitMs: 8000 }), /did not end within \d+ s after loading .*keeps a handle open/);
  });

  test("a load that does not finish within the limit names what it last reported", () => {
    const hang = plugin("setInterval(() => {}, 1000);\nawait new Promise(() => {});\n");
    fails(check(repo({ plugins: ["ct-test-ok", "ct-test-hang"] }, { "ct-test-ok": plugin(cjs(STEPS)), "ct-test-hang": hang }), { limitMs: 8000 }), /loading did not finish within \d+ s .*last report: /);
  });

  test("a plugin that exits on the next tick of the loop", () => {
    const quick = plugin(`setImmediate(() => process.exit(0));\nmodule.exports = ${STEPS};\n`);
    fails(check(repo({ plugins: ["ct-test-quick"] }, { "ct-test-quick": quick })), /ended before the configuration had loaded/);
  });

  test("several errors are reported together, with what names the offender", () => {
    fails(check(repo({ plugins: [5, null] })), /EPLUGINSCONF .*`5`/, /EPLUGINSCONF .*`null`/);
  });

  test("errors already reported are kept when the process then does not end", () => {
    const keeper = plugin(`setInterval(() => {}, 1000);\nmodule.exports = ${STEPS};\n`);
    fails(
      check(repo({ plugins: ["ct-test-keeper", 5, null] }, { "ct-test-keeper": keeper }), { limitMs: 8000 }),
      /EPLUGINSCONF .*`5`/,
      /EPLUGINSCONF .*`null`/,
      /did not end within \d+ s after reporting an error .*keeps a handle open/
    );
  });

  test("the audit runs after a failed load and its failure is reported too", () => {
    const dir = repo({ plugins: ["missing-plugin"] });
    fails(check(dir, { npm: 1 }), /Cannot find module 'missing-plugin'/, /npm audit failed in .*: exit status 1/);
    const failing = check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) }), { npm: 1 });
    fails(failing, /npm audit failed in .*: exit status 1/);
    assert.deepEqual(readFileSync(join(failing.bin, "argv.log"), "utf8").split("\n").filter(Boolean), ["audit", "--audit-level=low", "--no-update-notifier", "--prefix", prefix]);
    fails(check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) }), { npm: "missing" }), /npm audit failed in .*ENOENT/);
  });
});

// A copy of the installed tool whose files can be broken; every other package is linked.
function brokenPrefix(edit) {
  const dir = freshDir("prefix");
  const modules = join(dir, "node_modules");
  mkdirSync(modules);
  for (const name of readdirSync(join(prefix, "node_modules"))) {
    if (name !== "semantic-release") {
      symlinkSync(join(prefix, "node_modules", name), join(modules, name));
    }
  }
  cpSync(join(prefix, "node_modules", "semantic-release"), join(modules, "semantic-release"), { recursive: true });
  edit(join(modules, "semantic-release", "lib"));
  return dir;
}

function replaceIn(file, from, to) {
  const text = readFileSync(file, "utf8");
  assert.ok(text.includes(from), `${file} no longer contains the text this test breaks`);
  writeFileSync(file, text.replace(from, to));
}

describe("the canaries catch a tool whose internals moved", () => {
  const ok = { "ct-test-ok": plugin(cjs(STEPS)) };

  test("a loader that no longer imports plugins", () => {
    const broken = brokenPrefix((lib) => replaceIn(join(lib, "plugins", "utils.js"), "await import(`file://${file}`)", "{}"));
    const result = check(repo({ plugins: ["ct-test-ok"] }, ok), { toolchain: broken });
    assert.equal(result.status, 1);
    assert.ok(result.lines.some((line) => /canary "a present plugin loads" failed: semantic-release's internals changed/.test(line)), result.lines.join("\n"));
  });

  test("definitions that no longer list any step", () => {
    const broken = brokenPrefix((lib) => replaceIn(join(lib, "definitions", "plugins.js"), "export default {", "export default {};\nconst unused = {"));
    const result = check(repo({ plugins: ["ct-test-ok"] }, ok), { toolchain: broken });
    assert.equal(result.status, 1);
    assert.ok(result.lines.some((line) => /canary "a present plugin loads" failed/.test(line)), result.lines.join("\n"));
  });

  test("a step-key loader that no longer loads plugins", () => {
    const broken = brokenPrefix((lib) => replaceIn(join(lib, "plugins", "normalize.js"), "const plugin = await loadPlugin(context, name, pluginsPath);", "const plugin = { [type]: async () => {} };"));
    const result = check(repo({ plugins: ["ct-test-ok"], prepare: ["ct-test-ok"] }, ok), { toolchain: broken });
    assert.equal(result.status, 1);
    assert.ok(result.lines.some((line) => /canary "a missing step plugin fails" failed: semantic-release's internals changed/.test(line)), result.lines.join("\n"));
  });

  test("a configuration loader that is gone", () => {
    const broken = brokenPrefix((lib) => rmSync(join(lib, "get-config.js")));
    const result = check(repo({ plugins: ["ct-test-ok"] }, ok), { toolchain: broken });
    assert.equal(result.status, 1);
    assert.match(result.lines.at(-1), /configuration loader cannot be used/);
  });

  test("a loader whose arguments changed", () => {
    const broken = brokenPrefix((lib) => replaceIn(join(lib, "plugins", "utils.js"), "export async function loadPlugin({ cwd }, name, pluginsPath) {", "export async function loadPlugin(name, { cwd }, pluginsPath) {"));
    const result = check(repo({ plugins: ["ct-test-ok"] }, ok), { toolchain: broken });
    assert.equal(result.status, 1);
    assert.ok(result.lines.some((line) => /canary/.test(line)), result.lines.join("\n"));
  });
});
