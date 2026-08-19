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
import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
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
  const arch = process.arch === "arm64" ? "arm64" : process.arch === "x64" ? "amd64" : undefined;
  if (arch === undefined) {
    fail(`unsupported platform: ${process.platform}/${process.arch} (GoReleaser builds linux/darwin amd64+arm64 and windows amd64 only)`);
  }
  if (process.platform === "win32" && arch === "arm64") {
    fail("unsupported platform: windows/arm64 (upstream does not build this artifact)");
  }
  return `mcpx_${PKG_VERSION}_${os}_${arch}`;
}

function verifyChecksum(archivePath, archiveName, checksumsText) {
  const digest = createHash("sha256").update(readFileSync(archivePath)).digest("hex");
  const line = (checksumsText ?? "").split(/\r?\n/).find((l) => l.trim().endsWith(`  ${archiveName}`));
  if (!line) {
    if (process.env.MCPX_NPM_SKIP_CHECKSUM === "1") {
      log("checksum verification skipped (MCPX_NPM_SKIP_CHECKSUM=1)");
      return;
    }
    fail(`checksums.txt has no entry for ${archiveName}; refusing to install an unverified binary (set MCPX_NPM_SKIP_CHECKSUM=1 to override)`);
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
  if (result.status !== 0) {
    console.error(
      `[mcpx-for-pmf] download failed: ${url}\n` +
      `  The fork publishes no prebuilt GitHub Releases yet. Options:\n` +
      `  - MCPX_NPM_BINARY=<path-to-zip-or-tar.gz>  install a local build\n` +
      `  - MCPX_NPM_REPO=<owner/repo>  point at a fork that publishes releases\n` +
      `  - MCPX_NPM_URL=<full-archive-url>  use a mirror`,
    );
    fail(`download failed: ${url}`);
  }
}

function downloadText(url, label) {
  const result = spawnSync("curl", ["-fsSL", url], { encoding: "utf8", timeout: 60_000 });
  if (result.status !== 0) fail(`${label} download failed (status ${result.status ?? "spawn error"})`);
  return String(result.stdout ?? "");
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
      verifyChecksum(archivePath, archiveName, downloadText(`https://github.com/${REPO}/releases/download/v${PKG_VERSION}/checksums.txt`, "checksums.txt"));
      sourcePath = archivePath;
    }

    const extracted = extractArchive(sourcePath);
    const binary = findBinaryIn(extracted);
    if (!binary) fail("extracted archive does not contain the mcpx binary");
    mkdirSync(TARGET_DIR, { recursive: true });
    // Atomic install: temp file + rename, so a half-written binary can never
    // shadow the previous one; a running mcpx holds the exe on Windows, so
    // surface that clearly instead of a raw EPERM.
    const tmpTarget = `${TARGET}.tmp-${process.pid}`;
    try {
      writeFileSync(tmpTarget, readFileSync(binary));
      if (process.platform !== "win32") chmodSync(tmpTarget, 0o755);
      rmSync(TARGET, { force: true }); // Windows rename cannot replace a live exe
      renameSync(tmpTarget, TARGET);
    } catch (error) {
      rmSync(tmpTarget, { force: true });
      if (process.platform === "win32" && error && typeof error === "object" && "code" in error) {
        fail(`install failed (${String(error.code)}) — stop the running mcpx process first, then reinstall`);
      }
      throw error;
    }
    rmSync(extracted, { recursive: true, force: true });
    log(`installed ${TARGET}`);
  } finally {
    rmSync(archivePath, { force: true });
  }
}

main();
