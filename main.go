// OpinEd: a WYSIWYG editor for GitHub Flavored Markdown.
//
// Architecture:
//   - The UI is an embedded WebKitGTK window (github.com/webview/webview_go)
//     hosting a contenteditable surface (editor.html, embedded at build time).
//   - Markdown -> HTML on open uses goldmark with the GFM extension
//     (tables, task lists, strikethrough, autolinks + full CommonMark).
//   - HTML -> Markdown on save uses JohannesKaufmann/html-to-markdown with
//     its GitHubFlavored plugin (tables, task list items, strikethrough).
//   - Native open/save dialogs are provided by ncruces/zenity (which shells
//     out to the zenity binary on Linux).
//
// Security note: raw HTML passthrough is deliberately DISABLED (goldmark's
// default). Because the UI runs in a webview with bound functions that can
// write to the filesystem (appSave), rendering attacker-controlled <script>
// from an opened .md file would be an arbitrary-file-write vulnerability.
// Raw HTML in opened files is therefore shown escaped, as literal text.
//
// Usage:
//   OpinEd [file.md]
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	htmlmd "github.com/JohannesKaufmann/html-to-markdown"
	toml "github.com/pelletier/go-toml/v2"
	yaml "gopkg.in/yaml.v3"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/ncruces/zenity"
	webview "github.com/webview/webview_go"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

//go:embed editor.html
var editorHTML string

// assets holds the UI plus vendored tool assets (Harper's JS/WASM under
// assets/harper, populated by `make vendor-harper`). Named "assets", NOT
// "vendor": a root vendor/ directory flips the Go toolchain into vendored-
// dependency mode and breaks the build. Served over a loopback HTTP server
// because ES modules and WebAssembly cannot load from a SetHtml page,
// which has no real origin.
//
//go:embed editor.html assets
var assets embed.FS

// appName is the single source of truth for the application name: window
// title, per-user directory names (~/.config/OpinEd, ~/.cache/OpinEd),
// and the About dialog. Case-sensitive by decision.
const appName = "OpinEd"

// appVersion is the single source of truth for the version shown in
// Help > About.
const appVersion = "0.01"

var mdFilters = zenity.FileFilters{
	{Name: "Markdown files", Patterns: []string{"*.md", "*.markdown"}, CaseFold: true},
	{Name: "All files", Patterns: []string{"*"}},
}

// document is what the Go side hands to the JS side when a file is loaded.
// Front carries the file's leading YAML front matter VERBATIM (fences and
// all, empty if none): it is document metadata, not content, so it is held
// aside before Markdown parsing and re-prepended byte-for-byte on save.
// This is the one region of the file with a byte-preservation guarantee.
type document struct {
	Path  string `json:"path"`
	HTML  string `json:"html"`
	Front string `json:"front"`
}

// splitFrontMatter separates leading YAML front matter from the body.
// Recognized only when the file's very first line is exactly "---"; the
// block ends at the next line that is exactly "---" or "..." (YAML's two
// document terminators). If no terminator exists, the file is treated as
// having no front matter (a lone leading --- is a thematic break, and
// guessing otherwise risks eating content).
func splitFrontMatter(raw []byte) (front string, body []byte) {
	if !bytes.HasPrefix(raw, []byte("---\n")) && !bytes.HasPrefix(raw, []byte("---\r\n")) {
		return "", raw
	}
	offset := bytes.IndexByte(raw, '\n') + 1
	rest := raw[offset:]
	for {
		lineEnd := bytes.IndexByte(rest, '\n')
		var line []byte
		if lineEnd == -1 {
			line = rest
		} else {
			line = rest[:lineEnd]
		}
		trimmed := strings.TrimRight(string(line), "\r")
		if trimmed == "---" || trimmed == "..." {
			end := offset + len(rest)
			if lineEnd != -1 {
				end = offset + lineEnd + 1
			}
			return string(raw[:end]), raw[end:]
		}
		if lineEnd == -1 {
			return "", raw // unterminated: not front matter
		}
		offset += lineEnd + 1
		rest = rest[lineEnd+1:]
	}
}

// ---- Machine state (state.json) ----
// App-written state lives in JSON (per the config design decision: TOML is
// for the human-edited config.toml, which the app never rewrites). Tenants:
// the last opened/saved file (dialog positioning) and the theme choice.
// The legacy one-line "lastdir" file is migrated in and retired.

type appState struct {
	LastPath string `json:"lastPath,omitempty"`
	Theme    string `json:"theme,omitempty"`
}

var appStateData appState

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, appName, "state.json")
}

func saveState() {
	p := statePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(appStateData, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o644)
}

func loadState() {
	p := statePath()
	if p == "" {
		return
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &appStateData)
	} else {
		// Migrate the legacy one-line lastdir file, then retire it.
		legacy := filepath.Join(filepath.Dir(p), "lastdir")
		if lb, lerr := os.ReadFile(legacy); lerr == nil {
			appStateData.LastPath = strings.TrimSpace(string(lb))
			saveState()
			_ = os.Remove(legacy)
		}
	}
	// Validate the remembered path; fall back to its directory, then none.
	if appStateData.LastPath != "" {
		if _, err := os.Stat(appStateData.LastPath); err != nil {
			d := filepath.Dir(appStateData.LastPath)
			if st, derr := os.Stat(d); derr == nil && st.IsDir() {
				appStateData.LastPath = d
			} else {
				appStateData.LastPath = ""
			}
		}
	}
}

