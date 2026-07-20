# Setting up Claude Code + GitHub for OpinEd

A one-time guide to move OpinEd into formal development. Verified against
Anthropic's setup docs as of July 2026 (`code.claude.com/docs/en/setup`).
Commands may drift; when in doubt, read the current docs before copying.

---

## 1. Update (or install) Claude Code

Anthropic now ships Claude Code as a **native binary**, and the native
installer is the recommended method — zero dependencies (no Node.js), and it
**auto-updates in the background**. Since you already have an older Claude
Code, the cleanest path is to move to the native install.

### If your current install is npm-based (likely, if it's old)

Older Claude Code ran on Node via npm. Migrate to native from inside a
running session:

```bash
# inside a running Claude Code session:
/migrate-installer
```

or from the shell:

```bash
claude install
```

Then **remove the old npm copy** so it doesn't shadow the native binary on
your PATH (the most common post-migration snag):

```bash
npm uninstall -g @anthropic-ai/claude-code
hash -r                     # bash: forget the cached path (zsh: rehash)
which claude                # should now print ~/.local/bin/claude
claude doctor               # should report install type: native
```

### Fresh native install (macOS, Linux, WSL)

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

(Windows PowerShell: `irm https://claude.ai/install.ps1 | iex`.)

### If you prefer to stay on npm

Still supported but deprecated, and now requires **Node.js 22+**
(as of v2.1.198). Upgrade with `@latest` — *not* `npm update -g`, which
respects the original semver range and may not move you forward:

```bash
npm install -g @anthropic-ai/claude-code@latest
```

Never use `sudo npm install -g` (permission and security problems). If you
hit permission errors, configure a user-level npm prefix instead.

### Verify

```bash
claude --version    # prints e.g. 2.1.211 (Claude Code)
claude doctor       # read-only diagnostics: install health, settings, PATH
```

`claude doctor` is your friend — it validates settings files and flags PATH
conflicts without starting a session.

### Authentication

Claude Code needs a **Pro, Max, Team, Enterprise, or Console** account (the
free Claude.ai plan does not include Code access). Run `claude` and follow
the browser prompt. On a server/CI, set `ANTHROPIC_API_KEY` instead.

---

## 2. Put OpinEd under Git and push to GitHub

From the project root (the directory containing `main.go`, `editor.html`,
`Makefile`, `CLAUDE.md`):

```bash
git init
git add .
git status              # confirm no debris: no files/, no mnt/, no ./OpinEd binary
git commit -m "Initial commit: OpinEd v0.01 (pristine handoff state)"
```

The included `.gitignore` already excludes the build output, vendored
payloads, transport debris (`files/`, `mnt/`), and local Claude state.
**Verify `git status` is clean of those before the first commit** — the
whole point of committing the pristine state first is that all subsequent
polish lands as reviewable diffs (the same one-change-per-diff discipline as
certified-mode imports).

Create the GitHub repo and push (using the GitHub CLI, if installed):

```bash
gh repo create opined --private --source=. --remote=origin --push
```

Or manually: create an empty repo on github.com, then

```bash
git remote add origin git@github.com:<you>/opined.git
git branch -M main
git push -u origin main
```

---

## 3. What Claude Code will read in this repo

The handoff package is already wired for Claude Code's discovery:

- **`CLAUDE.md`** (repo root) — loaded automatically as project context.
  The constitution: architecture, invariants, certified mode, the ledger,
  and the verification discipline.
- **`.claude/skills/*/SKILL.md`** — four skills Claude Code surfaces by
  their descriptions when relevant:
  - `webkitgtk-quirks` — web view, CSS, contenteditable, overlays, build.
  - `gfm-roundtrip` — the conversion pipeline and byte-fidelity testing.
  - `house-style-checks` — checkers, audits, Vale-vs-native decision rule.
  - `build-and-verify` — make targets and the verification habits.
- **`docs/QUIRKS.md`** — the full quirks catalog (CLAUDE.md links to it).

You don't need to do anything to activate these beyond having them in the
repo; Claude Code reads `CLAUDE.md` on session start and consults skills by
description. Keep them current — when a decision or quirk changes, update the
relevant file in the same change.

---

## 4. First session suggestions

- Start with `claude` in the project root and ask it to summarize its
  understanding of the architecture from `CLAUDE.md` — a quick sanity check
  that context loaded.
- Before the first code change, run a build to confirm the toolchain:
  `make && ./OpinEd`. (Run `make vendor-harper && make vendor-vale` first if
  `assets/` only has placeholders — a fresh clone will.)
- Consider setting up project permissions (`.claude/settings.json`) to
  scope what the agent may do before giving broad write access. Add the file
  and commit it so the whole project shares it; `settings.local.json` (for
  personal overrides) is gitignored.
- Point Claude Code at `docs/CLAUDE_CODE_SETUP.md` (this file) if you want
  it to help finish any of the above.

---

## 5. Recommended early tasks (from the ledger)

None block development, but these are the natural first formal-repo tasks,
in rough priority:

1. **Vendor + build verification** — confirm a clean clone builds and runs
   end to end; capture any missing-asset or PATH gotchas in QUIRKS.md.
2. **Silent-style-drop warning** — small, self-contained, high value: warn
   when a configured Vale style isn't found instead of dropping it silently.
3. **Report generator** — the one real feature left. Do the Harper/Vale
   core-vs-presentation refactor as a *separately committed, separately
   verified* step first (it touches the most-debugged subsystems), then
   compose the five audits into an exportable GFM report.

See the ledger in `CLAUDE.md` for the full list and the cut-by-decision
items (don't resurrect those without a strong reason).
