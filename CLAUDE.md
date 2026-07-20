# CLAUDE.md — OpinEd

Guidance for Claude Code (and humans) working in this repository. Read this
before touching code. It is deliberately opinionated; the project has a
settled architecture and a settled operating philosophy, and most "obvious
improvements" have already been considered and either shipped or cut by
decision. When in doubt, prefer the existing grain over a clever change.

---

## What OpinEd is

A WYSIWYG editor for GitHub Flavored Markdown, built for a single author
writing opinionated technical prose. Go backend, WebKitGTK front end via a
web view; the entire UI is one embedded HTML file served from an in-process
loopback HTTP server. Markdown parsing in, HTML-to-Markdown serialization
out. It runs on Linux desktops.

The defining principle: **the author lives above the raw source; the source
is transport.** The editor abstracts the author away from Markdown syntax.
Anything that leaks raw syntax into the authoring surface is a bug, not a
feature. This single sentence pre-answers a class of feature requests:

- "Should OpinEd show a diff / what changed?" — No. That is the VCS's job.
  Diffs are a *maintainer's* verification tool, run in a shell, never a UI
  feature.
- "Should there be a raw source view / edit mode?" — No. That is the
  abstraction leaking.
- "Should saves warn about normalization?" — No. Normalization is the
  serializer's prerogative (see Certified mode below).

The maintainer of the abstraction (you, in this repo) works *at* the source
layer. The author (the end user) never does. Keep that line bright.

---

## Certified mode (the operating model)

OpinEd is designed for **sole-author, OpinEd-only repositories**. Under this
model:

- **The serializer output IS the specification.** Whatever `htmlToMD`
  produces is by definition correct formatting. Do not add checks that
  second-guess the serializer's whitespace, blank-line, or wrapping choices;
  the save pipeline *is* the check for those. (This is why trailing
  whitespace and multiple-blank-line audits were considered and cut — the
  serializer already owns them.)
- **Foreign files are import material.** Opening a file authored elsewhere
  and saving it once is a *normalization* step, not editing. Treat it as an
  import ritual: normalize in one commit, edit in the next, so each diff
  answers for one thing.
- **Byte-preservation is guaranteed only for declared verbatim regions:**
  front matter, shortcode/component notation, and opaque paired-component
  spans. Everything else is body content the serializer may normalize.

If you find yourself wanting to preserve body bytes exactly, stop — that
fights certified mode. The answer is almost always "the serializer decides."

---

## Architecture

```
main.go        Go backend: HTTP server, web view lifecycle, file dialogs,
               Markdown<->HTML conversion, config/state, sidecar I/O,
               all native OS integration and goldmark rendering overrides.
editor.html    The entire front end: one file, embedded via go:embed.
               HTML + CSS + JS. Every UI behavior lives here. Editing it
               requires a rebuild to take effect (it is embedded, not read
               from disk at runtime).
assets/        Vendored runtime payloads (Harper WASM, Vale binary+styles),
               fetched by `make vendor-*`. Placeholder files are committed;
               real payloads are gitignored.
Makefile       Build orchestration. `make` builds; `make debug` builds with
               the web inspector enabled; `make vendor-harper` /
               `make vendor-vale` fetch runtime assets.
```

**Conversion pipeline (the heart of the app):**

- **Open (md -> HTML):** goldmark with the GFM extension, plus a custom
  renderer override that *escapes* raw HTML to visible text instead of
  goldmark's safe-mode default of *omitting* it. Verbatim regions (front
  matter, opaque components) are lifted out before parsing and restored
  after.
- **Save (HTML -> md):** JohannesKaufmann/html-to-markdown with the
  GitHubFlavored plugin. Verbatim regions swap back to their exact bytes.

**Key dependencies:** goldmark (+extension.GFM), html-to-markdown
(+plugin.GitHubFlavored), webview_go (WebKitGTK), zenity (native dialogs),
pelletier/go-toml/v2, yaml.v3.

