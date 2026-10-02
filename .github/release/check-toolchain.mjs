// Usage: node check-toolchain.mjs <install prefix> [.releaserc]
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// One bound for every load together, well under the job's timeout-minutes.
const LOAD_LIMIT_MS = 180_000;

// Loads through semantic-release's own loadPlugin; the token comes last, so a plugin that calls process.exit(0) at load never prints it.
const CHILD = `
const [lib, lodash, kind, name, token] = process.argv.slice(1);
const fail = (message) => { process.stderr.write("Error: " + message + "\\n"); process.exit(1); };
const { loadPlugin } = await import(lib + "/plugins/utils.js");
const { default: definitions } = await import(lib + "/definitions/plugins.js");
const { isFunction, isPlainObject } = await import(lodash);
const plugin = await loadPlugin({ cwd: process.cwd() }, name, {});
if (kind === "plugin") {
  if (!isPlainObject(plugin)) fail("its export is not a plain object, which semantic-release rejects (EPLUGINSCONF)");
  for (const [type, step] of Object.entries(plugin)) {
    if (definitions[type] && !isFunction(step)) fail('its "' + type + '" export is not a function');
  }
} else if (kind !== "tool" && !isFunction(plugin) && !(isPlainObject(plugin) && isFunction(plugin[kind]))) {
  fail('it does not implement the "' + kind + '" step (EPLUGIN)');
}
process.stdout.write(token, () => process.exit(0));
`;

function fail(message) {
  console.error(message);
  process.exit(1);
}

function firstLine(error) {
  return String(error.message).split("\n")[0];
}

const [prefixArg, configArg = ".releaserc"] = process.argv.slice(2);
if (!prefixArg) {
  fail("usage: check-toolchain.mjs <install prefix> [.releaserc]");
}
const prefix = resolve(prefixArg);

let config;
try {
  config = JSON.parse(readFileSync(configArg, "utf8"));
} catch (error) {
  fail(`${configArg}: ${firstLine(error)}`);
}
if (config === null || typeof config !== "object" || Array.isArray(config)) {
  fail(`${configArg}: the top level is not an object`);
}

const libDir = join(prefix, "node_modules", "semantic-release", "lib");
const toolUtils = join(libDir, "plugins", "utils.js");
let utils;
let definitions;
let lodashFile;
let lodash;
try {
  utils = await import(pathToFileURL(toolUtils).href);
  definitions = (await import(pathToFileURL(join(libDir, "definitions", "plugins.js")).href)).default;
  lodashFile = createRequire(toolUtils).resolve("lodash-es");
  lodash = await import(pathToFileURL(lodashFile).href);
} catch (error) {
  fail(`${prefix}: cannot read semantic-release's plugin loader: ${firstLine(error)}`);
}
const { castArray, isNil, isString } = lodash;

// What the tool loads, from where it names plugins: "plugins" first, then each step key.
const loads = [{ kind: "tool", name: "semantic-release" }];
const entries = config.plugins ? castArray(config.plugins) : [];
if (entries.length === 0) {
  fail(`${configArg} names no plugin: "plugins" is missing or empty`);
}
for (const [index, entry] of entries.entries()) {
  if (!utils.validatePlugin(entry)) {
    fail(`${configArg}: plugin ${index + 1} is not a module name, a [name, options] pair or an object with a path: ${JSON.stringify(entry)}`);
  }
  const [name] = utils.parseConfig(entry);
  if (isString(name)) {
    loads.push({ kind: "plugin", name });
  }
}
for (const [type, { required }] of Object.entries(definitions)) {
  const value = config[type];
  if (isNil(value) || (lodash.isPlainObject(value) && !value.path)) {
    continue;
  }
  if (!utils.validateStep({ required }, value)) {
    fail(`${configArg}: "${type}" is not a plugin definition or a list of them: ${JSON.stringify(value)}`);
  }
  for (const entry of castArray(value).filter(Boolean)) {
    const [name] = utils.parseConfig(entry);
    if (isString(name)) {
      loads.push({ kind: type, name });
    }
  }
}
if (loads.length === 1) {
  fail(`${configArg} names no plugin by module name`);
}

const token = randomUUID();
const deadline = Date.now() + LOAD_LIMIT_MS;
const libUrl = pathToFileURL(libDir).href;
const lodashUrl = pathToFileURL(lodashFile).href;
for (const { kind, name } of loads) {
  const shown = name === "" ? '""' : name;
  const label = kind === "tool" || kind === "plugin" ? shown : `${shown} (step ${kind})`;
  const args = ["--input-type=module", "-e", CHILD, "--", libUrl, lodashUrl, kind, name, token];
  const load = spawnSync(process.execPath, args, {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    timeout: Math.max(deadline - Date.now(), 1),
  });
  if (load.error?.code === "ETIMEDOUT") {
    fail(`${label} did not settle within ${LOAD_LIMIT_MS / 1000} s, the limit for all the loads together`);
  }
  if (load.error) {
    fail(`${label} does not load: ${load.error.message}`);
  }
  const lines = load.stderr.split("\n").filter((line) => line.trim() !== "");
  const content = lines.filter((line) => !/^Node\.js v|^\s*\^+\s*$|^\(Use |^\s+at /.test(line));
  if (load.status !== 0 || load.signal) {
    const reason =
      content.findLast((line) => /^\S*Error\b/.test(line)) ??
      (load.status === 13 ? "the import never settled (unsettled top-level await)" : undefined) ??
      content.at(-1) ??
      (load.signal ? `killed by ${load.signal}` : `exit status ${load.status}`);
    process.stdout.write(load.stdout);
    fail(`${label} does not load: ${reason}`);
  }
  if (!load.stdout.endsWith(token)) {
    fail(`${label} does not load: the process exited before its import settled`);
  }
  process.stdout.write(load.stdout.slice(0, -token.length));
  if (lines.length > 0) {
    process.stderr.write(load.stderr);
    fail(`${label} loaded but wrote to stderr: a warning at load time is a defect`);
  }
  console.log(`loaded ${label}`);
}

const audit = spawnSync("npm", ["audit", "--audit-level=low", "--no-update-notifier", "--prefix", prefix], { stdio: "inherit" });
if (audit.status !== 0) {
  fail(`npm audit failed in ${prefix}: ${audit.error ? audit.error.message : `exit status ${audit.status}`}`);
}
