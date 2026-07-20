# QUIRKS.md — the hard-won corpus

Environment-specific and library-specific behaviors that cost real debugging
and are not in any training data. Each entry: the symptom, the cause, the
fix. When you burn a day on something subtle, add it here.

---

## goldmark

### Safe mode omits raw HTML (we override to escape)

**Symptom:** raw HTML in an opened file vanished — a shortcode's `<tr>`/`<td>`
rows were *deleted*, leaving the cell text concatenated into one unreadable
line.

**Cause:** goldmark's safe-by-default mode does not escape raw HTML, it
*omits* it. Indented HTML lines lazily continued a paragraph and their tags
were dropped as unsafe inline HTML; only the between-tag text survived.

**Fix:** a custom renderer (`rawHTMLEscaper`) overrides `ast.KindHTMLBlock`
and `ast.KindRawHTML` to entity-escape raw HTML to visible, editable text.
goldmark stays safe; nothing renders; nothing is silently deleted. Declared
verbatim regions are lifted before parsing and never reach this path — the
escaper is the backstop for stray/undeclared HTML.

### `text.Segment.Value` pointer receiver on non-addressable values

**Symptom:** `cannot call pointer method Value on text.Segment` at
`n.Lines().At(i).Value(source)` — but `n.ClosureLine.Value(source)` nearby
compiled fine.

**Cause:** `Value` has a pointer receiver; `At(i)` returns a *value* (a
non-addressable temporary), so Go cannot take its address. A struct field
accessed through a pointer *is* addressable, which is why `ClosureLine`
worked.

**Fix:** bind first — `seg := n.Lines().At(i)` then `seg.Value(source)`. A
named local is addressable. Applies to both `Lines().At` and `Segments.At`.

---

## Vale (plaintext mode)

### Cannot match non-ASCII characters — at all

**Symptom:** rules targeting nbsp, curly quotes, or em-dashes load without
error and never fire.

**Cause (established by exhaustive probing of the real binary):** Vale's
plaintext ingestion mangles non-ASCII characters into a form no pattern can
reach. `\p{Zs}`, `\x{00A0}`, literal characters, even adjacency probes
testing whether the char was deleted — all fail. Curly quotes and em-dashes
share the black hole.

**Consequence:** any such rule is silently dead forever. Quote-consistency,
the space+nbsp check, and smart-quotes-in-code are therefore **native**, not
Vale rules. Documented in `vale-styles/OpinEd/README.md`.

### Locates multiline matches by substring search, not regex position

**Symptom:** a trailing-punctuation finding for a *list item* ending
"...devices" placed its purple mark on the word "devices" in the **first
sentence** of the document. Three `template` findings scattered across the
1st/2nd/3rd occurrences of that word.

**Cause:** Vale reports a finding's location by searching the line/document
in reading order for the **matched substring text**, not by the regex's
actual position. Common final words collide.

**Fix:** the check went native (block-aware, DOM-computed ranges). A prior
partial mitigation — matching the whole final word instead of one char —
reduced collisions but did not eliminate them.

### No block-type awareness

**Symptom:** trailing-punctuation flagged H2 headings and list items.

**Cause:** flattened plaintext has no block types.

**Fix:** native check iterates `<p>` elements, giving heading/list-item
exemption by construction.

### Validate rules on SPAN, not just line

**Symptom:** a rule fired on the correct line but underlined the wrong
character (column 3 instead of the line end).

**Cause:** single-character matches located by substring search (see above).

**Lesson:** when validating any Vale rule against the real binary, assert
column spans too, not only line numbers. This is a permanent testing rule.

### Styles are directories; the subdirectory name is load-bearing

**Symptom:** a configured `OpinEd` style produced no findings, silently.

**Cause:** rules must live in `<extra_styles_path>/OpinEd/*.yml`. If placed
directly in `<extra_styles_path>/`, no style named `OpinEd` is found, and it
is dropped from `BasedOnStyles` **without an error**.

