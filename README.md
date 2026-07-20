# OpinEd

OpinEd (formerly gfm-editor) is a local WYSIWYG editor for GitHub Flavored Markdown, written in Go, with a UI
loosely modeled on Oxygen's Author mode. Files are read from and saved to
plain GFM; HTML exists only in memory as the editing representation.

## Supported GFM constructs

Full CommonMark core plus the four GFM extensions:

| Construct | Edit UI | Notes |
|---|---|---|
| Headings H1–H6 | dropdown, Ctrl+1..6 | |
| Paragraph | dropdown, Ctrl+0 | also escapes blockquote/code block |
| Bold / Italic | toolbar, Ctrl+B / Ctrl+I | |
| Strikethrough (GFM) | toolbar, Ctrl+Shift+X | |
| Inline code | toolbar, Ctrl+E | toggles wrap/unwrap |
| Fenced code blocks | toolbar `{ }` | optional language tag |
| Blockquotes | toolbar / dropdown | |
| Ordered / unordered lists | toolbar | nest with Tab / Shift+Tab |
| Task lists (GFM) | toolbar ☑ | checkboxes are clickable |
| Tables (GFM) | Insert + row/col add/delete | row/col buttons enable when caret is in a table |
| Links | toolbar, Ctrl+K | applies to selection, or inserts URL |
| Autolinks (GFM) | — | bare URLs become links when the file is (re)opened |
| Images | toolbar | URL + alt text |
| Horizontal rules | toolbar — | |
| Hard line breaks | Shift+Enter | |

## Architecture

- `main.go` — window, native dialogs, file I/O, and the conversion pipeline:
  **open** = goldmark (GFM extension) parses Markdown → HTML;
  **save** = html-to-markdown (GitHubFlavored plugin) serializes HTML → Markdown.
- `editor.html` — the editing surface (contenteditable in embedded WebKitGTK),
  compiled into the binary via `go:embed`.

## Prerequisites (Linux)

- Go 1.21 or newer
- GCC / pkg-config (cgo is required by the webview bindings)
- GTK 3 and WebKit2GTK development headers
- The `zenity` binary (used for native file dialogs)

Debian / Ubuntu:

    sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev zenity

Note: webview_go's cgo line pins `webkit2gtk-4.0`. On Ubuntu 24.04+/Debian 13,
which ship only 4.1, create a pkg-config alias (APIs are identical):

    mkdir -p ~/pkgconfig-shim
    PC=/usr/lib/x86_64-linux-gnu/pkgconfig
    cp "$PC/webkit2gtk-4.1.pc"        ~/pkgconfig-shim/webkit2gtk-4.0.pc
    cp "$PC/javascriptcoregtk-4.1.pc" ~/pkgconfig-shim/javascriptcoregtk-4.0.pc

and prefix builds with `PKG_CONFIG_PATH=~/pkgconfig-shim`.

Rocky / Fedora / RHEL

    sudo dnf install webkit2gtk4.1-devel gtk3-devel pkgconf-pkg-config gcc make

    mkdir -p ~/pkgconfig-shim
    PC=/usr/lib64/pkgconfig
    cp "$PC/webkit2gtk-4.1.pc"        ~/pkgconfig-shim/webkit2gtk-4.0.pc
    cp "$PC/javascriptcoregtk-4.1.pc" ~/pkgconfig-shim/javascriptcoregtk-4.0.pc

## Certified mode of operation

OpinEd's certified mode is the **sole-author, OpinEd-only repository**:
files are authored in OpinEd and edited only in OpinEd. Under this model,
saves are deterministic and git diffs are minimal -- a one-word edit is a
one-word diff.

Editing existing Markdown files from elsewhere is not a supported mode.
Foreign files are **import material**: the author first transforms them
into OpinEd form by opening and saving them once (the serializer's
normalization -- ATX headings, `-` bullets, unified emphasis markers,
inline links -- IS the transformation), adapting by hand whatever the
pipeline does not carry, as they are able. Committing that import
separately from content edits keeps history and blame honest. From then
on the file is OpinEd-native.

Two regions are exempt from normalization by design, because they are
other systems' syntax that OpinEd transports without owning: YAML front
matter and configured SSG notation (shortcodes/variables) are preserved
byte-for-byte.

## Renaming note (v0.01, gfm-editor -> OpinEd)