// rememberPath records a successfully opened/saved file.
func rememberPath(filePath string) {
	if filePath == "" || filePath == appStateData.LastPath {
		return
	}
	appStateData.LastPath = filePath
	saveState()
}

// lastDirOf returns the directory implied by the remembered path ("" if none).
func lastDirOf() string {
	lp := appStateData.LastPath
	if lp == "" {
		return ""
	}
	if st, err := os.Stat(lp); err == nil && st.IsDir() {
		return lp
	}
	return filepath.Dir(lp)
}

// startLocation returns the zenity Filename option positioning a dialog
// inside the last-used directory (with the last file preselected when it
// still exists), or nil when there is no usable memory.
func startLocation() zenity.Option {
	lp := appStateData.LastPath
	if lp == "" {
		return nil
	}
	if st, err := os.Stat(lp); err == nil && !st.IsDir() {
		return zenity.Filename(lp)
	}
	if d := lastDirOf(); d != "" {
		return zenity.Filename(d + string(os.PathSeparator))
	}
	return nil
}

// ---- User configuration (config.toml) ----
// TOML for human-edited preferences (per the config design decision);
// machine state stays in separate JSON/plain files. Absent config gets a
// commented template written once. Read fresh on each use so edits apply
// without restarting.

type appConfig struct {
	Vale struct {
		Styles          []string `toml:"styles"`
		ExtraStylesPath string   `toml:"extra_styles_path"`
	} `toml:"vale"`
	Shortcodes struct {
		Syntaxes []string `toml:"syntaxes"`
	} `toml:"shortcodes"`
	Editor struct {
		CodeFontSize int    `toml:"code_font_size"`
		Audience     string `toml:"audience"`
	} `toml:"editor"`
}

const configTemplate = `# OpinEd configuration (user preferences).
# This file is yours: the app writes it once as a template and never
# rewrites it. Machine state (last directory, theme, recovery) lives
# elsewhere.

[vale]
# Style packages applied by Audit > Vale, in order.
# Vendored styles: "Google", "Microsoft", "write-good".
styles = ["Google"]
# Additional directory searched for style packages you author yourself
# (each style is a subdirectory of Vale YAML rule files, named as listed
# in styles above). Vendored styles win name collisions.
#extra_styles_path = "~/writing/vale-styles"

[shortcodes]
# SSG flavor. Declares which notation is recognized and preserved
# verbatim through open/save, and which brackets Insert > Shortcode /
# Variable wrap around your text. Shortcode and variable DEFINITIONS
# live in your SSG, not here. Available flavors (first entry drives
# insertion):
#   "hugo"    shortcodes {{< ... >}} and {{% ... %}}, variables {{ ... }}
#   "liquid"  tags {% ... %}, variables/output {{ ... }}
#   "strict"  native GFM only -- no notation layer. Brace text is
#             ordinary prose (escaped to stay literal on save); Insert >
#             Component / Variable are hidden. Exclusive: overrides any
#             flavors listed alongside it. The metadata preamble is a
#             separate concern and remains available.
syntaxes = ["hugo"]
# Note: any PAIRED component's entire span (opening fence through closing
# fence) is automatically a verbatim region -- shown as a read-only source
# block, byte-preserved, checker-excluded -- with no per-name setting.

[editor]
# Code block font size in integer pixels (default 11, accepted range
# 8-24). Note: on some Linux systems the monospace face renders with
# synthetically heavy ("fake bold") stems at 14px and above.
code_font_size = 11

# Target audience for the SEO audit's readability calibration.
# "technical" (default): domain vocabulary legitimately depresses the
# Flesch score, so 40-59 reads as acceptable and scores below 40 draw a
# diagnostic finding. "general": conventional Flesch bands, with the
# finding drawn below 60.
audience = "technical"
`

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, appName, "config.toml")
}

func loadConfig() appConfig {
	var cfg appConfig
	cfg.Vale.Styles = []string{"Google"}          // default per spec
	cfg.Shortcodes.Syntaxes = []string{"hugo"}    // conservative default
	cfg.Editor.CodeFontSize = 11                  // default per spec
	cfg.Editor.Audience = "technical"             // matches shipped calibration
	p := configPath()
	if p == "" {
		return cfg
	}
	b, err := os.ReadFile(p)
	if err != nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(configTemplate), 0o644)
		return cfg
	}
	var parsed appConfig
	if err := toml.Unmarshal(b, &parsed); err != nil {
		return cfg // malformed config: fall back to defaults, never crash
	}
	if len(parsed.Vale.Styles) > 0 {
		cfg.Vale.Styles = parsed.Vale.Styles
	}
	cfg.Vale.ExtraStylesPath = strings.TrimSpace(parsed.Vale.ExtraStylesPath)
	if len(parsed.Shortcodes.Syntaxes) > 0 {
		cfg.Shortcodes.Syntaxes = parsed.Shortcodes.Syntaxes
	}
	// "strict" (native GFM only) is exclusive: combining it with a flavor
	// is a contradiction, resolved in strict's favor. All pattern builders
	// switch on known flavor names, so ["strict"] yields empty sets --
	// no recognition, no pairing, no blocks, no blanking.
	for _, sy := range cfg.Shortcodes.Syntaxes {
		if sy == "strict" {
			cfg.Shortcodes.Syntaxes = []string{"strict"}
			break
		}
	}
	// Integer px only, clamped: on the target system the code face
	// rasterizes with synthetically heavy stems at 14px+ and at fractional
	// sizes, so out-of-range values fall back rather than surprise.
	if parsed.Editor.CodeFontSize >= 8 && parsed.Editor.CodeFontSize <= 24 {
		cfg.Editor.CodeFontSize = parsed.Editor.CodeFontSize
	}
	if a := strings.ToLower(strings.TrimSpace(parsed.Editor.Audience)); a == "general" || a == "technical" {
		cfg.Editor.Audience = a
	}
	return cfg
}

