---
name: webkitgtk-quirks
description: >
  Environment-specific quirks of the WebKitGTK web view, contenteditable
  behavior, CSS specificity traps, overlay UI, and the pkg-config build shim
  in OpinEd. Consult whenever touching editor.html's CSS or DOM behavior,
  the keydown handler, overlay layers (find marks, lint marks, note cards),
  contenteditable editing operations, or the WebKitGTK build. These are
  non-obvious, cost real debugging, and are not in training data.
---

# WebKitGTK & front-end quirks

OpinEd's UI is a single `editor.html` running in a WebKitGTK web view. That
runtime and its contenteditable surface have several behaviors that differ
from a modern browser or from intuition. Every item here was learned by
debugging, not by reading docs.

## Keyboard: no native undo accelerator

WebKitGTK in this embedding delivers **no working `Ctrl+Z`/`Ctrl+Y`
accelerator** — not on the page, not even in text fields. The undo
*machinery* is fine: `document.execCommand('undo')` from the console reverts
edits, and `document.queryCommandEnabled('undo')` reports stack state
correctly. Only keystroke *delivery* is dead.

**Fix pattern:** intercept the chords in the global keydown handler and call
`execCommand`. Route to whatever is focused (a text field keeps its own
stack; do not steal focus to the editor). Gate `markDirty()` on
`queryCommandEnabled` so an empty stack is a true no-op and does not
false-dirty a clean document. Bind `Ctrl+Y` and `Ctrl+Shift+Z` both for
redo.

**Diagnostic recipe** (if undo ever misbehaves again): in `make debug`
console, `queryCommandEnabled('undo')` then `execCommand('undo')`. If the
console command works but the keystroke does not, it is delivery, and
interception is the whole fix. If the console command also fails, the native
stack itself was poisoned — suspect a direct-DOM mutation adjacent to the
edit (table ops, code-block text nodes) that bypassed the stack.

## CSS specificity: id+element beats a bare class

`#editor pre { ... }` has specificity (1,0,1) and **silently outranks**
`.opaque-sc { ... }` at (0,1,0). A class that "obviously" should style an
element loses to an id-scoped element rule. Symptom: your padding/margins
are ignored and you cannot see why.

**Fix pattern:** scope the winning rule with the same id, e.g.
`#editor pre.opaque-sc` (1,1,1). When an element inherits an id-scoped base
rule (any `<pre>` inside `#editor`), your override must out-specify it, not
merely target the class.

This is the same family as an early `[hidden]`-vs-`display:flex` bug: an
id-scoped base rule quietly outranking the class you expected to win.

## Overlays never touch the document DOM

Find highlights, lint marks, note cards, and badges are **absolutely
positioned overlay layers**, not nodes inserted into the editable content.
This is a hard invariant:

- The document DOM contains only document content. Nothing transient.
- Overlays are positioned by measuring element rects (`getBoundingClientRect`)
  against a container rect, then placing layers.
- The overlay container has `pointer-events: none`; individual interactive
  items (note cards, badges) re-enable `pointer-events: auto`.
- Overlays repaint on the established triggers: debounced `input`, `resize`,
  and after load. If you add content that shifts layout, make sure the
  relevant overlay repaints.

Why it matters: overlays coexist (marks + notes + find simultaneously)
precisely because none of them mutate the content the others read. Insert a
mark as a real DOM node and you will corrupt offset math elsewhere.

## Overlay geometry: measure after layout changes, not before

When an overlay's position depends on a layout the same code just changed
(e.g. shifting the page left to open the notes margin), apply the layout
change **first**, then measure. Two-pass is the pattern: place elements, let
layout settle, then read real heights for collision stacking. Measuring
against the pre-change layout puts things in the wrong place — this caused
note cards to render off-screen to the right.

Anchor overlays to the correct reference element. Cards anchored to the
full-width workspace rect land far right; anchor to the *editor sheet's*
measured right edge instead. "Wrong reference rect" is the first hypothesis
for any "renders but in the wrong place" overlay bug.

## contenteditable: form controls do not inherit color

Buttons and `<select>` do not inherit text color; they must be explicitly
owned in CSS (background + color both). A `<select>` needs
`appearance: none` plus a custom chevron to theme correctly in dark mode.
Symptom: invisible or system-colored controls after a theme change.

## contenteditable: blockquote is an indentation container

`formatBlock('p')` inside a `<blockquote>` is a **silent no-op** — WebKit
treats blockquote as indentation, and the content already is a paragraph.
To escape a blockquote, use `execCommand('outdent')`, not formatBlock.

Relatedly, the block-type dropdown must give **container blocks precedence
over the leaf blocks they wrap**: goldmark renders `> text` as
`<blockquote><p>...</p></blockquote>`; a nearest-ancestor match reports the
inner `<p>`. Check for an enclosing `pre`/`blockquote` first, fall back to
leaf blocks. `pre` outranks `blockquote` (code quoted in a blockquote edits
as code).

## contenteditable: two spaces become space + nbsp

Typing two spaces stores space + `U+00A0`, not two spaces (HTML would
collapse plain doubles). Serialized as bytes `20 C2 A0`. Consequences:
find-by-two-spaces misses them; Vale cannot see them (non-ASCII blindness);
they need the native character-hygiene check. A single deliberate nbsp
(`10 GB`) must NOT be flagged — only runs of 2+ space-class characters.

## Code blocks need explicit caret and newline handling

Code blocks are one `<pre><code>` with literal `\n`. Enter is intercepted
to insert a raw text-node newline; caret affinity at boundaries needs
`normalize()` plus an explicit offset set, and a sentinel trailing `\n`.
Double-Enter exits an empty block. These direct-DOM operations are outside
the native undo stack by design (documented boundary).

## The build: pkg-config shim

The WebKitGTK build requires `PKG_CONFIG_PATH` pointing at a shim (set in
the Makefile). Without it, cgo cannot find the WebKit pkg-config files and
the build fails at link. `make debug` additionally sets `GFM_DEBUG=1` to
enable the web inspector (right-click -> Inspect Element) — the primary
tool for diagnosing everything on this page.

## Editing editor.html requires a rebuild

The front end is embedded via `go:embed`, not read from disk at runtime.
Any change to `editor.html` (or assets) needs `make` to take effect. There
is no live reload.
