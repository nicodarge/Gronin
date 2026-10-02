// Run with the install prefix as cwd: node check-toolchain.mjs <path to .releaserc>
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";

const fail = (message) => {
  console.error(`check-toolchain: ${message}`);
  process.exit(1);
};

const releaserc = process.argv[2];
if (!releaserc) {
  console.error("usage: node check-toolchain.mjs <path to .releaserc>");
  process.exit(2);
}

const plugins = (JSON.parse(readFileSync(releaserc, "utf8")).plugins ?? []).map((plugin) =>
  Array.isArray(plugin) ? plugin[0] : plugin,
);
if (plugins.length === 0) fail(`${releaserc} names no plugin`);

// A child per name: a real import() resolved against the prefix, the way semantic-release loads it.
for (const name of new Set(["semantic-release", ...plugins])) {
  if (typeof name !== "string") fail(`${releaserc} names a plugin that is not a string`);
  const child = spawnSync(
    process.execPath,
    ["--input-type=module", "-e", "await import(process.argv[1])", "--", name],
    { encoding: "utf8" },
  );
  if (child.status !== 0) {
    const lines = child.stderr.split("\n").filter((line) => line.trim() !== "");
    const reason = lines.find((line) => /^\w*Error/.test(line)) ?? lines.at(-1) ?? "no output";
    fail(`${name} does not load: ${reason}`);
  }
  console.log(`loaded ${name}`);
}

if (spawnSync("npm", ["audit", "--audit-level=low"], { stdio: "inherit" }).status !== 0) {
  fail("npm audit reports advisories in the installed tree");
}
