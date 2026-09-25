[![CI](https://github.com/sametbrr/codeagent-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/sametbrr/codeagent-sync/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](go.mod)

# codeagent-sync

Keeps Claude Code and Codex configuration — instructions, settings, skills, agents, hooks, MCP servers and plugin lists — the same on every machine, end-to-end encrypted.

> 🇹🇷 Türkçe için [README.tr.md](README.tr.md)

[Quick Start](#quick-start) • [Installation](#installation) • [Usage](#usage) • [What Is Synced](#what-is-synced) • [Troubleshooting](#troubleshooting) • [Security](#security)

---

## Quick Start

```bash
npm install -g codeagent-sync   # or: pnpm add -g codeagent-sync
codeagent-sync init             # storage, passphrase, first sync
```

On every other machine: `codeagent-sync join-code` on a machine that is set up, then `codeagent-sync init --join <code>` on the new one.

---

## Features

- **Both tools, one layout** — `~/.claude`, `~/.claude.json` (MCP servers only), `~/.codex` and the skills both tools read in `~/.agents/skills`
- **End-to-end encrypted** — every file is encrypted with [age](https://age-encryption.org) before it leaves the machine; the storage sees names and sizes only
- **Merges, not overwrites** — settings files merge item by item, so two machines changing different settings never conflict; comments in `config.toml` stay
- **Portable paths** — home-directory paths are translated, so machines with other user names or systems get their own
- **Shares between tools** — judges whether a skill or MCP server one tool has works in the other, and shares it when you agree
- **Automatic** — hooks sync in the background and tell the agent about conflicts and new things to share, once
- **Your choice of what syncs** — include and exclude rules for every machine or just one, down to a single setting
- **Machine inventory** — shows how the tools and MCP programs are installed elsewhere and what this machine lacks
- **Safe** — backups and `undo` for every change, conflicts set aside instead of overwritten, conditional writes against races
- **Storage** — Cloudflare R2, Amazon S3 and S3-compatible services, Google Cloud Storage, WebDAV

---

## Requirements

- macOS, Linux or Windows
- Claude Code and/or Codex (a tool that is not installed on a machine is left alone there)
- A bucket on Cloudflare R2, S3, GCS or a WebDAV server, with credentials that can read and write it
- Node.js 18 or later to install with npm or pnpm (or a release binary, or Go 1.24 or later to build from source)

---

## Installation

```bash
npm install -g codeagent-sync
pnpm add -g codeagent-sync
```

The package installs the program for your system at `~/.local/bin/codeagent-sync` (on Windows `%USERPROFILE%\.local\bin`), checked against the release's checksums. pnpm skips install scripts by default; the first run installs it then. Keep it at the same place on every machine: the hooks of automatic sync start it from there. `codeagent-sync update` replaces it with the latest release.

<details>
<summary><strong>Release binaries and building from source</strong></summary>

Binaries for macOS, Linux and Windows are on the [releases](https://github.com/sametbrr/codeagent-sync/releases) page: rename the one for your system to `codeagent-sync` and put it in `~/.local/bin`.

To build from source (Go 1.24 or later):

```bash
git clone https://github.com/sametbrr/codeagent-sync && cd codeagent-sync
make install
```

</details>

---

## Usage

### Set up the first machine

```bash
codeagent-sync init
```

`init` asks for the storage and a passphrase, creates the bucket if the credentials allow it, lists what it will upload and asks before it does, then offers automatic sync. The passphrase cannot be recovered: keep it in your password manager. Instead of a passphrase you can use an age key file (`--key-file`).

### Add another machine

```bash
codeagent-sync join-code                 # on a machine that is set up
codeagent-sync init --join cas1-…        # on the new machine, same passphrase
```

The code carries the storage settings, encrypted with the bucket's passphrase. On a machine's first sync, files only it has are uploaded only when you confirm; files from the other machines are written with a backup.

### Every day

```bash
codeagent-sync sync        # download others' changes, upload this machine's
codeagent-sync status      # what a sync would do, changing nothing
codeagent-sync conflicts   # versions set aside; settle with: conflicts resolve <path> --keep local|remote
codeagent-sync undo        # take back a change; undo --list shows them all
```

`pull` and `push` sync one direction only. `sync`, `status`, `scan`, `conflicts`, `doctor`, `paths`, `machines` and `auto status` print JSON with `--json`.

### Sync automatically

```bash
codeagent-sync auto enable
```

Adds hooks to Claude Code and Codex: they sync in the background when a session starts and after every answer, and pass what you should hear about — a conflict, something new to share, a program another machine has — to the agent with your next message, once. The hooks are part of the synced settings, so they reach your other machines too. Codex runs new hooks only after you trust them: type `/hooks` in Codex once on each machine.

### Choose what syncs

```bash
codeagent-sync paths                                    # what syncs, and the rules
codeagent-sync paths include claude/plans/**            # sync more
codeagent-sync paths exclude claude/look-again/**       # sync less
codeagent-sync paths exclude claude-state/.claude.json  # not Claude Code's MCP servers
codeagent-sync paths exclude claude/settings.json#permissions --local
codeagent-sync paths reset claude/plans/**              # drop a rule
```

Rules are `<root>/<pattern>` (roots: `claude`, `codex`, `agents`, `claude-state`, `home`, `codeagent`). They live in `~/.codeagent-sync/sync.yaml`, which syncs so every machine follows them, and with `--local` in `sync.local.yaml`, for one machine. A path left out stops syncing but is deleted nowhere. After a `#`, an exclude names items of a settings file — `settings.json` keys, `config.toml` tables such as `mcp_servers.*`, `.claude.json` MCP servers — and each machine keeps its own version of them.

### Share between Claude Code and Codex

```bash
codeagent-sync scan                      # what only one tool has, and whether it works in the other
codeagent-sync share skill:foo           # share it
codeagent-sync unshare foo --to claude   # keep it with one tool again
codeagent-sync mark mcp:bar --codex-only # record a decision, change nothing
```

Shared skills live in `~/.agents/skills`, which Codex reads; Claude Code reaches each one through a link in `~/.claude/skills`. `scan` names the file and line of every reason: a skill using a Claude Code variable or tool stays with Claude Code; an MCP server is converted between the tools' formats where it can be. `--deep` asks the other tool's CLI for a second opinion, with no tools and no file access. Decisions live in `~/.codeagent-sync/registry.yaml`, which syncs, so a question answered on one machine is not asked again on another.

### Set up a machine like another

```bash
codeagent-sync machines
```

Shows how Claude Code, Codex and the programs MCP servers start are installed on each machine — versions and the command that installed each (native installer, npm, Homebrew, pipx, uv, pip) — and what this machine lacks, with the command to install it. Nothing is installed without you.

---

## What Is Synced

| Where | What |
|---|---|
| `~/.claude` | `CLAUDE.md`, `settings.json`, `agents/`, `commands/`, `hooks/`, `skills/`, `look-again/`, `statusline.sh`, the plugin lists |
| `~/.claude.json` | only `mcpServers` |
| `~/.codex` | `AGENTS.md`, `config.toml`, `hooks.json`, `agents/` |
| `~/.agents/skills` | the skills both tools read |
| `~/skills-lock.json` | the lock file of `npx skills` |
| `~/.codeagent-sync` | `registry.yaml` (sharing decisions), `sync.yaml` (rules), `machines/` (inventories) |

Never synced: sessions, history, credentials, caches, and what belongs to one machine — trusted projects, approved hooks, the rest of `.claude.json`. An MCP server whose command is not installed on a machine is held back there until it is.

---

## Troubleshooting

**Something does not sync, or you are not sure why** — run `codeagent-sync doctor`: it checks the setup, storage, key, conditional writes, conflicts, rules, hooks and what this machine lacks.

**"the credentials may not use the bucket (HTTP 403)"** — the API token is limited to other buckets, or the bucket does not exist and the token may not create it. Create the bucket and give the token read and write access to it.

**Codex does not sync automatically** — Codex skips hooks it has not been told to trust: type `/hooks` in Codex and trust the codeagent-sync hooks. `codeagent-sync auto status` shows whether it does.

**A skill works in one tool but not the other** — `codeagent-sync status --check` lists skills and agents a tool would ignore, such as a `SKILL.md` that is a symlink.

---

## Security

Every file is encrypted with age before upload; the key is derived from the passphrase with Argon2id and a random salt kept in the bucket, and a key check catches a wrong passphrase before anything is touched. The storage sees file names and sizes, not contents. Conditional writes keep two machines syncing at once from overwriting each other; deletions are recorded, not lost.

---

## Acknowledgements

Started as a fork of [claude-sync](https://github.com/tawanorg/claude-sync) (MIT); see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

## License

MIT — see [LICENSE](LICENSE).
