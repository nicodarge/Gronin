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

// A fake npm that records its arguments, one per line, in argv.log next to it, prints the given text and exits with the given status.
function binDir(npmExit, output = "") {
  const dir = freshDir("bin");
  if (npmExit !== undefined) {
    writeFileSync(join(dir, "report.json"), output);
    writeFileSync(join(dir, "npm"), `#!/bin/sh\nprintf '%s\\n' "$@" > "${join(dir, "argv.log")}"\ncat "${join(dir, "report.json")}"\nexit ${npmExit}\n`);
    chmodSync(join(dir, "npm"), 0o755);
  }
  return dir;
}

// What npm audit --json prints; a vulnerability is { via: [...] }, an advisory { name, severity, title, url }.
const report = (vulnerabilities = {}) => JSON.stringify({ auditReportVersion: 2, vulnerabilities, metadata: { vulnerabilities: { total: Object.keys(vulnerabilities).length } } });
const advisory = (id, name = "braces") => ({ source: 1, name, severity: "high", title: `Problem in ${name}`, url: `https://github.com/advisories/${id}`, range: "<=1.0.0" });
const ID = "GHSA-vfj7-8cjw-p6xm";
const OTHER = "GHSA-2222-3333-4444";

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

function check(dir, { npm = 0, audit = report(), today, env = {}, limitMs = 60_000, toolchain = prefix } = {}) {
  const bin = binDir(npm === "missing" ? undefined : npm, typeof audit === "string" ? audit : JSON.stringify(audit));
  const run = spawnSync(process.execPath, [script, toolchain, dir], {
    cwd: dir,
    encoding: "utf8",
    env: { PATH: `${bin}:/usr/bin:/bin`, HOME: work, CHECK_TOOLCHAIN_LIMIT_MS: String(limitMs), ...(today === undefined ? {} : { CHECK_TOOLCHAIN_TODAY: today }), ...env },
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
    fails(check(dir, { npm: 1, audit: "" }), /Cannot find module 'missing-plugin'/, /npm audit failed in .*: exit status 1, and its output is not JSON/);
    const failing = check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) }), { npm: 1, audit: "" });
    fails(failing, /npm audit failed in .*: exit status 1, and its output is not JSON/);
    assert.deepEqual(readFileSync(join(failing.bin, "argv.log"), "utf8").split("\n").filter(Boolean), ["audit", "--json", "--no-update-notifier", "--prefix", prefix]);
    fails(check(repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) }), { npm: "missing" }), /npm audit failed in .*ENOENT/);
  });

  test("an audit that does not answer within the limit is killed and fails", () => {
    const dir = repo({ plugins: ["ct-test-ok"] }, { "ct-test-ok": plugin(cjs(STEPS)) });
    const bin = freshDir("bin");
    writeFileSync(join(bin, "npm"), "#!/bin/sh\nexec sleep 30\n");
    chmodSync(join(bin, "npm"), 0o755);
    const run = spawnSync(process.execPath, [script, prefix, dir], { cwd: dir, encoding: "utf8", env: { PATH: `${bin}:/usr/bin:/bin`, HOME: work, CHECK_TOOLCHAIN_AUDIT_LIMIT_MS: "1500" }, timeout: 20_000 });
    assert.equal(run.status, 1, run.stderr);
    assert.match(run.stderr, /npm audit failed in .*: no answer within 1\.5 s/);
  });
});

