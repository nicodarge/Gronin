// Run from the install prefix, so every import resolves against the installed tree:
//   node check-toolchain.mjs <path to .releaserc>
// Fails when .releaserc names no plugin, when semantic-release or a plugin it names does
// not load, or when `npm audit` reports anything in the installed tree.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const releaserc = process.argv[2];
if (!releaserc) {
  console.error("usage: node check-toolchain.mjs <path to .releaserc>");
  process.exit(2);
}

const plugins = (JSON.parse(readFileSync(releaserc, "utf8")).plugins ?? []).map((plugin) =>
  Array.isArray(plugin) ? plugin[0] : plugin,
);
if (plugins.length === 0) {
  console.error(`${releaserc} names no plugin`);
  process.exit(1);
}

for (const name of new Set(["semantic-release", ...plugins])) {
  await import(name);
  console.log(`loaded ${name}`);
}

execFileSync("npm", ["audit", "--audit-level=low"], { stdio: "inherit" });