// ---- Shortcode / variable preservation ----
// SSG notation ({{< ... >}}, {{ ... }}, {% ... %}) is not GFM, but files in
// the wild contain it and both pipeline directions corrupt it: goldmark
// parses inner underscores/asterisks as emphasis on open, and the
// serializer backslash-escapes the braces on save. Recognized spans are
// therefore swapped for inert alphanumeric tokens before each conversion
// and restored verbatim after. Which syntax FAMILIES are recognized comes
// from config.toml ([shortcodes] syntaxes); recognition patterns are
// built-in, not user regexes (an over-broad user pattern would silently
// exempt prose from escaping). Detection is stateless and re-runs every
// open/save, so typed, pasted, and pre-existing notation are all protected
// alike. Spans are line-bound: notation split across lines (or split by
// inline formatting) is not recognized and degrades to escaped text.

func shortcodeRegexes(entityForm bool) []*regexp.Regexp {
	lt, gt := "<", ">"
	// Interior class: braces excluded, so newlines are legal (Hugo permits
	// multi-line shortcode calls) while a stray unclosed opener cannot run
	// away past the next notation -- braces are the fence. The entity form
	// additionally excludes real < and > so a match can never cross an
	// element tag in innerHTML (multi-line notation lives in ONE text node
	// with literal newlines; anything spread across blocks stays
	// unprotected by design).
	body := `[^{}]`
	if entityForm {
		lt, gt = "&lt;", "&gt;"
		body = `[^{}<>]`
	}
	var out []*regexp.Regexp
	for _, s := range loadConfig().Shortcodes.Syntaxes {
		switch s {
		case "hugo":
			// Complete flavor: shortcodes {{< >}} / {{% %}} AND variables
			// {{ }}. Specific forms first so the generic pattern only
			// catches what remains. Variables stay line-bound: prose braces
			// pairs across lines must not be swallowed.
			out = append(out,
				regexp.MustCompile(`\{\{`+lt+body+`*?`+gt+`\}\}`),
				regexp.MustCompile(`\{\{%`+body+`*?%\}\}`),
				regexp.MustCompile(`\{\{[^{}\n]*?\}\}`))
		case "liquid":
			// Tags {% %} and variables/output {{ }}.
			out = append(out,
				regexp.MustCompile(`\{%`+body+`*?%\}`),
				regexp.MustCompile(`\{\{[^{}\n]*?\}\}`))
		case "params": // legacy alias: bare {{ ... }}
			out = append(out, regexp.MustCompile(`\{\{[^{}\n]*?\}\}`))
		}
	}
	return out
}

func scToken(i int) string { return fmt.Sprintf("GFMSC%dTOKEN", i) }

func protectShortcodes(s string, pats []*regexp.Regexp) (string, []string) {
	var saved []string
	for _, re := range pats {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			saved = append(saved, m)
			return scToken(len(saved) - 1)
		})
	}
	return s, saved
}

func restoreShortcodes(s string, saved []string, transform func(string) string) string {
	for i, orig := range saved {
		if transform != nil {
			orig = transform(orig)
		}
		s = strings.Replace(s, scToken(i), orig, 1)
	}
	return s
}

