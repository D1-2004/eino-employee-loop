package i18n

import "github.com/D1-2004/eino-employee-loop/contract"

// ToolDescriptions returns copies of specs with Description resolved from
// DescriptionKey for the locale. Specs that already carry a description keep
// it (Host-authored text wins).
func (b *Bundle) ToolDescriptions(locale string, specs []contract.ToolSpec) []contract.ToolSpec {
	out := make([]contract.ToolSpec, len(specs))
	for i, s := range specs {
		if s.Description == "" && s.DescriptionKey != "" {
			s.Description = b.Text(locale, s.DescriptionKey)
		}
		out[i] = s
	}
	return out
}

// EventSummary returns the localized one-line description of an event type.
func (b *Bundle) EventSummary(locale string, spec contract.TypeSpec) string {
	return b.Text(locale, spec.SummaryKey)
}

// ContractKeys lists every key the contract catalogs and labels need. Each
// shipped locale must define all of them.
func ContractKeys(types []contract.TypeSpec, tools []contract.ToolSpec) []string {
	keys := []string{}
	for _, c := range contract.Classes {
		keys = append(keys, "class."+string(c))
	}
	for _, m := range []contract.Mode{contract.ModeTriggered, contract.ModeListening} {
		keys = append(keys, "mode."+string(m))
	}
	for _, l := range []contract.Lane{contract.LaneForeground, contract.LaneBackground, contract.LaneHost} {
		keys = append(keys, "lane."+string(l))
	}
	for _, s := range []contract.SessionKind{contract.SessionInteractive, contract.SessionObservation, contract.SessionFollowUp, contract.SessionRoutine, contract.SessionLifecycle} {
		keys = append(keys, "session."+string(s))
	}
	for _, s := range []string{contract.ScopeUserSaid, contract.ScopeEmployeeSaid, contract.ScopeHostDigest} {
		keys = append(keys, "evidence."+s)
	}
	for _, t := range types {
		keys = append(keys, t.SummaryKey)
	}
	for _, t := range tools {
		if t.DescriptionKey != "" {
			keys = append(keys, t.DescriptionKey)
		}
	}
	return keys
}
