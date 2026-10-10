package contract

import "fmt"

// StandardTools decodes the embedded standard tool catalog
// (catalog/tools.json): the tools package toolhost implements over the
// contract ports. Specs carry DescriptionKey only; resolve the text for the
// wake's locale (see i18n.Bundle.ToolDescriptions) before offering them.
func StandardTools() ([]ToolSpec, error) {
	doc, err := readCatalog("tools.json")
	if err != nil {
		return nil, err
	}
	return doc.Tools, checkTools(nil, doc.Tools)
}

// checkTools validates specs and rejects names already in prior or used
// twice.
func checkTools(prior, specs []ToolSpec) error {
	seen := map[string]bool{}
	for _, t := range prior {
		seen[t.Name] = true
	}
	for _, t := range specs {
		if err := t.Validate(); err != nil {
			return err
		}
		if seen[t.Name] {
			return fmt.Errorf("contract: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

// ToolsFor filters specs to those offered in a lane and session.
func ToolsFor(specs []ToolSpec, lane Lane, session SessionKind) []ToolSpec {
	out := make([]ToolSpec, 0, len(specs))
	for _, s := range specs {
		if s.OfferedIn(lane, session) {
			out = append(out, s)
		}
	}
	return out
}
