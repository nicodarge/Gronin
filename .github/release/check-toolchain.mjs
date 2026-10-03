// Usage: node check-toolchain.mjs <install prefix> [repository root]
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Budget for every load together, canaries included; keep it well under the job's timeout-minutes. The environment variables only serve the tests.
const LOAD_LIMIT_MS = Number(process.env.CHECK_TOOLCHAIN_LIMIT_MS) || 180_000;
const AUDIT_LIMIT_MS = Number(process.env.CHECK_TOOLCHAIN_AUDIT_LIMIT_MS) || 120_000;
// The advisories this repository accepts for a while: [{ "id": "GHSA-xxxx-xxxx-xxxx", "reason": "...", "expires": "YYYY-MM-DD" }].
const ALLOW_FILE = ".github/release/audit-allow.json";
const ALLOW_KEYS = ["id", "reason", "expires"];
const ALLOW_MAX_DAYS = 180;
const ADVISORY_URL = "https://github.com/advisories/";
const GHSA = /^GHSA(?:-[2-9cfghjmpqrvwx]{4}){3}$/;
const DAY_MS = 86_400_000;
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

// Midnight UTC of a YYYY-MM-DD date that exists, as a number; NaN for anything else.
function dayOf(text) {
  const time = /^\d{4}-\d{2}-\d{2}$/.test(text) ? Date.parse(`${text}T00:00:00Z`) : NaN;
  return Number.isNaN(time) || new Date(time).toISOString().slice(0, 10) !== text ? NaN : time;
}
// The environment variable only serves the tests.
if (process.env.CHECK_TOOLCHAIN_TODAY !== undefined && process.env.GITHUB_ACTIONS !== undefined) {
  console.error("CHECK_TOOLCHAIN_TODAY moves the day the allow list is judged against and is refused in GitHub Actions");
  process.exit(1);
}
const todayText = process.env.CHECK_TOOLCHAIN_TODAY ?? new Date().toISOString().slice(0, 10);
const today = dayOf(todayText);
if (Number.isNaN(today)) {
  console.error(`CHECK_TOOLCHAIN_TODAY is not a date: ${todayText}`);
  process.exit(1);
}

// The allow list as { entries } when it is valid, { errors } otherwise; a missing file is an empty list.
function readAllowList() {
  let text;
  try {
    text = readFileSync(join(root, ALLOW_FILE), "utf8");
  } catch (error) {
    return error.code === "ENOENT" ? { entries: [] } : { errors: [`cannot read ${ALLOW_FILE}: ${error.message}`] };
  }
  let list;
  try {
    list = JSON.parse(text);
  } catch (error) {
    return { errors: [`${ALLOW_FILE} is not valid JSON: ${error.message}`] };
  }
  if (!Array.isArray(list)) {
    return { errors: [`${ALLOW_FILE} must be an array of { id, reason, expires } objects`] };
  }
  const errors = [];
  const seen = new Set();
  list.forEach((entry, index) => {
    const where = `${ALLOW_FILE} entry ${index}`;
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      errors.push(`${where} is not an object`);
      return;
    }
    for (const key of Object.keys(entry).filter((name) => !ALLOW_KEYS.includes(name))) {
      errors.push(`${where} has an unknown key: ${key}`);
    }
    for (const key of ALLOW_KEYS.filter((name) => typeof entry[name] !== "string" || entry[name].trim() === "")) {
      errors.push(`${where} needs a non-empty string "${key}"`);
    }
    if (typeof entry.id === "string" && entry.id.trim() !== "") {
      if (!GHSA.test(entry.id)) {
        errors.push(`${where} has a malformed id: ${entry.id}`);
      } else if (seen.has(entry.id)) {
        errors.push(`${where} repeats the id ${entry.id}`);
      }
      seen.add(entry.id);
    }
    if (typeof entry.expires === "string" && entry.expires.trim() !== "") {
      const expires = dayOf(entry.expires);
      if (Number.isNaN(expires)) {
        errors.push(`${where} has an expires that is not a YYYY-MM-DD date: ${entry.expires}`);
      } else if ((expires - today) / DAY_MS > ALLOW_MAX_DAYS) {
        errors.push(`${where} expires on ${entry.expires}, more than ${ALLOW_MAX_DAYS} days after ${todayText}`);
      }
    }
  });
  return errors.length > 0 ? { errors } : { entries: list };
}