// Text-node entity forms: the editor DOM shows shortcode text literally,
// so < > & appear entity-encoded in innerHTML and must be translated at
// the HTML-side boundary of each swap.
func entityEncode(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func entityDecode(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}

// ---- Vale (vendored) ----
// Like Harper, Vale ships inside the binary (assets/vale, populated by
// `make vendor-vale`): the user's installed Vale, if any, is not used.
// The binary and style packages are extracted once per build into the
// user cache dir and exec'd there against a generated .vale.ini whose
// BasedOnStyles comes from config.toml.

// expandTilde resolves a leading ~/ against the user's home directory.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func ensureVale() (binPath, iniPath string, err error) {
	if _, err := assets.Open("assets/vale/vale"); err != nil {
		return "", "", fmt.Errorf("Vale is not vendored; run `make vendor-vale`, then `make`")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	root := filepath.Join(cache, appName, "vale")
	binPath = filepath.Join(root, "vale")
	marker := filepath.Join(root, ".extracted")
	info, err := fs.Stat(assets, "assets/vale/vale")
	if err != nil {
		return "", "", err
	}
	want := fmt.Sprintf("%s-%d", appVersion, info.Size())
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != want {
		werr := fs.WalkDir(assets, "assets/vale", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel("assets/vale", p)
			dst := filepath.Join(root, rel)
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			data, err := assets.ReadFile(p)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if rel == "vale" {
				mode = 0o755
			}
			return os.WriteFile(dst, data, mode)
		})
		if werr != nil {
			return "", "", werr
		}
		_ = os.WriteFile(marker, []byte(want), 0o644)
	}
	// Reconcile user-authored styles: refresh symlinks from
	// extra_styles_path into the styles dir, then validate. Symlinks are
	// dropped and re-derived every run so config edits apply immediately
	// and stale links never linger. Vendored styles win name collisions
	// (a same-named symlink would otherwise be written THROUGH by the
	// next re-extraction).
	cfg := loadConfig()
	requested := cfg.Vale.Styles
	stylesDir := filepath.Join(root, "styles")
	if entries, err := os.ReadDir(stylesDir); err == nil {
		for _, e := range entries {
			full := filepath.Join(stylesDir, e.Name())
			if fi, lerr := os.Lstat(full); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(full)
			}
		}
	}
	if extra := expandTilde(cfg.Vale.ExtraStylesPath); extra != "" {
		for _, st := range requested {
			dst := filepath.Join(stylesDir, st)
			if _, err := os.Stat(dst); err == nil {
				continue // vendored wins
			}
			if src := filepath.Join(extra, st); dirExists(src) {
				_ = os.Symlink(src, dst)
			}
		}
	}
	var valid []string
	for _, st := range requested {
		if _, err := os.Stat(filepath.Join(stylesDir, st)); err == nil {
			valid = append(valid, st)
		}
	}
	if len(valid) == 0 {
		return "", "", fmt.Errorf("no configured Vale style in %v is vendored or found under extra_styles_path; check [vale] in %s", requested, configPath())
	}
	ini := "StylesPath = styles\nMinAlertLevel = suggestion\n\n[*]\nBasedOnStyles = " + strings.Join(valid, ", ") + "\n"
	iniPath = filepath.Join(root, ".vale.ini")
	if err := os.WriteFile(iniPath, []byte(ini), 0o644); err != nil {
		return "", "", err
	}
	return binPath, iniPath, nil
}