**Fix:** ensure the `OpinEd/` subdirectory. (Ledger item: warn on
configured-but-unfound styles instead of dropping silently.)

### Retiring a shipped rule needs a manual delete

The styles directory is the user's authored data. OpinEd reconciles it into
config each run but never deletes rules. A retired `.yml` keeps firing until
the user removes it by hand — no build or download does it.

---

## WebKitGTK / contenteditable

### No native undo accelerator

**Symptom:** Ctrl+Z does nothing, anywhere — page or text field.

**Cause:** this embedding delivers no undo keyboard accelerator. The command
machinery is fine (`execCommand('undo')` works from the console).

**Fix:** intercept Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z in the keydown handler and
call `execCommand`, routed to the focused editable, gating `markDirty()` on
`queryCommandEnabled`.

### CSS: id+element beats a bare class

**Symptom:** `.opaque-sc` padding ignored; label overlapped content.

**Cause:** `#editor pre` (1,0,1) outranks `.opaque-sc` (0,1,0). Any `<pre>`
in `#editor` inherits the code-block rule's weight.

**Fix:** scope as `#editor pre.opaque-sc` (1,1,1). Same family as an early
`[hidden]` vs `display:flex` bug.

### formatBlock('p') inside blockquote is a no-op

**Cause:** WebKit treats blockquote as an indentation container.

**Fix:** use `execCommand('outdent')` to escape a blockquote.

### Block dropdown must prefer containers over wrapped leaves

**Symptom:** caret in a blockquote reported "Paragraph".

**Cause:** `> text` renders as `<blockquote><p>...</p></blockquote>`;
nearest-ancestor match found the inner `<p>`.

**Fix:** check enclosing `pre`/`blockquote` first (pre outranks blockquote),
fall back to leaf blocks.

### Two spaces -> space + nbsp

Typing two spaces stores space + `U+00A0` (bytes `20 C2 A0`); HTML would
collapse plain doubles. Invisible to find-by-two-spaces and to Vale. Native
character-hygiene check catches runs of 2+ space-class chars; a lone
deliberate nbsp (`10 GB`) is not flagged.

### Form controls do not inherit color

Buttons and `<select>` need explicit background + color; `<select>` needs
`appearance: none` + custom chevron to theme in dark mode.

### Overlay geometry: measure after layout change; anchor to the right rect

Note cards rendered off-screen right because they were anchored to the
full-width workspace rect and measured before the page-shift that opens the
margin. Fix: apply the layout class first, then measure against the *editor
sheet's* right edge. "Renders but wrong place" -> suspect the reference rect.

---

## Build / environment

### pkg-config shim

The WebKitGTK cgo build needs `PKG_CONFIG_PATH` pointing at a shim (Makefile
sets it). Without it, linking fails.

### editor.html is embedded

`go:embed` — front-end changes need a rebuild; no live reload.

---

## Scripting the codebase (for automated edits)

### Brace counting lies without stripping strings

Regex string literals contain intentional unmatched braces. Strip `"..."`
and `` `...` `` before counting, or a balanced file reads as imbalanced.

### Anchor text drifts between edits

Find-and-replace anchors that matched last week may not match now. Match
verbatim; refuse to write on mismatch; re-read and re-derive rather than
guessing. Aborts here are the safety net, not obstacles — they have caught
real mispatches repeatedly.

### Escaping injuries when generating code via scripts

Generating Go/JS through Python/shell heredocs injures escapes (backslashes,
quotes, newlines). Always syntax-check the result (Node for JS, brace-
balance for Go) before trusting it.

---

## Download / transport artifacts

Bulk downloads from the authoring environment can extract with full
container paths, recreating `mnt/user-data/outputs/...` subtrees or a
`files/` folder of duplicate `main.go`/`editor.html`. These are not source.
Gitignored; verify with `diff` against the top-level copies and delete. The
risk is canonicality confusion — building one copy while editing another.