Per-user directories migrate automatically: on first launch, OpinEd
renames ~/.config/gfm-editor to ~/.config/OpinEd (config, state, recovery
carry over) and likewise under ~/.cache. Manual steps for the project
itself: rename the source directory (mv gfm-editor OpinEd), optionally
`go mod edit -module=OpinEd` (cosmetic; the build works either way), and
remove a previously installed old binary (rm -f ~/.local/bin/gfm-editor)
before `make install`.

## Build

    cd OpinEd
    go mod init OpinEd    # first time only
    go mod tidy
    PKG_CONFIG_PATH=~/pkgconfig-shim go build -o OpinEd .

## Run

    ./OpinEd              # start with an empty document
    ./OpinEd notes.md     # open an existing GFM file

## Editor preferences

config.toml's [editor] section holds `code_font_size` -- code block text
size in integer pixels (default 11, accepted range 8-24; out-of-range or
fractional values fall back to the default). Note that some Linux systems
render the monospace face with synthetically heavy stems at 14px and
above. Existing config files gain the setting by adding the section by
hand; the template written for new installs includes it.

The [editor] section also holds `audience` ("technical", the default, or
"general"), calibrating the SEO audit's Flesch bands: technical treats
40-59 as acceptable for domain prose and draws its diagnostic finding
below 40; general uses the conventional bands and draws it below 60.
Re-read on each audit run, so edits apply without restarting.

At startup the window shrinks to fit the screen when the default size
(980x1200) would run off the display (margin is left for panels and
decorations; the window never grows beyond the default and never shrinks
below 600x400).

## Light and dark modes

The sun/moon toolbar button (left of reload) toggles the theme. Until first
toggled, the app follows the desktop preference (prefers-color-scheme) live;
an explicit choice persists across runs. The dark palette uses tufte-css's
dark scheme for the page (dark parchment #151515 / #ddd, matching the
off-white philosophy) and GitHub's dark colors for GitHub-styled elements
(links, tables); overlay marks (find, spelling/grammar, Vale, audit pulses)
have their own dark tuning for contrast over light text. Known seam: the
workspace scrollbar is GTK's and follows the desktop theme, not the app's.

Machine state now lives in ~/.config/OpinEd/state.json (theme, last
file location); the legacy one-line "lastdir" file is migrated in and
removed on first run. config.toml remains user-owned and is never written
by the app after the initial template.

File > Exit quits OpinEd, with the same unsaved-changes gate as New and
Open (Save / Don't Save / Cancel; an explicit Don't Save clears the
crash-recovery snapshot, so no stale restore offer appears at next
launch).

## Front matter (document metadata)

Leading YAML front matter (a first line of exactly `---`, ended by `---` or
`...`) is held aside verbatim on open and re-prepended byte-for-byte on save
-- the one region of the file with a byte-preservation guarantee. A banner
above the page shows that metadata is present, with an "edit" action.
The SEO audit checks it for `title:` and `description:` keys and snippet-
friendly description length. Unterminated fences are treated as content
(a lone leading `---` is a thematic break).