---

## Non-negotiable invariants

Break these and something subtle will regress:

1. **Transient UI never mutates the document DOM.** Find highlights, lint
   marks, note cards, and badges are all *overlay* — absolutely positioned
   layers over the page, never nodes inserted into the editable content.
   The document DOM contains only document content. This is why notes,
   marks, and find survive each other without interference.

2. **Verbatim regions are lifted before parsing, restored after
   serializing.** Never let raw HTML or notation reach the Markdown parser
   or the serializer's normal path. The lift/restore token dance
   (`GFMSC%dTOKEN`, `GFMOPQ%dTOKEN`) is load-bearing.

3. **The checker/audit collectors exclude code and verbatim regions.**
   Prose checks run on flattened text with code elided and notation
   blanked; the `drops` intervals suppress findings that land on the
   surgical seams. When adding a check, respect these intervals or you will
   ship false positives on code boundaries.

4. **Config template is written once, never rewritten.** User config
   (`~/.config/OpinEd/config.toml`) is scaffolded on first run and then
   owned by the user. Machine state (`state.json`) is ours to rewrite.
   Never round-trip the user's config file.

5. **The styles directory is the user's authored data.** OpinEd reconciles
   the user's Vale styles into config each run but never deletes rules the
   user placed there. Retiring a shipped rule requires a manual `rm` on the
   user's side — no build or download does it for them.

---

## The verification discipline (critical — read this)

**There is no Go toolchain in the authoring environment where much of this
code was written.** Historically, correctness was established by structural
checks and mirror-testing, not by compiling. The habits that produced a
working codebase this way are worth keeping even now that you *can* compile:

- **Node syntax-check the embedded JS after every editor.html edit:**
  extract the `<script>` body and run `node --check`. A syntax error caught
  here costs seconds; shipped, it costs a session.
- **Brace-balance Go with string literals stripped.** Naive brace counting
  lies in files containing regex string literals (this codebase has many).
  Strip `"..."` and `` `...` `` first, then count. A raw count of "2" is
  usually two braces inside a regex, not a real imbalance.
- **Mirror-test regex and mapping logic in a scratch script** against real
  input (the `tests/fixtures/` documents are exactly the pathological
  inputs that caught past bugs). Prove the algorithm before wiring it in.
- **Validate Vale rules against the real Vale binary — line numbers AND
  column spans.** A rule can fire on the right line and still place its mark
  on the wrong character (see the span-by-substring quirk below). Checking
  only line numbers is how a shipped rule underlined the wrong word.
- **When editing by find-and-replace, match verbatim and refuse to write on
  mismatch.** The single most reliable way to corrupt this codebase is to
  patch text that has drifted from what you remember. If an anchor string
  is not found exactly, re-read the file rather than guessing — this caught
  real mispatches repeatedly.

Two failure modes recur and both are caught by the above, not by cleverness:
Python/shell escaping injuries when scripting edits, and anchor text that
has drifted between edits. Assume both will happen; let the checks catch
them.

---

## The quirks corpus

Hard-won, environment-specific knowledge that is not in anyone's training
data. Each of these cost real debugging. See `docs/QUIRKS.md` for the full
catalog with reproduction detail; the highest-value few:

