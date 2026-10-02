// Usage: node check-toolchain.mjs <install prefix> [repository root]
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Budget for every load together, canaries included; keep it well under the job's timeout-minutes. The environment variable only serves the tests.
const LOAD_LIMIT_MS = Number(process.env.CHECK_TOOLCHAIN_LIMIT_MS) || 180_000;
const AUDIT_LIMIT_MS = 120_000;
const MAX_BUFFER = 64 * 1024 * 1024;
const MAX_NOISE_LINES = 20;
const MARK = "check-toolchain:";

// The child asks the tool's own loader for the configuration (file, extends, defaults, validation, every plugin). The token comes last, after one tick, and the child then ends by itself.
const CHILD = `
const [lib, token] = process.argv.slice(1);
const mark = ${JSON.stringify(MARK)};
const firstLine = (e) => String(e instanceof Error ? e.message : e).split("\\n")[0];
const report = (text) => process.stderr.write(mark + " " + text + "\\n");
const detail = (e) => String(e.details ?? "").split("\\n").map((line) => line.trim()).filter(Boolean).at(-1)?.replace(/\\[([^\\]]*)\\]\\([^)]*\\)/g, "$1");
try {
  const { default: getConfig } = await import(lib + "/get-config.js");
  const { default: getLogger } = await import(lib + "/get-logger.js");
  const { extractErrors } = await import(lib + "/utils.js");
  const context = { cwd: process.cwd(), env: process.env, stdout: process.stdout, stderr: process.stderr };
  context.logger = getLogger(context);
  try {
    await getConfig(context, {});
  } catch (error) {
    for (const e of extractErrors(error)) {
      report(e && e.semanticRelease ? [e.code, e.message, detail(e)].filter(Boolean).join(" ") : (e instanceof Error ? e.name + ": " : "") + firstLine(e));
    }
    process.exitCode = 1;
  }
} catch (error) {
  report("semantic-release's configuration loader cannot be used: " + firstLine(error));
  process.exitCode = 1;
}
if (!process.exitCode) {
  await new Promise((resolve) => setImmediate(resolve));
  process.stdout.write(token);
}
`;

const failures = [];
function fail(message) {
  failures.push(message);
  console.error(message);
}

const [prefixArg, rootArg = "."] = process.argv.slice(2);
if (!prefixArg) {
  console.error("usage: check-toolchain.mjs <install prefix> [repository root]");
  process.exit(1);
}
const prefix = resolve(prefixArg);
const root = resolve(rootArg);
const libUrl = pathToFileURL(join(prefix, "node_modules", "semantic-release", "lib")).href;
const deadline = Date.now() + LOAD_LIMIT_MS;

function load(cwd) {
  const token = randomUUID();
  const started = Date.now();
  const run = spawnSync(process.execPath, ["--input-type=module", "-e", CHILD, "--", libUrl, token], {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    timeout: Math.max(deadline - started, 1),
    killSignal: "SIGKILL",
    maxBuffer: MAX_BUFFER,
  });
  const stdout = run.stdout ?? "";
  const lines = (run.stderr ?? "").split("\n").filter((line) => line.trim() !== "");
  return {
    run,
    seconds: Math.round((Date.now() - started) / 1000),
    stdout: stdout.replace(token, ""),
    tokenSeen: stdout.includes(token),
    reported: lines.filter((line) => line.startsWith(MARK)).map((line) => line.slice(MARK.length).trim()),
    noise: lines.filter((line) => !line.startsWith(MARK)),
  };
}