// valeFinding is what the UI receives; positions refer to the plain text
// the UI submitted (same flattened text the other checkers use).
type valeFinding struct {
	Line     int    `json:"line"`
	Span     [2]int `json:"span"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Check    string `json:"check"`
}

// ---- Autosave / crash recovery ----
// Every autosave interval the UI ships the current (dirty) document here;
// it is written as a complete GFM file plus a small JSON sidecar with the
// original path and timestamp. On the next launch, if a recovery pair
// exists, the user is offered a restore. The recovery file caps the damage
// of every unguarded exit: window close, crash, power loss. Deliberate
// data-disposal paths (manual save, "Don't Save", Reload) clear it.
// File mode 0600: recovered drafts may hold content the on-disk file never
// had.

func recoveryPaths() (mdPath, metaPath string) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", ""
	}
	base := filepath.Join(dir, appName)
	return filepath.Join(base, "recovery.md"), filepath.Join(base, "recovery.json")
}

type recoveryMeta struct {
	Path string `json:"path"`
	Time string `json:"time"`
}

func clearRecovery() {
	mdP, metaP := recoveryPaths()
	if mdP != "" {
		_ = os.Remove(mdP)
		_ = os.Remove(metaP)
	}
}

// ---- Raw-HTML escape (safety floor) ----
// goldmark's safe default OMITS raw HTML on parse -- silent data loss for
// opened files (observed destroying a shortcode's <tr> rows). This
// renderer override keeps goldmark safe but ESCAPES instead: raw HTML
// arrives in the editor as visible, editable literal text, honoring the
// documented invariant. Declared opaque components (below) are lifted out
// before parsing and never reach this path; this is the backstop for
// stray or undeclared HTML.

type rawHTMLEscaper struct{}

func (r rawHTMLEscaper) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
}

func (r rawHTMLEscaper) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.HTMLBlock)
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i) // bind: Value has a pointer receiver
		line := strings.TrimRight(string(seg.Value(source)), "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, _ = w.WriteString("<p>" + entityEncode(line) + "</p>\n")
	}
	if n.HasClosure() {
		line := strings.TrimRight(string(n.ClosureLine.Value(source)), "\n")
		if strings.TrimSpace(line) != "" {
			_, _ = w.WriteString("<p>" + entityEncode(line) + "</p>\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r rawHTMLEscaper) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i) // bind: Value has a pointer receiver
		_, _ = w.WriteString(entityEncode(string(seg.Value(source))))
	}
	return ast.WalkSkipChildren, nil
}

// ---- Verbatim paired-component regions ----
// Every paired component's span is lifted out before Markdown parsing,
// shown as a read-only monospace block, excluded from checkers (it
// renders as a pre, which the collectors already skip), edited via a raw
// dialog, and restored byte-for-byte on save. The transport-vs-author
// principle at region scale, applied universally (see the scanner below).

type opaqueSpan struct {
	Name string
	Text string
}

// ---- Paired-shortcode span scanner ----
// Universal rule (no per-name configuration): ANY paired component's
// entire span -- opening fence through closing fence -- is a verbatim
// region, like code-block content. Pairing is detected syntactically: a
// closing fence names its opener ({{< /name >}}, {% endname %}), so
// fences are tokenized with names and matched on a stack. Self-contained
// shortcodes ({{< youtube id >}}) simply never find a closer and remain
// individually fence-protected inline; strays inside a pair are swallowed
// into its verbatim text; nesting resolves to the outermost span via a
// containment sweep. RE2 has no backreferences, hence a scanner rather
// than one regex.

type scPairedSpan struct {
	Name  string
	Start int
	End   int
}

type scFence struct {
	start, end int
	name       string
	closing    bool
	style      int
}

func scanPairedSpans(body string, syntaxes []string) []scPairedSpan {
	type patStyle struct {
		re    *regexp.Regexp
		style int
	}
	var pats []patStyle
	for _, s := range syntaxes {
		switch s {
		case "hugo":
			pats = append(pats,
				patStyle{regexp.MustCompile(`\{\{<\s*(/)?\s*([A-Za-z0-9_.-]+)\b[^{}]*?>\}\}`), 0},
				patStyle{regexp.MustCompile(`\{\{%\s*(/)?\s*([A-Za-z0-9_.-]+)\b[^{}]*?%\}\}`), 1})
		case "liquid":
			pats = append(pats,
				patStyle{regexp.MustCompile(`\{%\s*(end)?([A-Za-z0-9_-]+)\b[^{}]*?%\}`), 2})
		}
	}
	var fences []scFence
	for _, ps := range pats {
		for _, m := range ps.re.FindAllStringSubmatchIndex(body, -1) {
			fences = append(fences, scFence{m[0], m[1], body[m[4]:m[5]], m[2] != -1, ps.style})
		}
	}
	sort.Slice(fences, func(i, j int) bool { return fences[i].start < fences[j].start })
	var candidates []scPairedSpan
	var stack []scFence
	for _, f := range fences {
		if !f.closing {
			stack = append(stack, f)
			continue
		}
		// Nearest matching open, skipping self-contained strays above it.
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].name == f.name && stack[i].style == f.style {
				candidates = append(candidates, scPairedSpan{f.name, stack[i].start, f.end})
				stack = stack[:i]
				break
			}
		}
	}
	// Outermost only: start asc, end desc, sweep non-contained.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Start != candidates[j].Start {
			return candidates[i].Start < candidates[j].Start
		}
		return candidates[i].End > candidates[j].End
	})
	var spans []scPairedSpan
	lastEnd := -1
	for _, c := range candidates {
		if c.Start >= lastEnd {
			spans = append(spans, c)
			lastEnd = c.End
		}
	}
	return spans
}

func opaqueToken(i int) string { return fmt.Sprintf("GFMOPQ%dTOKEN", i) }

// componentNameOf pulls the component name from a fence's leading token
// (any flavor, open or close) for the block's label; "component" if the
// shape is unexpected.
var componentNameRe = regexp.MustCompile(`^\{(?:\{[<%]|%)\s*/?\s*(?:end)?([A-Za-z0-9_.-]+)`)

func componentNameOf(fence string) string {
	if m := componentNameRe.FindStringSubmatch(fence); m != nil {
		return m[1]
	}
	return "component"
}

// joinFrontBody joins metadata and body for writing: exactly one blank
// line between the closing fence and the first content line (dialog-
// created metadata previously abutted the body's first heading; opened
// files lost their separating blank to serializer normalization on every
// save). The front block itself remains verbatim; the blank line is
// body-side formatting, normalized like the rest of the body.
func joinFrontBody(front, md string) string {
	if front == "" {
		return md
	}
	if !strings.HasSuffix(front, "\n") {
		front += "\n"
	}
	return front + "\n" + strings.TrimLeft(md, "\n")
}

// mdToHTML converts GFM source to HTML for display in the editor.
// extension.GFM = Table + Strikethrough + TaskList + Linkify, on top of
// goldmark's CommonMark core.
func mdToHTML(src []byte) (string, error) {
	// Paired-component spans first: whole spans lift out before anything
	// can parse or token them, each standing in as its own paragraph token.
	body := string(src)
	var opaques []opaqueSpan
	if spans := scanPairedSpans(body, loadConfig().Shortcodes.Syntaxes); len(spans) > 0 {
		var b strings.Builder
		last := 0
		for _, sp := range spans {
			b.WriteString(body[last:sp.Start])
			opaques = append(opaques, opaqueSpan{Name: sp.Name, Text: body[sp.Start:sp.End]})
			b.WriteString("\n\n" + opaqueToken(len(opaques)-1) + "\n\n")
			last = sp.End
		}
		b.WriteString(body[last:])
		body = b.String()
	}
	// Multi-line SELF-CONTAINED fences also become component blocks: a
	// wrapped paragraph of notation reads poorly, and block presentation
	// matches the paired case. Single-line fences stay inline -- they live
	// mid-sentence. (The generic variable pattern is line-bound and can
	// never contain a newline, so variables are structurally exempt.)
	for _, re := range shortcodeRegexes(false) {
		body = re.ReplaceAllStringFunc(body, func(m string) string {
			if !strings.Contains(m, "\n") {
				return m
			}
			opaques = append(opaques, opaqueSpan{Name: componentNameOf(m), Text: m})
			return "\n\n" + opaqueToken(len(opaques)-1) + "\n\n"
		})
	}
	protected, saved := protectShortcodes(body, shortcodeRegexes(false))
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(rawHTMLEscaper{}, 200)),
		),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(protected), &buf); err != nil {
		return "", err
	}
	// Restore into HTML text-node form (entity-encoded) so the notation
	// displays literally in the editor.
	out := restoreShortcodes(buf.String(), saved, entityEncode)
	for i, op := range opaques {
		tok := opaqueToken(i)
		el := "<pre class=\"opaque-sc\" data-name=\"" + entityEncode(op.Name) +
			"\" contenteditable=\"false\">" + entityEncode(op.Text) + "</pre>"
		if strings.Contains(out, "<p>"+tok+"</p>") {
			out = strings.Replace(out, "<p>"+tok+"</p>", el, 1)
		} else {
			out = strings.Replace(out, tok, el, 1)
		}
	}
	return out, nil
}

// htmlToMD converts the editor's HTML content back to GFM for saving.
var opaqueElRe = regexp.MustCompile(`(?s)<pre class="opaque-sc"[^>]*>(.*?)</pre>`)

func htmlToMD(src string) (string, error) {
	// Verbatim component elements first: swap each for a bare token (which
	// the serializer treats as a one-word paragraph), then substitute the
	// verbatim span back into the produced Markdown.
	var opaqueTexts []string
	src = opaqueElRe.ReplaceAllStringFunc(src, func(m string) string {
		sub := opaqueElRe.FindStringSubmatch(m)
		opaqueTexts = append(opaqueTexts, entityDecode(sub[1]))
		return opaqueToken(len(opaqueTexts) - 1)
	})
	// In the editor DOM the notation is literal text, so it arrives here
	// entity-encoded; recognize that form, restore the raw form after.
	protected, saved := protectShortcodes(src, shortcodeRegexes(true))
	conv := htmlmd.NewConverter("", true, nil)
	conv.Use(plugin.GitHubFlavored())
	out, err := conv.ConvertString(protected)
	if err != nil {
		return "", err
	}
	out = restoreShortcodes(out, saved, entityDecode)
	for i, t := range opaqueTexts {
		out = strings.Replace(out, opaqueToken(i), t, 1)
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

func loadDocument(path string) (*document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	front, body := splitFrontMatter(raw)
	h, err := mdToHTML(body)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return &document{Path: abs, HTML: h, Front: front}, nil
}

// ---- Notes sidecar ----
// Notes are annotations associated with, but not part of, a document:
// they live beside it as <sourceName>-notes.md (glossary.md ->
// glossary-notes.md), itself a valid GFM file (## <anchor> sections with
// note text beneath). Never created until a first note exists; removed
// when the last note is deleted.

func notesPathFor(docPath string) string {
	ext := filepath.Ext(docPath)
	if ext == ".md" || ext == ".markdown" {
		return strings.TrimSuffix(docPath, ext) + "-notes.md"
	}
	return docPath + "-notes.md"
}

// migrateLegacyDirs renames the pre-rename per-user directories
// (gfm-editor -> OpinEd) so existing config, state, recovery files, and
// extracted tool caches carry over invisibly. Best-effort; only when the
// new location does not already exist.
func migrateLegacyDirs() {
	move := func(base string) {
		oldP := filepath.Join(base, "gfm-editor")
		newP := filepath.Join(base, appName)
		if _, err := os.Stat(newP); !os.IsNotExist(err) {
			return
		}
		if _, err := os.Stat(oldP); err == nil {
			_ = os.Rename(oldP, newP)
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		move(dir)
	}
	if dir, err := os.UserCacheDir(); err == nil {
		move(dir)
	}
}

func main() {
	migrateLegacyDirs()
	// Uniform text rendering: WebKitGTK rasterizes actually-scrolling
	// regions through its accelerated-compositing tile path, which on some
	// systems draws text visibly heavier than static content (observed:
	// horizontally scrolling code blocks looked bold next to identical
	// non-scrolling ones). Disabling compositing routes everything through
	// one software path. Set only if unset, so users can override with
	// WEBKIT_DISABLE_COMPOSITING_MODE=0.
	if _, set := os.LookupEnv("WEBKIT_DISABLE_COMPOSITING_MODE"); !set {
		os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
	}

	var initialPath string
	if len(os.Args) > 1 {
		initialPath = os.Args[1]
	}
	loadState()

	// GFM_DEBUG=1 enables the WebKit Web Inspector (right-click > Inspect
	// Element) for diagnosing rendering, font matching, and CSS issues.
	w := webview.New(os.Getenv("GFM_DEBUG") != "")
	defer w.Destroy()
	w.SetTitle(appName)
	// Geometry: the content page is max-width 760px, so at 980px wide the
	// gray workspace flanking it is ~110px per side (half the previous
	// ~220px). Height 1200 = previous 800 x 1.5.
	w.SetSize(980, 1200, webview.HintNone)

	// appInit: called once by the UI on startup. Returns the document given
	// on the command line, or null if the editor was started empty.
	w.Bind("appInit", func() (*document, error) {
		if initialPath == "" {
			return nil, nil
		}
		doc, err := loadDocument(initialPath)
		if err != nil {
			return nil, fmt.Errorf("cannot open %s: %w", initialPath, err)
		}
		rememberPath(doc.Path)
		return doc, nil
	})

	// appOpen: shows a native open dialog, returns the loaded document,
	// or null if the user cancelled.
	w.Bind("appOpen", func() (*document, error) {
		opts := []zenity.Option{zenity.Title("Open Markdown File"), mdFilters}
		if loc := startLocation(); loc != nil {
			opts = append(opts, loc)
		}
		path, err := zenity.SelectFile(opts...)
		if err == zenity.ErrCanceled {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		doc, err := loadDocument(path)
		if err != nil {
			return nil, err
		}
		rememberPath(doc.Path)
		return doc, nil
	})

	// appSave: converts the editor HTML to GFM and writes it. If path is
	// empty, a native save dialog is shown first. Returns the path written,
	// or "" if the user cancelled the dialog.
	w.Bind("appSave", func(htmlContent, path, front string) (string, error) {
		if path == "" {
			defaultName := "untitled.md"
			if d := lastDirOf(); d != "" {
				defaultName = filepath.Join(d, "untitled.md")
			}
			p, err := zenity.SelectFileSave(
				zenity.Title("Save Markdown File"),
				zenity.ConfirmOverwrite(),
				zenity.Filename(defaultName),
				mdFilters,
			)
			if err == zenity.ErrCanceled {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			path = p
		}
		md, err := htmlToMD(htmlContent)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(joinFrontBody(front, md)), 0o644); err != nil {
			return "", err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		rememberPath(abs)
		clearRecovery() // the work is now safely on disk
		return abs, nil
	})

	// appAutosave: periodic snapshot of a dirty document to the recovery
	// pair. Best-effort by design; a failed autosave must never disturb
	// editing (the UI ignores errors and retries next interval).
	w.Bind("appAutosave", func(htmlContent, front, origPath string) error {
		mdP, metaP := recoveryPaths()
		if mdP == "" {
			return nil
		}
		md, err := htmlToMD(htmlContent)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(mdP), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(mdP, []byte(joinFrontBody(front, md)), 0o600); err != nil {
			return err
		}
		meta, _ := json.Marshal(recoveryMeta{Path: origPath, Time: time.Now().Format(time.RFC3339)})
		return os.WriteFile(metaP, meta, 0o600)
	})

	// appRecoveryResolve: called once at startup. If a recovery pair exists,
	// shows a native Restore/Discard dialog; returns the recovered document
	// (with its ORIGINAL path, so Save targets the right file) on restore,
	// nil otherwise. The recovery files are kept after a restore -- the
	// content is still unsaved -- and cleared on discard.
	w.Bind("appRecoveryResolve", func() (*document, error) {
		mdP, metaP := recoveryPaths()
		if mdP == "" {
			return nil, nil
		}
		raw, err := os.ReadFile(mdP)
		if err != nil {
			return nil, nil // no recovery pending
		}
		var meta recoveryMeta
		if b, err := os.ReadFile(metaP); err == nil {
			_ = json.Unmarshal(b, &meta)
		}
		what := "an untitled document"
		if meta.Path != "" {
			what = filepath.Base(meta.Path)
		}
		when := ""
		if t, err := time.Parse(time.RFC3339, meta.Time); err == nil {
			when = " from " + t.Local().Format("Jan 2 15:04")
		}
		err = zenity.Question(
			"Unsaved changes"+when+" were recovered for "+what+".\nRestore them?",
			zenity.Title("Recover Unsaved Changes"),
			zenity.OKLabel("Restore"),
			zenity.CancelLabel("Discard"),
			zenity.QuestionIcon,
		)
		if err != nil {
			clearRecovery() // discarded (or dialog unavailable): don't re-ask forever
			return nil, nil
		}
		front, body := splitFrontMatter(raw)
		h, err := mdToHTML(body)
		if err != nil {
			return nil, err
		}
		return &document{Path: meta.Path, HTML: h, Front: front}, nil
	})

	// appRecoveryClear: deliberate data disposal ("Don't Save", Reload)
	// also disposes of the recovery snapshot.
	w.Bind("appRecoveryClear", func() {
		clearRecovery()
	})

	// appValeCheck: run the vendored Vale over the submitted plain text.
	// Exit code 1 means "alerts found" (success for our purposes); 2+ is a
	// real failure.
	w.Bind("appValeCheck", func(text string) ([]valeFinding, error) {
		bin, ini, err := ensureVale()
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(bin, "--output=JSON", "--config", ini, "--ext=.txt")
		cmd.Stdin = strings.NewReader(text)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
				return nil, fmt.Errorf("vale failed: %v: %s", err, strings.TrimSpace(stderr.String()))
			}
		}
		var out map[string][]struct {
			Check    string
			Message  string
			Line     int
			Span     [2]int
			Severity string
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			return nil, fmt.Errorf("vale output not parseable: %w", err)
		}
		res := []valeFinding{}
		for _, alerts := range out {
			for _, a := range alerts {
				res = append(res, valeFinding{Line: a.Line, Span: a.Span, Message: a.Message, Severity: a.Severity, Check: a.Check})
			}
		}
		return res, nil
	})

	// appGetTheme / appSetTheme: theme preference, a state.json tenant.
	// "system" (the default when unset) means follow prefers-color-scheme.
	w.Bind("appGetTheme", func() string {
		if appStateData.Theme == "" {
			return "system"
		}
		return appStateData.Theme
	})
	w.Bind("appSetTheme", func(t string) {
		if t != "light" && t != "dark" && t != "system" {
			return
		}
		appStateData.Theme = t
		saveState()
	})

	// appGetShortcodeSyntaxes: the configured SSG flavor(s), driving both
	// the checkers' notation blanking and the Insert menu's bracket choice.
	w.Bind("appGetShortcodeSyntaxes", func() []string {
		return loadConfig().Shortcodes.Syntaxes
	})

	// appGetCodeFontSize: [editor] code_font_size from config.toml.
	w.Bind("appGetCodeFontSize", func() int {
		return loadConfig().Editor.CodeFontSize
	})

	// appSetWindowSize: the UI calls this once at startup after measuring
	// the screen, shrinking the window when the default would run off the
	// display. Never grows beyond the defaults; floors keep it usable.
	w.Bind("appSetWindowSize", func(width, height int) {
		if width < 600 {
			width = 600
		}
		if height < 400 {
			height = 400
		}
		w.SetSize(width, height, webview.HintNone)
	})

	// appGetAudience: [editor] audience, calibrating readability bands.
	w.Bind("appGetAudience", func() string {
		return loadConfig().Editor.Audience
	})

	// appValidateYAML: parser-grade validation for the metadata dialog.
	// Advisory only -- the dialog shows the error and lets the author
	// apply anyway (free-form ethos: informed, not gatekept).
	w.Bind("appValidateYAML", func(src string) string {
		var out interface{}
		if err := yaml.Unmarshal([]byte(src), &out); err != nil {
			return err.Error()
		}
		return ""
	})

	// appLoadNotes / appSaveNotes: the notes sidecar. Empty content
	// removes the file (no husk left beside an un-annotated document).
	w.Bind("appLoadNotes", func(docPath string) string {
		if docPath == "" {
			return ""
		}
		b, err := os.ReadFile(notesPathFor(docPath))
		if err != nil {
			return ""
		}
		return string(b)
	})
	w.Bind("appSaveNotes", func(docPath, content string) error {
		if docPath == "" {
			return nil
		}
		p := notesPathFor(docPath)
		if strings.TrimSpace(content) == "" {
			_ = os.Remove(p)
			return nil
		}
		return os.WriteFile(p, []byte(content), 0o644)
	})

	// appVersion: reports the application version for Help > About.
	// appQuit: graceful exit for File > Exit. Dispatch onto the main
	// loop so Terminate is not called from inside a binding callback.
	w.Bind("appQuit", func() {
		w.Dispatch(func() { w.Terminate() })
	})

	w.Bind("appVersion", func() string {
		return appVersion
	})

	// appSetTitle: lets the UI set the window title (e.g. current filename).
	w.Bind("appSetTitle", func(title string) {
		w.Dispatch(func() { w.SetTitle(title) })
	})

	// appLoad: (re)loads a known path without showing a dialog. Used by the
	// toolbar Reload action to re-read the current file from disk.
	w.Bind("appLoad", func(path string) (*document, error) {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("no file to reload")
		}
		return loadDocument(path)
	})

	// appOpenURL: opens a link in the system default browser (Ctrl+Click).
	// Only http(s) is allowed: an opened .md file is untrusted input, and
	// xdg-open on arbitrary schemes/paths would let a document trigger
	// unexpected local handlers.
	w.Bind("appOpenURL", func(raw string) error {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid URL: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("only http(s) links can be opened in the browser")
		}
		return exec.Command("xdg-open", u.String()).Start()
	})

	// appConfirmDiscard: native three-way dialog shown before an action that
	// would discard unsaved changes. Returns "save", "discard", or "cancel".
	w.Bind("appConfirmDiscard", func() (string, error) {
		err := zenity.Question(
			"The document has unsaved changes.\nDo you want to save them?",
			zenity.Title("Unsaved Changes"),
			zenity.OKLabel("Save"),
			zenity.ExtraButton("Don't Save"),
			zenity.CancelLabel("Cancel"),
			zenity.QuestionIcon,
		)
		switch {
		case err == nil:
			return "save", nil
		case errors.Is(err, zenity.ErrExtraButton):
			return "discard", nil
		case errors.Is(err, zenity.ErrCanceled):
			return "cancel", nil
		}
		return "", err
	})

	// Serve the embedded UI + vendored assets on a random loopback port and
	// navigate to it. The server exposes only embedded static files; nothing
	// from the filesystem. If listening fails, fall back to SetHtml: the
	// editor still works, but module-based tools (Harper) are unavailable.
	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		go func() { _ = http.Serve(ln, http.FileServer(http.FS(assets))) }()
		w.Navigate(fmt.Sprintf("http://%s/editor.html", ln.Addr().String()))
	} else {
		w.SetHtml(editorHTML)
	}
	w.Run()
}
