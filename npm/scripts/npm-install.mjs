// mcpx-for-pmf postinstall: install the platform binary to ~/.mcpx/bin.
//
// Source resolution order:
//   1. MCPX_NPM_BINARY — direct path to a local zip/tar.gz (e.g. a GoReleaser
//      artifact or a local `go build` packaged archive); bypasses downloads.
//   2. MCPX_NPM_URL — full URL to the archive (useful for private mirrors).
//   3. GitHub Releases of the configured repository (default: opentokenz/mcpx)
//      using the GoReleaser asset naming: mcpx_{version}_{os}_{arch}.zip|.tar.gz
//      with checksums.txt verification.
//
// The install target (~/.mcpx/bin/mcpx[.exe]) is exactly what the
// pi-maestro-flow bridge and /mcpx panel probe, so a plain
// `npm i -g mcpx-for-pmf` gives pi automatic association.

import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";

const PKG_VERSION = process.env.npm_package_version ?? "0.9.7";
const REPO = process.env.MCPX_NPM_REPO ?? "opentokenz/mcpx";
const TARGET_DIR = join(homedir(), ".mcpx", "bin");
const TARGET = process.platform === "win32"
  ? join(TARGET_DIR, "mcpx.exe")
  : join(TARGET_DIR, "mcpx");

const log = (message) => console.log(`[mcpx-for-pmf] ${message}`);
const fail = (message) => {
  console.error(`[mcpx-for-pmf] ${message}`);
  process.exit(1);
};

function platformAssetName() {
  const os = process.platform === "win32" ? "windows" : process.platform === "darwin" ? "darwin" : "linux";
  const arch = process.arch === "arm64" ? "arm64" : "amd64";
  return `mcpx_${PKG_VERSION}_${os}_${arch}`;
}

function verifyChecksum(archivePath, archiveName, checksumsText) {
  const digest = createHash("sha256").update(readFileSync(archivePath)).digest("hex");
  const line = (checksumsText ?? "").split(/\r?\n/).find((l) => l.trim().endsWith(`  ${archiveName}`));
  if (!line) {
    log("checksums.txt entry not found; skipping verification (set MCPX_NPM_URL to a trusted mirror)");
    return;
  }
  const expected = line.trim().split(/\s+/)[0];
  if (expected !== digest) fail(`sha256 mismatch for ${archiveName}: expected ${expected}, got ${digest}`);
  log("sha256 verified");
}

function extractArchive(archivePath) {
  const tmp = join(dirname(archivePath), "mcpx-extract");
  rmSync(tmp, { recursive: true, force: true });
  mkdirSync(tmp, { recursive: true });
  const isZip = /\.zip$/i.test(archivePath);
  let result;
  if (isZip) {
    if (process.platform === "win32") {
      // Git Bash ships GNU tar (no zip support); use the system bsdtar.
      const systemTar = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe");
      result = spawnSync(existsSync(systemTar) ? systemTar : "tar", ["-xf", archivePath, "-C", tmp], { stdio: "inherit" });
    } else if (process.platform === "darwin") {
      result = spawnSync("tar", ["-xf", archivePath, "-C", tmp], { stdio: "inherit" });
    } else {
      result = spawnSync("unzip", ["-q", archivePath, "-d", tmp], { stdio: "inherit" });
    }
  } else {
    result = spawnSync("tar", ["-xzf", archivePath, "-C", tmp], { stdio: "inherit" });
  }
  if (result.status !== 0) fail(`extract failed for ${archivePath}`);
  return tmp;
}

function findBinaryIn(dir) {
  for (const candidate of [join(dir, "mcpx.exe"), join(dir, "mcpx"), join(dir, "bin", "mcpx.exe"), join(dir, "bin", "mcpx")]) {
    if (existsSync(candidate)) return candidate;
  }
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (/^mcpx(\.exe)?$/.test(entry)) return full;
  }
  return undefined;
}

function download(url, target, label) {
  log(`downloading ${label} …`);
  const result = spawnSync("curl", ["-fsSL", "-o", target, url], { stdio: "inherit", timeout: 300_000 });
  if (result.status !== 0) fail(`download failed: ${url}`);
}

function main() {
  const direct = process.env.MCPX_NPM_BINARY;
  const urlOverride = process.env.MCPX_NPM_URL;
  const archiveName = `${platformAssetName()}${process.platform === "win32" ? ".zip" : ".tar.gz"}`;
  const workDir = resolve(process.env.MCPX_NPM_WORKDIR ?? join(homedir(), ".mcpx", "tmp"));
  mkdirSync(workDir, { recursive: true });
  const archivePath = join(workDir, archiveName);

  try {
    let sourcePath = direct ? resolve(direct) : undefined;
    if (sourcePath && !existsSync(sourcePath)) fail(`MCPX_NPM_BINARY not found: ${sourcePath}`);
    if (!sourcePath) {
      const url = urlOverride ?? `https://github.com/${REPO}/releases/download/v${PKG_VERSION}/${archiveName}`;
      download(url, archivePath, archiveName);
      const checksums = spawnSync(
        "curl",
        ["-fsSL", `https://github.com/${REPO}/releases/download/v${PKG_VERSION}/checksums.txt`],
        { encoding: "utf8", timeout: 60_000 },
      );
      verifyChecksum(archivePath, archiveName, checksums.status === 0 ? checksums.stdout : "");
      sourcePath = archivePath;
    }

    const extracted = extractArchive(sourcePath);
    const binary = findBinaryIn(extracted);
    if (!binary) fail("extracted archive does not contain the mcpx binary");
    mkdirSync(TARGET_DIR, { recursive: true });
    writeFileSync(TARGET, readFileSync(binary));
    if (process.platform !== "win32") chmodSync(TARGET, 0o755);
    rmSync(extracted, { recursive: true, force: true });
    log(`installed ${TARGET}`);
  } finally {
    rmSync(archivePath, { force: true });
  }
}

main();
