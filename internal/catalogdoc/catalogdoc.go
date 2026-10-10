// Package catalogdoc renders the event-type and tool catalogs as Markdown:
// the standard catalogs, then optionally a platform's extension profile
// (the committed documents render the standard catalogs only). Every word of the
// output comes from the contract catalogs and the i18n packs, so
// docs/catalog*.md cannot drift from the code: a test compares the
// committed files with Render.
package catalogdoc

import (
	"sort"
	"strconv"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Dir is the module-relative directory of the generated documents.
const Dir = "docs"

// FileName returns the document name for a locale: catalog.md for the
// fallback locale, catalog.<locale>.md otherwise.
func FileName(locale string) string {
	loc := i18n.Canonical(locale)
	if loc == "" || loc == i18n.Fallback {
		return "catalog.md"
	}
	return "catalog." + loc + ".md"
}

// Command returns the command line that regenerates the document.
func Command(locale string) string {
	loc := i18n.Canonical(locale)
	if loc == "" {
		loc = i18n.Fallback
	}
	return "go run ./cmd/catalog-doc -locale " + loc + " -o " + Dir + "/" + FileName(loc)
}

// Profile is one set of catalogs: event types and tools.
type Profile struct {
	Types []contract.TypeSpec
	Tools []contract.ToolSpec
}

// StandardProfile returns the standard catalogs embedded in package
// contract: what the module implements.
func StandardProfile() (Profile, error) {
	tools, err := contract.StandardTools()
	if err != nil {
		return Profile{}, err
	}
	return Profile{Types: contract.StandardCatalog().Types(), Tools: tools}, nil
}

// Standard renders the embedded catalogs with the embedded packs.
func Standard(locale string) (string, error) {
	std, err := StandardProfile()
	if err != nil {
		return "", err
	}
	return Render(Messages(), locale, std, Profile{}), nil
}

// Render returns the Markdown document for one locale: the standard
// profile, then a platform's extension profile when it is not empty (b
// must then hold the extension's pack too). Types are
// rendered grouped by class in the given order; tools in the given order.
func Render(b *i18n.Bundle, locale string, standard, extension Profile) string {
	r := renderer{b: b, locale: locale}
	r.line("<!-- " + r.t("catalogdoc.generated", "command", Command(locale)) + " -->")
	r.line("")
	r.line("# " + r.t("catalogdoc.title"))
	r.line("")
	r.line(r.t("catalogdoc.intro"))
	r.line("")
	r.legend()
	r.events("##", standard.Types)
	r.tools("##", "catalogdoc.tools", standard.Tools)
	if len(extension.Types)+len(extension.Tools) > 0 {
		r.line("## " + r.t("catalogdoc.extensions.title"))
		r.line("")
		r.line(r.t("catalogdoc.extensions.intro"))
		r.line("")
		if len(extension.Types) > 0 {
			r.events("###", extension.Types)
		}
		if len(extension.Tools) > 0 {
			r.tools("###", "catalogdoc.extensions.tools", extension.Tools)
		}
	}
	return strings.TrimRight(r.sb.String(), "\n") + "\n"
}

type renderer struct {
	b      *i18n.Bundle
	locale string
	sb     strings.Builder
}

func (r *renderer) t(key string, args ...string) string { return r.b.Text(r.locale, key, args...) }

func (r *renderer) line(s string) { r.sb.WriteString(s + "\n") }

func (r *renderer) row(cells ...string) {
	for i, c := range cells {
		cells[i] = cell(c)
	}
	r.line("| " + strings.Join(cells, " | ") + " |")
}

func (r *renderer) header(cells ...string) {
	r.row(cells...)
	seps := make([]string, len(cells))
	for i := range seps {
		seps[i] = "---"
	}
	r.line("| " + strings.Join(seps, " | ") + " |")
}

// cell makes text safe inside a Markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

func code(s string) string { return "`" + s + "`" }

func codes(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = code(x)
	}
	return strings.Join(out, ", ")
}

func (r *renderer) legend() {
	r.line("## " + r.t("catalogdoc.legend.title"))
	r.line("")
	r.header(r.t("catalogdoc.legend.dimension"), r.t("catalogdoc.legend.value"), r.t("catalogdoc.legend.meaning"))
	for _, c := range contract.Classes {
		r.row(r.t("catalogdoc.dimension.class"), code(string(c)), r.t("class."+string(c)))
	}
	for _, m := range []contract.Mode{contract.ModeTriggered, contract.ModeListening} {
		r.row(r.t("catalogdoc.dimension.mode"), code(string(m)), r.t("mode."+string(m)))
	}
	for _, l := range []contract.Lane{contract.LaneForeground, contract.LaneBackground, contract.LaneHost} {
		r.row(r.t("catalogdoc.dimension.lane"), code(string(l)), r.t("lane."+string(l)))
	}
	for _, s := range []contract.SessionKind{contract.SessionInteractive, contract.SessionObservation, contract.SessionFollowUp, contract.SessionRoutine, contract.SessionLifecycle} {
		r.row(r.t("catalogdoc.dimension.session"), code(string(s)), r.t("session."+string(s)))
	}
	r.line("")
}

