[![Go Reference](https://pkg.go.dev/badge/github.com/sametbrr/codeagent-sync.svg)](https://pkg.go.dev/github.com/sametbrr/codeagent-sync)
[![GitHub release](https://img.shields.io/github/v/release/sametbrr/codeagent-sync?display_name=tag&sort=semver)](https://github.com/sametbrr/codeagent-sync/releases/latest)
[![npm version](https://badge.fury.io/js/codeagent-sync.svg)](https://badge.fury.io/js/codeagent-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[Quick Start](#quick-start) • [Installation](#installation) • [Usage](#usage) • [Troubleshooting](#troubleshooting) • [What Is Synced](#what-is-synced) • [Security](#security)

# codeagent-sync

Keeps Claude Code and Codex configuration — instructions, settings, skills, agents, hooks, MCP servers and plugin lists — the same on every machine, end-to-end encrypted.

> 🇹🇷 Türkçe için [README.tr.md](README.tr.md)

**One setup for Claude Code and Codex on every machine.** Change a setting or add a skill or MCP server on one machine, and the others get it on their next sync — merged item by item, not overwritten. Everything is encrypted before it leaves the machine, so a storage bucket or WebDAV server is all the infrastructure you need.

---

## Quick Start

```bash
npm install -g codeagent-sync   # or: pnpm add -g codeagent-sync
codeagent-sync init             # storage, passphrase, first sync
```

On every other machine: `codeagent-sync join-code` on a machine that is set up, then `codeagent-sync init --join <code>` on the new one.

---

## Features

| | |
|---|---|
| **Both tools, one layout** | `~/.claude`, `~/.claude.json` (MCP servers only), `~/.codex` and the skills both tools read in `~/.agents/skills` |
| **End-to-end encrypted** | Every file is encrypted with [age](https://age-encryption.org) before it leaves the machine; the storage sees names and sizes only |
| **Merges, not overwrites** | Settings files merge item by item, so two machines changing different settings never conflict; comments in `config.toml` stay |
| **Portable paths** | Home-directory paths are translated, so machines with other user names or systems get their own |
| **Shares between tools** | Judges whether a skill or MCP server one tool has works in the other, and shares it when you agree |
| **Automatic** | Hooks sync in the background and tell the agent about conflicts and new things to share, once |
| **Your choice of what syncs** | Include and exclude rules for every machine or just one, down to a single setting |
| **Machine inventory** | Shows how the tools and MCP programs are installed elsewhere and what this machine lacks |
| **Safe** | Backups and `undo` for every change, conflicts set aside instead of overwritten, conditional writes against races |
| **Storage** | Cloudflare R2, Amazon S3 and S3-compatible services, Google Cloud Storage, WebDAV |

---

## Requirements

- macOS, Linux or Windows
- Claude Code and/or Codex (a tool that is not installed on a machine is left alone there)
- A bucket on Cloudflare R2, S3, GCS or a WebDAV server, with credentials that can read and write it
- Node.js 22 or later (LTS) to install with npm or pnpm (or a release binary, or Go 1.24 or later to build from source)

---

## Installation

```bash
npm install -g codeagent-sync
pnpm add -g codeagent-sync
```

The package installs the program for your system at `~/.local/bin/codeagent-sync` (on Windows `%USERPROFILE%\.local\bin`), checked against the release's checksums. pnpm skips install scripts by default; the first run installs it then. Keep it at the same place on every machine: the hooks of automatic sync start it from there. `codeagent-sync update` replaces it with the latest release.

When the package manager links its own launcher at that same path — npm with its global prefix at `~/.local`, pnpm with `PNPM_HOME` at `~/.local/bin` — the program goes to `~/.codeagent-sync/bin` instead, the launcher keeps `~/.local/bin/codeagent-sync`, and the hooks start the launcher. npm refuses to replace a program that is already there, so on such a machine move it aside once before installing with npm: `mkdir -p ~/.codeagent-sync/bin && mv ~/.local/bin/codeagent-sync ~/.codeagent-sync/bin/`.

<details>
<summary><strong>Release binaries and building from source</strong></summary>

Binaries for macOS, Linux and Windows are on the [releases](https://github.com/sametbrr/codeagent-sync/releases) page: rename the one for your system to `codeagent-sync` and put it in `~/.local/bin`.

To build from source (Go 1.24 or later):

```bash
git clone https://github.com/sametbrr/codeagent-sync && cd codeagent-sync
make install
```

</details>

### Uninstall

```bash
codeagent-sync auto disable       # remove the hooks; other machines follow on their next sync
npm uninstall -g codeagent-sync   # or: pnpm remove -g codeagent-sync
rm -f ~/.local/bin/codeagent-sync
rm -rf ~/.codeagent-sync          # local rules, decisions and setup
```

Your Claude Code and Codex files stay as they are. The encrypted copies stay in the bucket until you delete them there.

---

## Usage

```bash
# Setup
codeagent-sync init                       # storage, passphrase, first sync
codeagent-sync join-code                  # print a code that sets up another machine
codeagent-sync init --join <code>         # set up a new machine from that code
codeagent-sync auto enable                # sync automatically through hooks

# Daily use
codeagent-sync sync                       # download others' changes, upload this machine's
codeagent-sync pull                       # download only
codeagent-sync push                       # upload only
codeagent-sync status                     # what a sync would do, changing nothing
codeagent-sync conflicts                  # versions set aside because of conflicts
codeagent-sync undo                       # take back a change
codeagent-sync paths                      # what syncs, and the rules
codeagent-sync scan                       # what only one tool has
codeagent-sync share <name>               # share it with the other tool
codeagent-sync unshare <name> --to claude # keep it with one tool again
codeagent-sync mark <name> --codex-only   # record a decision, change nothing
codeagent-sync machines                   # how each machine is set up, what this one lacks

# Maintenance
codeagent-sync doctor                     # check setup, storage, key and hooks
codeagent-sync update                     # replace the program with the latest release
codeagent-sync auto disable               # remove the hooks
```

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

A project's own MCP servers are compared too: those in its `.mcp.json` (Claude Code) and in its `.codex/config.toml` (Codex, which reads that file only in projects it trusts). Sharing writes the server into the other file of the same project, and the decision is recorded for that project; name it as `mcp:<project root>/<server>` when the server name alone is ambiguous.

### Set up a machine like another

```bash
codeagent-sync machines
```

Shows how Claude Code, Codex and the programs MCP servers start are installed on each machine — versions and the command that installed each (native installer, npm, Homebrew, pipx, uv, pip) — and what this machine lacks, with the command to install it. Nothing is installed without you.

---

## Troubleshooting

**Something does not sync, or you are not sure why** — run `codeagent-sync doctor`: it checks the setup, storage, key, conditional writes, conflicts, rules, hooks and what this machine lacks.

**"the credentials may not use the bucket (HTTP 403)"** — the API token is limited to other buckets, or the bucket does not exist and the token may not create it. Create the bucket and give the token read and write access to it.

**Codex does not sync automatically** — Codex skips hooks it has not been told to trust: type `/hooks` in Codex and trust the codeagent-sync hooks. `codeagent-sync auto status` shows whether it does.

**A skill works in one tool but not the other** — `codeagent-sync status --check` lists skills and agents a tool would ignore, such as a `SKILL.md` that is a symlink.

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
| each project | `.mcp.json`, `CLAUDE.local.md`, `.claude/settings.json`, `.claude/settings.local.json`, `.claude/agents/`, `.claude/commands/`, `.claude/skills/`, `.codex/config.toml`, `.codex/agents/` — except the files git tracks |

Projects are the directories Claude Code and Codex list as projects, recognized by their git remote rather than their path, so a project's configuration reaches the same project wherever it is checked out on another machine. A repository is recognized by its `origin` URL (its https and ssh forms are the same), and a plain directory holding several repositories by the remotes of those repositories. Worktrees, repositories without a remote, and two directories that are the same project are not synced; `codeagent-sync paths` lists the projects found.

Never synced: sessions, history, credentials, caches, and what belongs to one machine — trusted projects, approved hooks, the rest of `.claude.json`. An MCP server whose command is not installed on a machine is held back there until it is.

---

## Security

Every file is encrypted with age before upload; the key is derived from the passphrase with Argon2id and a random salt kept in the bucket, and a key check catches a wrong passphrase before anything is touched. The storage sees file names and sizes, not contents. Conditional writes keep two machines syncing at once from overwriting each other; deletions are recorded, not lost.

---

## Acknowledgements

Started as a fork of [claude-sync](https://github.com/tawanorg/claude-sync) (MIT); see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

## Contributing

- Questions and ideas: [Discussions](https://github.com/sametbrr/codeagent-sync/discussions)
- Bugs: [issue tracker](https://github.com/sametbrr/codeagent-sync/issues)
- Pull requests: read the [contribution guide](https://github.com/sametbrr/.github/blob/main/CONTRIBUTING.md) first
- Security reports: follow the [security policy](https://github.com/sametbrr/codeagent-sync/security/policy), not a public issue

---

## License

MIT — see [LICENSE](LICENSE).

---

<div align="center">

[Report Bug](https://github.com/sametbrr/codeagent-sync/issues) · [Request Feature](https://github.com/sametbrr/codeagent-sync/issues)

</div>