Metadata is created and edited in-app via **Insert > Metadata preamble**
(or the
banner's "edit"): a free-form dialog holding the content between the
fences -- OpinEd supplies the `---` fences, the same division of labor as
Component and Variable. No key vocabulary is imposed; write whatever your
SSG expects. Content is carried verbatim and YAML syntax is the author's
responsibility (the SEO audit's title/description checks still apply).
Fence style (`...` terminators, CRLF) is preserved; an unedited dialog
changes nothing; clearing all text removes the block. Applying validates
the YAML with a real parser (gopkg.in/yaml.v3) -- advisory only: errors
display in the dialog with the parser's line information, and "Apply
anyway" remains available. Apply saves the document immediately (for an
untitled document it snapshots to the recovery file and defers to the
first Save). On save, the
file is written with exactly one blank line between the closing fence and
the body. Config defaults
merged at export and Dublin Core mapping remain unplanned/possible
follow-ons.

## Shortcodes and variables (SSG notation)

SSG templating notation is not GFM, but files contain it and the editor
must not corrupt it. Recognized notation is preserved verbatim through
open and save (never escaped, never parsed as Markdown emphasis). The
config declares only the SSG *flavor* -- shortcode and variable
definitions live in your SSG, never in the editor:

    [shortcodes]
    syntaxes = ["hugo"]
    # "hugo":   shortcodes {{< ... >}} / {{% ... %}}, variables {{ ... }}
    # "liquid": tags {% ... %}, variables/output {{ ... }}

Recognition is stateless and pattern-based: typed, pasted, and
pre-existing notation are protected alike.

A third setting, `syntaxes = ["strict"]`, declares native GFM only: no
notation layer exists. Brace text is ordinary prose and is escaped on
save so it stays literal in rendered output; Insert > Component and
Insert > Variable are hidden; pairing, verbatim blocks, and checker
blanking are all inert. Strict is exclusive (it overrides flavors listed
alongside it), and flipping the config later is deterministic: notation
escaped under strict does not match any flavor's patterns, so it never
resurrects as live notation. The metadata preamble is a separate concern
and remains available under strict.

**Raw HTML** in opened files is escaped to visible, editable literal text
(never rendered, never silently dropped -- goldmark's default omission is
overridden). **Every paired component** -- one whose closing fence names its opener,
like `{{< table ... >}} ... {{< /table >}}` or `{% highlight %} ...
{% endhighlight %}` -- is automatically a verbatim region, with no
per-name configuration: its entire span, fences and content alike, is
displayed as a read-only source block (like code-block content), excluded
from the checkers, preserved byte-for-byte through save, and edited by
clicking the block (a raw dialog on the metadata-editor pattern; Apply
saves the document, clearing all text deletes the block). Self-contained
components (`{{< youtube id >}}`) remain inline, individually protected
text. Find still matches text inside verbatim regions; the checkers
ignore them. Notation must stay on one line
and unformatted (bolding half a shortcode splits it and it degrades to
escaped text). The checkers and audits ignore recognized notation (it is
blanked from the text they analyze); find still matches it.

The toolbar **Insert** menu (between Unlink and Table) holds Image,
Component, and Variable ("component" is the menu's generic term for a
shortcode/tag; the notation depends on the configured flavor). The
Component/Variable dialogs collect the text that goes between the
brackets; the editor wraps it in the first configured flavor's brackets
and inserts at the last cursor position. E.g. with the hugo flavor,
entering `figure src="x.png"` in the Component dialog inserts
`{{< figure src="x.png" >}}`. Variables are inserted and
preserved as notation only -- they are not resolved to values in the
editor (the SSG owns resolution).

## Notes (margin annotations)

Notes are annotations associated with, but never part of, the document:
the author's reminders or others' comments. They live in a sidecar beside
the source -- `glossary.md` gets `glossary-notes.md` -- which is itself
valid GFM (one `## <anchor>` section per note; an anchor is a heading's
`#slug` or `general` for document-level notes), so it is readable
anywhere, diffable, and committable next to its document. The file is
created on the first note and removed with the last.

**Insert > Note** creates a note anchored to the caret's section (the
nearest heading above; `general` if none). Notes display as Tufte-style
margin cards in a right gutter, top-aligned to their headings and stacked
on collision, with a small badge at each annotated heading (the badge marks "this
section has a note" and remains visible -- and clickable -- when the
margin is hidden; document-level and unanchored notes badge at the
sheet's top-right corner, unanchored ones in amber); the margin is hidden by default -- the
notes toolbar button (right cluster) shows it (shifting the page left to
make gutter room) and hides it again; badges always display, so an
annotated document announces itself even with the margin closed. Click a card or
badge to edit (Apply persists the sidecar immediately; clearing all text
deletes the note). A note whose heading was renamed or removed shows
flagged as *unanchored* rather than being silently re-guessed. Note edits
never mark the document dirty -- the sidecar persists independently (for
an untitled document, notes are held and written on first Save). All
display is overlay: the document DOM is never touched.

## Special characters

The Ω toolbar button (right of inline code) opens a LibreOffice-style
Special Characters dialog: a Unicode-subset dropdown and character grid.
Hovering shows the code point; clicking inserts the character at the last
cursor position in the document and closes the dialog. Invisible characters
(zero-width, bidi controls, soft hyphen) are excluded from the grid.

## Character hygiene (with Spelling & grammar)

The checker is off by default; Audit > Spelling & grammar turns it on
(the linter loads on first use), after which it checks continuously
until toggled off.

The continuous checker also runs three native Unicode-level checks that
Vale's plaintext mode structurally cannot host (non-ASCII characters are
unmatchable there, established empirically): consecutive space-class
characters, including the space + non-breaking space pair minted by
typing two spaces in the editor (the message says when a hidden nbsp is
present); double-quote style consistency (the minority style is marked
when a document mixes straight and curly); and smart quotes inside code
spans or blocks, which break copy-paste and compilation (opaque component
regions exempt). Findings appear as checker marks with a [Character]
kind; the usual suppression rules apply. A single non-breaking space
(10 GB) is deliberately not flagged.