// What went wrong in one load, as lines; empty when it loaded, ended by itself and wrote nothing to stderr.
function problems(result) {
  const { run, seconds, tokenSeen, reported, noise, stdout } = result;
  const phase = tokenSeen ? "after loading" : "while loading";
  if (run.error?.code === "ETIMEDOUT") {
    const last = stdout.trimEnd().split("\n").at(-1) || "nothing reported";
    const limit = `limit ${LOAD_LIMIT_MS / 1000} s for the whole check`;
    let timeout = `loading did not finish within ${seconds} s (${limit}); last report: ${last}`;
    if (tokenSeen) {
      timeout = `the process did not end within ${seconds} s after loading (${limit}): a plugin keeps a handle open`;
    } else if (reported.length > 0) {
      timeout = `the process did not end within ${seconds} s after reporting an error (${limit}): a plugin keeps a handle open`;
    }
    return [...reported, timeout];
  }
  if (run.error?.code === "ENOBUFS") {
    return [`the load wrote more than ${MAX_BUFFER / 1024 / 1024} MiB to stdout or stderr`];
  }
  if (run.error) {
    return [`cannot run the loader: ${run.error.message}`];
  }
  if (reported.length > 0) {
    return reported;
  }
  if (run.status !== 0 || run.signal) {
    const content = noise.filter((line) => !/^Node\.js v|^\s*\^+\s*$|^\(Use |^\s+at /.test(line));
    const reason =
      content.findLast((line) => /^\S*Error\b/.test(line)) ??
      (run.status === 13 ? "the import never settled (unsettled top-level await)" : undefined) ??
      content.at(-1) ??
      (run.signal ? `killed by ${run.signal}` : `exit status ${run.status}`);
    return [`${phase}: ${reason}`];
  }
  if (!tokenSeen) {
    return ["the process ended before the configuration had loaded: a plugin called process.exit"];
  }
  if (noise.length > 0) {
    const more = noise.length > MAX_NOISE_LINES ? [`(${noise.length - MAX_NOISE_LINES} more lines not shown)`] : [];
    process.stderr.write(`${[...noise.slice(0, MAX_NOISE_LINES), ...more].join("\n")}\n`);
    return ["a plugin wrote to stderr while loading: a warning at load time is a defect"];
  }
  return [];
}

function canary(name, files, verdict) {
  const dir = mkdtempSync(join(tmpdir(), "check-toolchain-"));
  try {
    for (const [file, content] of Object.entries(files)) {
      writeFileSync(join(dir, file), content);
    }
    const result = load(dir);
    if (!verdict(result)) {
      fail(`canary "${name}" failed: semantic-release's internals changed, update check-toolchain.mjs`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const MISSING = "check-toolchain-canary-missing-plugin";
const present = "export async function verifyConditions() {}\nexport async function analyzeCommits() {}\n";
canary("a missing plugin fails", { ".releaserc": JSON.stringify({ plugins: [MISSING] }) }, (result) => !result.tokenSeen && result.reported.some((line) => line.includes(MISSING)));
canary(
  "a present plugin loads",
  { ".releaserc": JSON.stringify({ plugins: ["./canary.mjs"] }), "canary.mjs": present },
  (result) => result.tokenSeen && problems(result).length === 0 && result.stdout.includes('from "./canary.mjs"')
);

canary(
  "a missing step plugin fails",
  { ".releaserc": JSON.stringify({ plugins: ["./canary.mjs"], prepare: [MISSING] }), "canary.mjs": present },
  (result) => !result.tokenSeen && result.reported.some((line) => line.includes(MISSING))
);

const real = load(root);
if (real.run.error?.code !== "ENOBUFS") {
  process.stdout.write(real.stdout);
}
const found = problems(real);
for (const line of found) {
  fail(line);
}
if (found.length === 0 && !real.stdout.includes("Loaded plugin")) {
  fail("the load reported no plugin at all: semantic-release's internals changed, update check-toolchain.mjs");
}

const audit = spawnSync("npm", ["audit", "--audit-level=low", "--no-update-notifier", "--prefix", prefix], { stdio: "inherit", timeout: AUDIT_LIMIT_MS, killSignal: "SIGKILL" });
if (audit.status !== 0) {
  fail(`npm audit failed in ${prefix}: ${audit.error ? audit.error.message : `exit status ${audit.status}`}`);
}
process.exitCode = failures.length > 0 ? 1 : 0;
