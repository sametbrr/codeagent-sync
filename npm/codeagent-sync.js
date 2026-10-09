#!/usr/bin/env node
// Installs the codeagent-sync program for this package's version at
// ~/.local/bin (the place the hooks of automatic sync start it from), then
// runs it with the given arguments. Runs as the postinstall script too; when
// a package manager skips that (pnpm does by default), the first run installs.
//
// When the package manager links its own launcher at that very path (npm with
// its global prefix at ~/.local, pnpm with PNPM_HOME at ~/.local/bin), the
// program lives in ~/.codeagent-sync/bin instead and the launcher keeps the
// path; the hooks then start the launcher, which runs the program.

"use strict";

const crypto = require("crypto");
const fs = require("fs");
const http = require("http");
const https = require("https");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const REPO = "sametbrr/codeagent-sync";
const VERSION = require("./package.json").version;
const WINDOWS = process.platform === "win32";

function installDir() {
  return process.env.CODEAGENT_SYNC_INSTALL_DIR || path.join(os.homedir(), ".local", "bin");
}

const PROGRAM = WINDOWS ? "codeagent-sync.exe" : "codeagent-sync";

// asideDir holds the program when a package manager's launcher has its place.
function asideDir() {
  return path.join(os.homedir(), ".codeagent-sync", "bin");
}

function samePath(a, b) {
  const norm = (p) => {
    try { return fs.realpathSync(p); } catch (_) { return path.resolve(p); }
  };
  return norm(a) === norm(b);
}

// isLauncher reports whether the file at p is a script (a package manager's
// launcher, or this wrapper through npm's link) rather than the program.
function isLauncher(p) {
  try {
    const fd = fs.openSync(p, "r");
    const head = Buffer.alloc(2);
    fs.readSync(fd, head, 0, 2, 0);
    fs.closeSync(fd);
    return head.toString("latin1") === "#!";
  } catch (_) {
    return false;
  }
}

// globalBinDir is the directory npm links this package's bin into, for a
// global install (<prefix>/lib/node_modules/codeagent-sync, bin in
// <prefix>/bin); "" otherwise.
function globalBinDir() {
  const modules = path.dirname(__dirname);
  if (WINDOWS || path.basename(modules) !== "node_modules" || path.basename(path.dirname(modules)) !== "lib") {
    return "";
  }
  return path.join(path.dirname(path.dirname(modules)), "bin");
}

// collides reports whether the package manager's launcher takes the
// program's place.
function collides() {
  const dir = installDir();
  if (isLauncher(path.join(dir, PROGRAM))) return true;
  const bin = globalBinDir();
  return bin !== "" && samePath(bin, dir);
}

function programPath() {
  return path.join(collides() ? asideDir() : installDir(), PROGRAM);
}

function assetName() {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
  const goarch = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!goos || !goarch) {
    throw new Error(`no codeagent-sync build for ${process.platform}/${process.arch}`);
  }
  return `codeagent-sync-${goos}-${goarch}${WINDOWS ? ".exe" : ""}`;
}

// installedVersion returns the version the program reports, "" when it is
// missing, or null when it cannot tell (a development build).
function installedVersion(program) {
  if (!fs.existsSync(program)) return "";
  const out = spawnSync(program, ["--version"], { encoding: "utf8" });
  const m = /version v?(\d+\.\d+\.\d+)/.exec(out.stdout || "");
  return m ? m[1] : null;
}

function older(a, b) {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    if (pa[i] !== pb[i]) return pa[i] < pb[i];
  }
  return false;
}

function get(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    (url.startsWith("http:") ? http : https)
      .get(url, { headers: { "User-Agent": "codeagent-sync-npm" } }, (res) => {
        if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location && redirects > 0) {
          res.resume();
          resolve(get(res.headers.location, redirects - 1));
          return;
        }
        if (res.statusCode !== 200) {
          res.resume();
          reject(new Error(`${url}: HTTP ${res.statusCode}`));
          return;
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
      })
      .on("error", reject);
  });
}

async function install(program) {
  const asset = assetName();
  // CODEAGENT_SYNC_RELEASE_URL points at another copy of the release (tests, mirrors).
  const base = process.env.CODEAGENT_SYNC_RELEASE_URL || `https://github.com/${REPO}/releases/download/v${VERSION}/`;
  process.stderr.write(`Installing codeagent-sync v${VERSION} at ${program}\n`);
  const [binary, sums] = await Promise.all([get(base + asset), get(base + "checksums.txt")]);

  const want = sums
    .toString("utf8")
    .split("\n")
    .map((l) => l.trim().split(/\s+/))
    .find((f) => f.length === 2 && f[1].replace(/^\*/, "") === asset);
  const got = crypto.createHash("sha256").update(binary).digest("hex");
  if (!want || want[0].toLowerCase() !== got) {
    throw new Error(`checksum mismatch for ${asset}; not installing it`);
  }

  fs.mkdirSync(path.dirname(program), { recursive: true });
  const tmp = `${program}.new-${process.pid}`;
  fs.writeFileSync(tmp, binary, { mode: 0o755 });
  if (WINDOWS && fs.existsSync(program)) {
    // A running .exe cannot be replaced, but it can be renamed.
    const old = `${program}.old`;
    try { fs.unlinkSync(old); } catch (_) {}
    fs.renameSync(program, old);
  }
  fs.renameSync(tmp, program);
}

async function ensure() {
  const program = programPath();
  const have = installedVersion(program);
  // Keep a newer program (installed by `codeagent-sync update`) and a
  // development build.
  if (have === "" || (have !== null && older(have, VERSION))) {
    await install(program);
  }
  return program;
}

async function main() {
  const args = process.argv.slice(2);
  if (args[0] === "--codeagent-sync-install") {
    try {
      await ensure();
    } catch (err) {
      // Never fail the package install: the first run tries again.
      process.stderr.write(`codeagent-sync: ${err.message}; it is installed on first use instead\n`);
    }
    return;
  }
  let program;
  try {
    program = await ensure();
  } catch (err) {
    process.stderr.write(`codeagent-sync: ${err.message}\nDownload it from https://github.com/${REPO}/releases\n`);
    process.exit(1);
  }
  const env = { ...process.env };
  if (!samePath(path.dirname(program), installDir())) {
    // Tell the program how it is started, for the hooks it writes.
    env.CODEAGENT_SYNC_LAUNCHER = path.join(installDir(), PROGRAM);
  }
  const res = spawnSync(program, args, { stdio: "inherit", env });
  if (res.error) {
    process.stderr.write(`codeagent-sync: ${res.error.message}\n`);
    process.exit(1);
  }
  process.exit(res.status === null ? 1 : res.status);
}

main();