- **goldmark safe mode omits raw HTML; we override to escape it.** The
  default silently *deletes* raw HTML on open (observed destroying a
  shortcode's table rows). Our renderer escapes to visible text instead.
- **goldmark `text.Segment.Value` has a pointer receiver; `At(i)` returns a
  value.** You must bind `seg := n.Lines().At(i)` before calling
  `seg.Value(source)` — the method cannot be called on the non-addressable
  temporary. Field access through a pointer (`n.ClosureLine.Value`) is fine.
- **Vale plaintext mode cannot match non-ASCII characters at all.** nbsp,
  curly quotes, em-dashes — unmatchable in every pattern form. This is why
  quote-consistency and the space+nbsp check are *native*, not Vale rules.
- **Vale locates multiline matches by document-order substring search of
  the matched TEXT, not by regex position.** A finding for a line ending
  "...devices" gets attributed to the *first* "devices" anywhere in the
  document. This is why trailing-punctuation is native (block-aware,
  computed ranges) and not a Vale rule.
- **Typing two spaces in contenteditable stores space + U+00A0 (nbsp),**
  serialized as bytes `20 C2 A0`. This is OpinEd's own fingerprint and is
  invisible to Vale (see above) — hence the native character-hygiene check.
- **WebKitGTK delivers no native Ctrl+Z accelerator.** The undo *command*
  works (`execCommand('undo')`); the keystroke never arrives. Interception
  in our keydown handler is the complete fix.
- **CSS specificity: `#editor pre` (id+element) outranks a bare `.class`.**
  Element-scoped id rules silently beat classes that "should" win; scope
  opaque-block rules as `#editor pre.opaque-sc` to win.
- **pkg-config shim required for the WebKitGTK build** — see the Makefile's
  `PKG_CONFIG_PATH`.

---

## Working conventions

- **HTML element IDs are stable contracts.** `insert-metadata` keeps its ID
  even after the label became "Metadata preamble". Rename labels freely;
  do not rename IDs that JS binds to.
- **Every new dialog follows the custody pattern:** OpinEd supplies the
  syntax/delimiters, the author supplies content, an unedited dialog is a
  strict byte no-op, clearing all content deletes the thing. Metadata, raw
  components, and notes all follow this. Match it.
- **Findings carry a severity and a kind.** Native checks tag their kind
  (`[Character]`, `[Style]`) parallel to Vale's `[OpinEd.*]`. Keep
  provenance visible in the status line.
- **Prefer cutting to building.** This project's best decisions were
  removals (review comments -> notes; opaque config -> universal paired
  detection; vocabulary decision -> free-form). When a feature feels
  heavy, look for the simplifying cut first.

---

## Build and run

```bash
make                 # build ./OpinEd
make debug           # build with GFM_DEBUG=1 (web inspector: right-click -> Inspect)
make vendor-harper   # fetch Harper WASM into assets/
make vendor-vale     # fetch Vale binary + Google/Microsoft/write-good styles
./OpinEd [file.md]   # run, optionally opening a file
```

The build needs the pkg-config shim on the path (Makefile sets
`PKG_CONFIG_PATH`). After any `editor.html` change you must rebuild — the
front end is embedded, not loaded from disk.

---

## Where things live at runtime

- `~/.config/OpinEd/config.toml` — user preferences (scaffolded once, then
  user-owned; never rewritten by us).
- `~/.config/OpinEd/state.json` — machine state: last path, theme (ours to
  rewrite).
- `~/.config/OpinEd/recovery.md` + `.json` — crash-recovery snapshot.
- `~/.cache/OpinEd/vale/` — extracted Vale binary and reconciled styles.
- `<document>-notes.md` — notes sidecar, beside the document, valid GFM.

---

## The ledger (open, non-blocking)

Carried into formal development; none block a first commit:

- **Report generator** — run all five audits on demand, present combined
  results, optionally export a GFM report. Feasibility done; sequencing:
  refactor Harper/Vale core-vs-presentation as a *separately verified* step
  first (it touches the most-debugged subsystems), then compose.
- **Silent-style-drop warning** — when a Vale style is listed in config but
  found in neither the vendored set nor the extra path, warn at audit time
  instead of silently dropping it. (A user diagnosed this by symptom once.)
- **Skills distillation** — ongoing; this file and `docs/QUIRKS.md` are its
  first products.

Cut by decision (do not resurrect without a strong reason): in-editor
diffs; raw source view; review-comment threading (replaced by notes);
byte-preserving body saves (contradicts certified mode); the multiple-blank
-lines audit (serializer owns it); QuoteConsistency as a Vale rule (Vale
can't see the characters).
