#!/usr/bin/env node
// mcpx npm shim — resolves the platform binary installed by postinstall
// (~/.mcpx/bin) and forwards argv + exit code, so `mcpx`, `npx mcpx` and the
// pi-maestro-flow bridge all share one entry point.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const binary = process.platform === "win32"
  ? join(homedir(), ".mcpx", "bin", "mcpx.exe")
  : join(homedir(), ".mcpx", "bin", "mcpx");

const candidates = [process.env.MCPX_BIN, binary].filter(Boolean);
const resolved = candidates.find((candidate) => candidate && existsSync(candidate));

if (!resolved) {
  console.error(
    "mcpx binary not found. Run the package postinstall again (npm rebuild mcpx-for-pmf) or set MCPX_BIN.",
  );
  process.exit(1);
}

const result = spawnSync(resolved, process.argv.slice(2), {
  stdio: "inherit",
  shell: process.platform === "win32" && /\.(cmd|bat)$/i.test(resolved),
});
if (result.error) {
  console.error(`mcpx: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
