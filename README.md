# codeagent-sync

Keep the configuration of your coding agents — Claude Code and Codex — the
same on every machine: instructions, settings, skills, agents, hooks, MCP
servers and plugin lists. Everything is encrypted with
[age](https://age-encryption.org) before it leaves the machine.

It also looks after what the two tools could share: a skill or an MCP server
you add to one of them is judged for the other, and moved into the shared
layout when you agree.

Sessions and history are not synced.

## What is synced

| Where | What |
|---|---|
| `~/.claude` | `CLAUDE.md`, `settings.json`, `agents/`, `commands/`, `hooks/`, `skills/`, `look-again/`, `statusline.sh`, the plugin lists |
| `~/.claude.json` | only `mcpServers` |
| `~/.codex` | `AGENTS.md`, `config.toml`, `hooks.json`, `agents/` |
| `~/.agents/skills` | skills both tools read |
| `~/skills-lock.json` | the lock file of `npx skills` |
| `~/.codeagent-sync/registry.yaml` | the decisions about what is shared |

Never synced: sessions, history, credentials, caches, and what belongs to one
machine — trusted projects, approved hooks, security approvals, the rest of
`.claude.json`. A tool that is not installed on a machine is left alone
there.

Settings files merge item by item: `settings.json` per setting, `.claude.json`
per MCP server, `config.toml` per setting or table. Two machines changing
different settings never conflict, and comments in `config.toml` stay. Paths
under your home directory are written portably, so machines with different
user names (or systems) get their own paths. An MCP server whose command is
not installed on a machine is held back there until it is.

### Choosing what syncs

```bash
codeagent-sync paths                                   # what syncs, and the rules
codeagent-sync paths include claude/plans/**           # sync more
codeagent-sync paths exclude claude/look-again/**      # sync less
codeagent-sync paths exclude claude-state/.claude.json # not Claude Code's MCP servers
codeagent-sync paths exclude claude/settings.json#permissions --local
codeagent-sync paths reset claude/plans/**             # drop a rule
```

Rules are `<root>/<pattern>` (roots: `claude`, `codex`, `agents`,
`claude-state`, `home`, `codeagent`). They live in
`~/.codeagent-sync/sync.yaml`, which syncs so every machine follows them, and
with `--local` in `sync.local.yaml`, for one machine. A path left out stops
syncing but is deleted nowhere. After a `#`, an exclude names items of a
settings file (`settings.json` keys, `config.toml` tables such as
`mcp_servers.*`, `.claude.json` MCP servers): each machine keeps its own
version of them. Credentials, sessions and history can never be included.

## Install

With npm or pnpm (Node 18 or later):

```bash
npm install -g codeagent-sync
pnpm add -g codeagent-sync
```

The package installs the program for your system at
`~/.local/bin/codeagent-sync` (on Windows `%USERPROFILE%\.local\bin`),
checked against the release's checksums. pnpm skips install scripts by
default; the first run installs it then.

Or download a binary from the
[releases](https://github.com/sametbrr/codeagent-sync/releases), rename it to
`codeagent-sync` and put it in `~/.local/bin`. Or build it (Go 1.24 or later):

```bash
make install      # builds ~/.local/bin/codeagent-sync
```

Keep it at the same place on every machine: the hooks of automatic sync
start it from there. `codeagent-sync update` replaces it with the latest
release.

## First machine

```bash
codeagent-sync init
```

`init` asks for the storage — Cloudflare R2, Amazon S3 or an S3-compatible
service, Google Cloud Storage, or WebDAV — and a passphrase, creates the
bucket if needed, and runs the first sync. It shows what it will upload
before it does.

Instead of a passphrase you can use an age key file (`--key-file`).

## Other machines

Either run `init` with the same storage settings and passphrase, or print a
join code on a machine that is set up:

```bash
codeagent-sync join-code                 # on a machine that is set up
codeagent-sync init --join cas1-…        # on the new machine
```

The code carries the storage settings, encrypted with the bucket's
passphrase.

On a machine's first sync, files that exist only there are uploaded only
when you confirm; files from the other machines are written with a backup.

## Every day

```bash
codeagent-sync sync       # download others' changes, upload this machine's
codeagent-sync status     # what a sync would do
codeagent-sync undo       # restore what the last sync (or share) changed here
codeagent-sync conflicts  # versions set aside; resolve with --keep local|remote
```

`pull` and `push` sync one direction only. `sync`, `status`, `scan`,
`conflicts`, `doctor` and `auto status` print JSON with `--json`.

### Automatically

```bash
codeagent-sync auto enable
```

adds hooks to Claude Code and Codex: they sync in the background when a
session starts and after every answer, and pass what you should hear about —
a conflict, something new that could be shared — to the agent with your
next message, once. The hooks are part of the synced settings, so they reach
your other machines too.

Codex runs new hooks only after you trust them: type `/hooks` in Codex and
trust the codeagent-sync hooks, once on each machine. `auto status` shows
whether it does.

## Sharing between Claude Code and Codex

Skills live in `~/.agents/skills`, which Codex reads; Claude Code reaches each
shared skill through a link in `~/.claude/skills`.

```bash
codeagent-sync scan                  # what only one tool has, and whether it works in the other
codeagent-sync share skill:foo       # share it
codeagent-sync unshare foo --to claude
codeagent-sync mark mcp:bar --codex-only
```

`scan` judges by what the files say, and names the file and line of every
reason: a skill using a Claude Code variable or tool stays with Claude Code;
an MCP server converts between the tools' formats where it can. What depends
on one tool is recorded as such, so it is not asked about again. `--deep`
asks the other tool's CLI for a second opinion on each skill, read-only.

Decisions live in `~/.codeagent-sync/registry.yaml`, which syncs, so a
question answered on one machine is not asked again on another.

## Setting up another machine like this one

```bash
codeagent-sync machines
```

shows, for every machine, how Claude Code, Codex and the programs MCP
servers start are installed — versions and the command that installed each
(native installer, npm, Homebrew, pipx, uv, pip) — and what this machine
lacks, with the command to install it. Each machine keeps its own file in
`~/.codeagent-sync/machines/`, which syncs; syncs refresh it twice a day.
`init` and `doctor` show what is missing too, and with automatic sync the
agent mentions it once. Nothing is installed without you.

## When something is wrong

```bash
codeagent-sync doctor          # setup, storage, key, hooks, layout
codeagent-sync status --check  # skills and agents the tools would ignore
```

## Platforms

macOS, Linux and Windows. Where Windows allows no symlinks, a shared skill
becomes a directory junction, or else a copy that codeagent-sync keeps up to
date.

## Security

Every file is encrypted with age before upload; the key is derived from the
passphrase with Argon2id and a random salt kept in the bucket. The storage
sees file names and sizes, not contents. Conditional writes keep two
machines syncing at once from overwriting each other.

## License

MIT — see [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