func (r *renderer) events(level string, types []contract.TypeSpec) {
	r.line(level + " " + r.t("catalogdoc.events.title"))
	r.line("")
	r.line(r.t("catalogdoc.events.intro") + " " + r.t("catalogdoc.events.count", "count", strconv.Itoa(len(types))))
	r.line("")
	byClass := map[contract.Class][]contract.TypeSpec{}
	var extra []contract.Class
	for _, s := range types {
		if _, ok := byClass[s.Class]; !ok && !knownClass(s.Class) {
			extra = append(extra, s.Class)
		}
		byClass[s.Class] = append(byClass[s.Class], s)
	}
	for _, c := range append(append([]contract.Class(nil), contract.Classes...), extra...) {
		specs := byClass[c]
		if len(specs) == 0 {
			continue
		}
		heading := code(string(c))
		if r.b.Has(r.locale, "class."+string(c)) {
			heading = r.t("class."+string(c)) + " (" + heading + ")"
		}
		r.line(level + "# " + heading)
		r.line("")
		r.header(r.t("catalogdoc.col.type"), r.t("catalogdoc.col.mode"), r.t("catalogdoc.col.lane"),
			r.t("catalogdoc.col.session"), r.t("catalogdoc.col.options"), r.t("catalogdoc.col.summary"))
		for _, s := range specs {
			session := r.t("catalogdoc.none")
			if s.Session != "" {
				session = code(string(s.Session))
			}
			r.row(code(s.Type), r.t("mode."+string(s.Mode)), r.t("lane."+string(s.Lane)), session,
				r.options(s), r.t(s.SummaryKey))
		}
		r.line("")
	}
}

func knownClass(c contract.Class) bool {
	for _, k := range contract.Classes {
		if k == c {
			return true
		}
	}
	return false
}

func (r *renderer) options(s contract.TypeSpec) string {
	var opts []string
	if s.SessionKey != "" {
		opts = append(opts, r.t("catalogdoc.opt.session_key", "key", s.SessionKey))
	}
	if s.Preempts {
		opts = append(opts, r.t("catalogdoc.opt.preempts"))
	}
	if s.Preemptible {
		opts = append(opts, r.t("catalogdoc.opt.preemptible"))
	}
	if len(s.Revokes) > 0 {
		kinds := make([]string, len(s.Revokes))
		for i, k := range s.Revokes {
			kinds[i] = string(k)
		}
		opts = append(opts, r.t("catalogdoc.opt.revokes", "kinds", codes(kinds)))
	}
	if c := s.Coalesce; c != nil {
		opts = append(opts, r.t("catalogdoc.opt.coalesce", "quiet", strconv.FormatInt(c.QuietMs, 10),
			"wait", strconv.FormatInt(c.MaxWaitMs, 10), "items", strconv.Itoa(c.MaxItems)))
	}
	if !s.RequiresScene {
		opts = append(opts, r.t("catalogdoc.opt.no_scene"))
	}
	if len(opts) == 0 {
		return r.t("catalogdoc.none")
	}
	return strings.Join(opts, "; ")
}

// tools renders a tool table under the title and intro of prefix
// (prefix.title, prefix.intro).
func (r *renderer) tools(level, prefix string, tools []contract.ToolSpec) {
	r.line(level + " " + r.t(prefix+".title"))
	r.line("")
	r.line(r.t(prefix+".intro") + " " + r.t("catalogdoc.tools.count", "count", strconv.Itoa(len(tools))))
	r.line("")
	r.header(r.t("catalogdoc.col.tool"), r.t("catalogdoc.col.kind"), r.t("catalogdoc.col.terminal"),
		r.t("catalogdoc.col.offered"), r.t("catalogdoc.col.ports"), r.t("catalogdoc.col.parameters"),
		r.t("catalogdoc.col.description"))
	for _, s := range tools {
		kind := r.t("catalogdoc.kind." + string(s.Kind))
		if s.Exclusive {
			kind += "; " + r.t("catalogdoc.opt.exclusive")
		}
		terminal := r.t("catalogdoc.none")
		if s.Terminal != "" {
			terminal = code(string(s.Terminal))
		}
		desc := s.Description
		if desc == "" {
			desc = r.t(s.DescriptionKey)
		}
		ports := r.t("catalogdoc.none")
		if len(s.Ports) > 0 {
			ports = codes(s.Ports)
		}
		r.row(code(s.Name), kind, terminal, r.offered(s), ports, r.params(s.Parameters), desc)
	}
	r.line("")
}

func (r *renderer) offered(s contract.ToolSpec) string {
	lanes := make([]string, len(s.Lanes))
	for i, l := range s.Lanes {
		lanes[i] = string(l)
	}
	sessions := make([]string, len(s.Sessions))
	for i, k := range s.Sessions {
		sessions[i] = string(k)
	}
	where := r.t("catalogdoc.none")
	if len(lanes) > 0 {
		where = codes(lanes)
	}
	if len(sessions) > 0 {
		return where + " / " + codes(sessions)
	}
	return where + " / " + r.t("catalogdoc.any_session")
}

// params lists required parameters in schema order, then optional ones
// sorted by name with a trailing "?".
func (r *renderer) params(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	var out []string
	if req, ok := schema["required"].([]any); ok {
		for _, v := range req {
			if name, ok := v.(string); ok {
				required[name] = true
				out = append(out, code(name))
			}
		}
	}
	var optional []string
	for name := range props {
		if !required[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	for _, name := range optional {
		out = append(out, code(name+"?"))
	}
	if len(out) == 0 {
		return r.t("catalogdoc.none")
	}
	return strings.Join(out, ", ")
}

// Check reports the keys a locale needs and does not define in its own
// pack (fallback not applied).
func Check(b *i18n.Bundle, locale string, types []contract.TypeSpec, tools []contract.ToolSpec) []string {
	keys := map[string]bool{}
	for _, k := range b.Keys(i18n.Fallback) {
		if strings.HasPrefix(k, "catalogdoc.") {
			keys[k] = true
		}
	}
	for _, k := range i18n.ContractKeys(types, tools) {
		keys[k] = true
	}
	own := map[string]bool{}
	for _, k := range b.Keys(locale) {
		own[k] = true
	}
	var missing []string
	for k := range keys {
		if !own[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}