// npm audit's report as the advisories it holds, by GHSA id; a "via" string names another vulnerable package of the report, whose advisories count too.
function advisoriesOf(vulnerabilities) {
  const found = new Map();
  const problem = [];
  const follow = (name, visited) => {
    if (visited.has(name)) {
      return [];
    }
    visited.add(name);
    const via = vulnerabilities[name]?.via;
    return (Array.isArray(via) ? via : []).flatMap((item) => (typeof item === "string" ? follow(item, visited) : item !== null && typeof item === "object" ? [item] : []));
  };
  for (const name of Object.keys(vulnerabilities)) {
    const items = follow(name, new Set());
    if (items.length === 0) {
      problem.push(`npm audit reports ${name} without any advisory`);
    }
    for (const item of items) {
      const url = typeof item.url === "string" ? item.url : "";
      const id = url.startsWith(ADVISORY_URL) ? url.slice(ADVISORY_URL.length) : "";
      if (GHSA.test(id)) {
        found.set(id, { id, package: item.name ?? name, severity: item.severity ?? "unknown", title: item.title ?? "no title" });
      } else {
        problem.push(`advisory of ${item.name ?? name} without a GHSA url under ${ADVISORY_URL}: ${item.title ?? item.url ?? "no title"}`);
      }
    }
  }
  return { found, problem };
}

function audit() {
  const failed = (reason) => fail(`npm audit failed in ${prefix}: ${reason}`);
  const run = spawnSync("npm", ["audit", "--json", "--no-update-notifier", "--prefix", prefix], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "inherit"],
    timeout: AUDIT_LIMIT_MS,
    killSignal: "SIGKILL",
    maxBuffer: MAX_BUFFER,
  });
  if (run.error) {
    return failed(run.error.code === "ETIMEDOUT" ? `no answer within ${AUDIT_LIMIT_MS / 1000} s` : run.error.message);
  }
  let report;
  try {
    report = JSON.parse(run.stdout);
  } catch {
    return failed(`${run.signal ? `killed by ${run.signal}` : `exit status ${run.status}`}, and its output is not JSON: ${run.stdout.trim().split("\n")[0] ?? ""}`);
  }
  if (report === null || typeof report !== "object" || Array.isArray(report)) {
    return failed("the report is not a JSON object");
  }
  if (report.error) {
    return failed(`${report.error.code ?? "error"}: ${report.error.summary ?? "no summary"}`);
  }
  if (report.auditReportVersion !== 2 || report.vulnerabilities === null || typeof report.vulnerabilities !== "object" || Array.isArray(report.vulnerabilities)) {
    return failed("the report has an unknown format: npm's output changed, update check-toolchain.mjs");
  }
  const vulnerabilities = report.vulnerabilities;
  const total = report.metadata?.vulnerabilities?.total;
  if (total !== Object.keys(vulnerabilities).length) {
    return failed(`the report lists ${Object.keys(vulnerabilities).length} vulnerabilities but counts ${total === undefined ? "none" : JSON.stringify(total)} in its metadata: npm's output changed, update check-toolchain.mjs`);
  }
  if (run.status !== 0 && Object.keys(vulnerabilities).length === 0) {
    return failed(`exit status ${run.status} without any vulnerability in the report`);
  }
  const { found, problem } = advisoriesOf(vulnerabilities);
  problem.forEach(fail);

  const allow = readAllowList();
  allow.errors?.forEach(fail);
  if (allow.errors) {
    return;
  }
  const allowed = new Map(allow.entries.map((entry) => [entry.id, entry]));
  for (const advisory of [...found.values()].sort((a, b) => a.id.localeCompare(b.id))) {
    const entry = allowed.get(advisory.id);
    const what = `${advisory.id} (${advisory.severity}, ${advisory.package}): ${advisory.title}`;
    if (!entry) {
      fail(`${what}: not allowed; fix it or list it in ${ALLOW_FILE}`);
    } else if (today > dayOf(entry.expires)) {
      fail(`${what}: allowed until ${entry.expires}, which has passed: ${entry.reason}`);
    } else {
      console.log(`allowed ${advisory.id} until ${entry.expires}: ${entry.reason}`);
    }
  }
  for (const entry of allow.entries.filter(({ id }) => !found.has(id))) {
    fail(`${entry.id} is listed in ${ALLOW_FILE} but npm audit does not report it: remove the entry`);
  }
  if (found.size === 0 && allow.entries.length === 0) {
    console.log("npm audit reports no advisory");
  }
}

audit();
process.exitCode = failures.length > 0 ? 1 : 0;