## Undo / redo

Ctrl+Z undoes; Ctrl+Y or Ctrl+Shift+Z redoes. The chords are intercepted
by the editor because this webview delivers no native undo accelerator
(the command machinery works; the keybinding never fires). Coverage is
the native contenteditable history: typing, deletion, inline formatting,
block format changes, and insertions made as text. Outside the history,
by architecture: table structure operations, code-block newlines and
in-block pastes, block-to-code conversions, and anything applied through
a dialog (metadata, raw components, notes) -- each dialog is its own
reversal path. Undo in the find field stays local to the field.

## Audit menu

The Audit menu hosts document checkers:

- **Spelling & grammar** (implemented; powered by Harper) -- continuous
  checking via harper.js (WASM, fully local). Spelling issues get red underlines, grammar
  and style issues blue; placing the caret inside a mark shows the message in
  the status bar. Toggle it from the menu (toggling on/off also flips native
  browser spellcheck, so the two never double-underline). Code blocks and
  inline code are excluded from checking.
- **Vale style** (implemented; powered by a vendored Vale) -- on-demand
  style checking from the Audit menu. The Vale binary and style packages
  ship inside the editor (`make vendor-vale`; the user's installed Vale, if
  any, is not used) and are extracted to the user cache dir on first run.
  Which styles apply comes from `~/.config/OpinEd/config.toml`:

      [vale]
      styles = ["Google"]            # default; multiple allowed, in order

  Vendored styles: Google (default), Microsoft, write-good. Styles you
  author yourself can live outside the binary: uncomment
  `extra_styles_path` in config.toml to name a directory whose
  subdirectories are style packages (referenced by name in `styles`).
  Vendored names win collisions; the path is re-read on every run, so
  rule edits apply on the next check without restarting. Findings paint
  as purple overlay marks; place the caret in a mark to read the message
  (with the rule name) in the status bar. Marks clear on edit; re-run from
  the menu. Checking runs over the document's plain text, so Markdown-
  scoped Vale rules (e.g. heading-specific scopes) are not distinguished.
  config.toml is written once as a commented template if absent, is never
  rewritten by the app, and is re-read on every run of the checker.