// The audit is judged against the advisories npm reports and the repository's .github/release/audit-allow.json.
describe("the audit and its allow list", () => {
  const ok = { "ct-test-ok": plugin(cjs(STEPS)) };
  const allowRepo = (allow) => {
    const dir = repo({ plugins: ["ct-test-ok"] }, ok);
    if (allow !== undefined) {
      mkdirSync(join(dir, ".github", "release"), { recursive: true });
      writeFileSync(join(dir, ".github", "release", "audit-allow.json"), typeof allow === "string" ? allow : JSON.stringify(allow));
    }
    return dir;
  };
  const entry = (extra = {}) => ({ id: ID, reason: "only reads the patterns of .releaserc", expires: "2026-12-31", ...extra });
  const today = "2026-10-03";
  const braces = report({ braces: { name: "braces", via: [advisory(ID)] } });
  const run = (allow, audit, options = {}) => check(allowRepo(allow), { audit, today, ...options });

  test("no advisory passes, with or without an allow file", () => {
    passes(run(undefined, report()));
    passes(run([], report()));
    assert.match(run(undefined, report()).stdout, /npm audit reports no advisory/);
  });

  test("an advisory that is not listed fails, whatever its severity", () => {
    fails(run(undefined, braces), new RegExp(`^${ID} \\(high, braces\\): Problem in braces: not allowed; fix it or list it in .github/release/audit-allow.json$`));
    const low = report({ braces: { name: "braces", via: [{ ...advisory(ID), severity: "low" }] } });
    fails(run([], low), /GHSA-vfj7-8cjw-p6xm \(low, braces\)/);
    fails(run([entry({ id: OTHER })], braces), /GHSA-vfj7-8cjw-p6xm .* not allowed/, new RegExp(`^${OTHER} is listed in .* does not report it`));
  });

  test("a listed advisory passes and is printed with its reason and date", () => {
    const result = run([entry()], braces);
    passes(result);
    assert.match(result.stdout, new RegExp(`^allowed ${ID} until 2026-12-31: only reads the patterns of .releaserc$`, "m"));
  });

  test("an advisory is allowed on its last day and expired the day after", () => {
    passes(run([entry({ expires: today })], braces));
    fails(run([entry({ expires: today })], braces, { today: "2026-10-04" }), new RegExp(`^${ID} .*: allowed until 2026-10-03, which has passed: only reads`));
    fails(run([entry({ expires: "2020-01-01" })], braces), /allowed until 2020-01-01, which has passed/);
  });

  test("a listed advisory that npm does not report is stale and fails", () => {
    fails(run([entry()], report()), new RegExp(`^${ID} is listed in .github/release/audit-allow.json but npm audit does not report it: remove the entry$`));
  });

  test("the verdict covers every advisory, listed or not", () => {
    const both = report({ braces: { name: "braces", via: [advisory(ID)] }, other: { name: "other", via: [advisory(OTHER, "other")] } });
    const result = run([entry()], both);
    fails(result, new RegExp(`^${OTHER} \\(high, other\\)`));
    assert.match(result.stdout, new RegExp(`allowed ${ID}`));
    passes(run([entry(), entry({ id: OTHER })], both));
  });

  test("chained vulnerabilities resolve to the advisory at the end of the chain", () => {
    const chain = report({
      "ct-consumer": { name: "ct-consumer", via: ["micromatch"] },
      micromatch: { name: "micromatch", via: ["braces"] },
      braces: { name: "braces", via: [advisory(ID)] },
    });
    const result = run([entry()], chain);
    passes(result);
    assert.equal(result.stdout.match(/^allowed /gm).length, 1);
    fails(run(undefined, chain), new RegExp(`^${ID} \\(high, braces\\)`));
  });

  test("a cycle of vulnerabilities ends, and a vulnerability without any advisory fails", () => {
    const loop = report({ a: { name: "a", via: ["b"] }, b: { name: "b", via: ["a", advisory(ID, "b")] } });
    passes(run([entry()], loop));
    fails(run([], report({ a: { name: "a", via: ["b"] }, b: { name: "b", via: ["a"] } })), /reports a without any advisory/, /reports b without any advisory/);
    fails(run([], report({ a: { name: "a", via: [] } })), /reports a without any advisory/);
  });

  test("an advisory whose url carries no GHSA id fails and cannot be listed", () => {
    const cve = report({ pkg: { name: "pkg", via: [{ ...advisory(ID, "pkg"), url: "https://example.org/CVE-2026-0001" }] } });
    fails(run([entry()], cve), /advisory of pkg without a GHSA url under https:\/\/github.com\/advisories\/: Problem in pkg/, /is listed in .* does not report it/);
  });

  test("only a url under github.com/advisories names an advisory", () => {
    for (const url of [`https://example.org/advisories/${ID}`, `http://github.com/advisories/${ID}`, `https://github.com/advisories/${ID}/`, `https://github.com/advisories/${ID}?x=1`, `https://github.com/other/advisories/${ID}`, ID, undefined, null, [`https://github.com/advisories/${ID}`], { href: `https://github.com/advisories/${ID}` }, 5]) {
      const odd = report({ braces: { name: "braces", via: [{ ...advisory(ID), url }] } });
      fails(run([entry()], odd), /advisory of braces without a GHSA url under/, /is listed in .* does not report it/);
    }
  });

  test("a report whose count disagrees with its list fails, whatever npm's exit status", () => {
    const counted = (total, vulnerabilities = {}) => JSON.stringify({ auditReportVersion: 2, vulnerabilities, metadata: { vulnerabilities: { total } } });
    fails(run([], counted(5)), /the report lists 0 vulnerabilities but counts 5 in its metadata: npm's output changed/);
    fails(run([entry()], counted(5), { npm: 1 }), /lists 0 vulnerabilities but counts 5/);
    fails(run([], counted(0, { braces: { name: "braces", via: [advisory(ID)] } })), /lists 1 vulnerabilities but counts 0/);
    fails(run([], counted(2, { braces: { name: "braces", via: [advisory(ID)] } })), /lists 1 vulnerabilities but counts 2/);
    for (const metadata of [undefined, {}, { vulnerabilities: {} }, { vulnerabilities: { total: "0" } }, { vulnerabilities: { total: null } }, { vulnerabilities: 0 }, null]) {
      fails(run([], JSON.stringify({ auditReportVersion: 2, vulnerabilities: {}, metadata })), /the report lists 0 vulnerabilities but counts .* in its metadata/);
    }
  });

  test("the test-only date override is refused in GitHub Actions and must be a date", () => {
    for (const override of ["2020-01-01", ""]) {
      for (const actions of ["true", "false", "", "0"]) {
        fails(run([entry()], braces, { env: { GITHUB_ACTIONS: actions }, today: override }), /CHECK_TOOLCHAIN_TODAY .* refused in GitHub Actions/);
      }
    }
    const soon = new Date(Date.now() + 30 * 86_400_000).toISOString().slice(0, 10);
    passes(check(allowRepo([entry({ expires: soon })]), { audit: braces, env: { GITHUB_ACTIONS: "true" } }));
    for (const bad of ["", "tomorrow", "2026-02-30", "2026-10-3"]) {
      fails(run([entry()], braces, { today: bad }), /CHECK_TOOLCHAIN_TODAY is not a date/);
    }
  });

  test("npm audit is read whatever its exit status", () => {
    passes(run([entry()], braces, { npm: 1 }));
    passes(run([entry()], braces, { npm: 0 }));
    fails(run([], braces, { npm: 1 }), /not allowed/);
  });

  test("an output that is not a report fails", () => {
    fails(run([entry()], "Segmentation fault\n", { npm: 1 }), /npm audit failed in .*: exit status 1, and its output is not JSON: Segmentation fault/);
    fails(run([entry()], "", { npm: 0 }), /npm audit failed in .*: exit status 0, and its output is not JSON/);
    fails(run([entry()], "{", { npm: 0 }), /output is not JSON/);
    fails(run([entry()], "[]"), /the report is not a JSON object/);
    fails(run([entry()], "null"), /the report is not a JSON object/);
    fails(run([entry()], JSON.stringify({ error: { code: "ENOAUDIT", summary: "registry unreachable" } }), { npm: 1 }), /npm audit failed in .*: ENOAUDIT: registry unreachable/);
    fails(run([entry()], JSON.stringify({ auditReportVersion: 3, vulnerabilities: {} })), /unknown format/);
    fails(run([entry()], JSON.stringify({ auditReportVersion: 2 })), /unknown format/);
    fails(run([entry()], JSON.stringify({ auditReportVersion: 2, vulnerabilities: [], metadata: { vulnerabilities: { total: 0 } } })), /unknown format/);
    fails(run([entry()], JSON.stringify({ auditReportVersion: 2, vulnerabilities: [advisory(ID)], metadata: { vulnerabilities: { total: 1 } } })), /unknown format/);
    fails(run([], report(), { npm: 1 }), /exit status 1 without any vulnerability in the report/);
  });

  describe("an allow file that is not valid", () => {
    const invalid = (allow, pattern) => fails(run(allow, braces), pattern);

    test("not JSON, not an array, not objects", () => {
      invalid("[", /audit-allow.json is not valid JSON/);
      invalid("", /audit-allow.json is not valid JSON/);
      invalid({ id: ID }, /must be an array/);
      invalid([5], /entry 0 is not an object/);
      invalid([null], /entry 0 is not an object/);
      invalid([[]], /entry 0 is not an object/);
    });

    test("unknown keys, and missing, empty or non-string fields", () => {
      invalid([entry({ why: "x" })], /entry 0 has an unknown key: why/);
      for (const key of ["id", "reason", "expires"]) {
        const missing = entry();
        delete missing[key];
        invalid([missing], new RegExp(`entry 0 needs a non-empty string "${key}"`));
        invalid([entry({ [key]: "" })], new RegExp(`needs a non-empty string "${key}"`));
        invalid([entry({ [key]: "  " })], new RegExp(`needs a non-empty string "${key}"`));
        invalid([entry({ [key]: 5 })], new RegExp(`needs a non-empty string "${key}"`));
      }
    });

    test("a malformed id", () => {
      for (const id of ["GHSA-vfj7-8cjw-p6x", "ghsa-vfj7-8cjw-p6xm", "GHSA-VFJ7-8CJW-P6XM", "GHSA-vfj7-8cjw-p6xm-aaaa", "CVE-2026-0001", "GHSA-aeio-8cjw-p6xm", " GHSA-vfj7-8cjw-p6xm"]) {
        invalid([entry({ id })], /entry 0 has a malformed id/);
      }
    });

    test("a malformed date", () => {
      for (const expires of ["2026-13-01", "2026-02-30", "2026-1-01", "20261231", "2026-12-31T00:00:00Z", "tomorrow", "2026-12-31 "]) {
        invalid([entry({ expires })], /entry 0 has an expires that is not a YYYY-MM-DD date/);
      }
    });

    test("a date more than 180 days ahead", () => {
      passes(run([entry({ expires: "2027-04-01" })], braces));
      invalid([entry({ expires: "2027-04-02" })], /entry 0 expires on 2027-04-02, more than 180 days after 2026-10-03/);
      invalid([entry({ expires: "2999-01-01" })], /more than 180 days/);
    });

    test("a repeated id", () => {
      invalid([entry(), entry({ reason: "again" })], /entry 1 repeats the id GHSA-vfj7-8cjw-p6xm/);
    });

    test("every error is reported", () => {
      fails(run([entry({ why: 1, expires: "x" }), entry({ id: "nope" })], braces), /unknown key/, /not a YYYY-MM-DD date/, /entry 1 has a malformed id/);
    });

    test("a file that cannot be read", () => {
      const dir = allowRepo();
      mkdirSync(join(dir, ".github", "release", "audit-allow.json"), { recursive: true });
      fails(check(dir, { audit: braces, today }), /cannot read .github\/release\/audit-allow.json: EISDIR/);
    });
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