- **SEO audit** (implemented; native) -- on-demand on-page checks:
  H1 presence/uniqueness/length, heading-level skips, empty or duplicate
  headings, empty sections, image alt text, generic or bare-URL link anchors,
  thin content, over-long paragraphs and sentences, a Flesch readability
  score, and within-document fragment links: every #anchor link is checked
  against the document's heading anchors (GitHub's slug algorithm --
  lowercase, punctuation stripped, spaces to hyphens, duplicates suffixed
  -n; Hugo agrees for ASCII headings), with a did-you-mean suggestion when
  a near match exists. Cross-file fragments (page.md#section) are
  site-level and out of scope. Clicking a finding jumps to it with a brief highlight. Scope is
  honest: only what is verifiable from the document itself -- rankings,
  backlinks, and page speed are out of scope. Front-matter metadata is
  checked (title/description keys, snippet-friendly description length,
  block-scalar syntax problems).
- **AI readability** (implemented; native) -- on-demand GEO checks of
  whether the document survives chunk-and-retrieve reading: per-section
  word/token budgets (RAG pipelines split at heading boundaries), sections
  opening with unresolved references ("This...", "As mentioned above..."),
  generic or over-long headings (the heading labels the retrieved chunk),
  buried key points (long opening sentences), undefined acronyms, and
  unheaded prose. Same clickable, locate-on-click findings as the SEO
  audit.
- **Tufte audit** (implemented; native) -- structural and rhetorical checks
  from Tufte's writing: bullet "grunts" (fragment-heavy lists), list-heavy
  documents, bare-list sections, deeply nested lists, headings past H3,
  slide-deck fragmentation (many tiny sections -- deliberately in tension
  with the AI readability check's chunk sizing: different target readers),
  emphasis overuse (share of bold/italic words, whole-bold paragraphs,
  ALL-CAPS runs), unattributed quotations, figures cited with no links,
  and images lacking adjacent prose. Scope is stated honestly: chartjunk,
  data-ink, and sidenote discipline are not assessable from GFM.

Harper's assets are vendored, not fetched at runtime:

    make vendor-harper   # one-time; needs only curl + tar (no Node/npm)
    make                 # rebuild -- assets are embedded via go:embed

This grows the binary by ~18 MB (the WASM). Without vendoring, the editor
works normally and the Harper menu item reports itself unavailable.

The UI is served to the webview from an embedded loopback HTTP server
(127.0.0.1, random port, embedded static files only) because ES modules and
WebAssembly cannot load from a SetHtml page, which has no origin.

## Help

The Help button (far right of the toolbar, or F1) contains:

- **Keyboard shortcuts** -- quick-reference dialog.
- **About** -- application version (currently 0.01, defined as `appVersion`
  in `main.go`, the single source of truth).

## Keyboard shortcuts

| Shortcut | Action |
|---|---|
| Ctrl+N / O / S / Shift+S | New / Open / Save / Save As |
| F1 | Keyboard shortcuts dialog |
| Ctrl+B / I | Bold / Italic |
| Ctrl+Shift+X | Strikethrough |
| Ctrl+E | Inline code |
| Ctrl+K | Insert link |
| Ctrl+Click | Open link in system browser (http/https only) |
| Ctrl+R | Reload from file (discards unsaved changes) |
| Ctrl+F | Find in document |
| F3 / Shift+F3 | Next / previous match |
| Ctrl+1..6 | Heading level |
| Ctrl+0 | Paragraph |
| Shift+Enter | Hard line break |

## Design decisions and known limitations

- **Accelerated compositing is disabled** (`WEBKIT_DISABLE_COMPOSITING_MODE=1`,
  set at startup unless already set). On the systems tested, WebKitGTK
  rasterizes text inside actually-scrolling overflow regions (e.g. code
  blocks with horizontal scroll) through the compositing tile path, drawing
  it visibly heavier than identical static text. Software rendering makes
  typography uniform; override with WEBKIT_DISABLE_COMPOSITING_MODE=0.
  Related: code blocks use 13px integer font size -- at 14px+ (and at
  fractional sizes) hinting stem-rounding makes monospace read as bold.

- **Find-in-document highlights are drawn in an overlay layer**, not with the
  CSS Custom Highlight API: on the WebKitGTK builds tested, the API painted
  highlights but failed to repaint on registry delete/replace, leaving stale
  highlights on screen. The overlay (translucent boxes in a sibling of the
  editable area) clears via plain DOM removal, and never touches the document
  itself -- searching cannot dirty the file, the undo stack, or saved output.

- **Raw HTML is deliberately not rendered.** GFM permits inline HTML (with
  GitHub's tag filter), but this editor's UI runs in a webview whose page has
  bound functions that write to the filesystem (`appSave`). Rendering
  `<script>` from an arbitrary opened `.md` file would be an
  arbitrary-file-write vulnerability, so raw HTML is shown escaped as literal
  text (goldmark's default).
- **Save normalizes syntax.** Input syntax variants (setext headings,
  reference-style links, `*` vs `-` bullets, indented code blocks) parse
  correctly but are re-serialized in one canonical form (ATX headings, inline
  links, fenced code). Semantics are preserved; byte-identical round-trips are
  not. Relevant if files live under review in Git.
- Table column alignment (`:---:`) parses and round-trips best-effort but has
  no editing UI.
- Images referenced by relative/local path save correctly but may not display
  inside the editor (the embedded page has no file:// base URL).
- Direct DOM operations (table row/column edits, checkbox toggles) are not in
  the browser undo stack; Ctrl+Z covers typing and formatting commands only.
- Footnotes, alerts (`> [!NOTE]`), Mermaid, and math are GitHub *pipeline*
  features, not part of the GFM spec, and are out of scope.
- New and Open prompt to save unsaved changes (Save / Don't Save / Cancel,
  via a native zenity dialog). Closing the *window* does not prompt (the
  webview bindings expose no close-request hook), but **autosave/crash
  recovery** caps the damage: every 30 seconds a dirty document is
  snapshotted to ~/.config/OpinEd/recovery.md (mode 0600, with a JSON
  sidecar recording the original path). On the next launch the app offers
  Restore/Discard. This also covers crashes and power loss. The snapshot is
  cleared by every deliberate disposal: manual save, "Don't Save", Reload,
  or choosing Discard at the prompt. A GTK delete-event close guard remains
  a possible future refinement.
- Single document per window.
