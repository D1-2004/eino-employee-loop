package main

import (
	"errors"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// loadPromptBundle is an explicit application boundary, not a template loader.
// Its literal artifact is frozen into the Profile before opening any state.
func loadPromptBundle(path string, profile *contract.Profile) error {
	if path == "" {
		return nil
	}
	if profile.PromptBundle != nil {
		return errors.New("prompt bundle: profile already selects a bundle; refusing an override")
	}
	var bundle contract.PromptBundle
	if err := readJSON(path, &bundle); err != nil {
		return err
	}
	selected := profile.Snapshot()
	selected.PromptBundle = &bundle
	if err := selected.Validate(); err != nil {
		return err
	}
	*profile = selected.Snapshot()
	return nil
}

// promptProvenance exposes only immutable artifact identities. It never prints
// the private instructions, template locations, endpoint or model credentials.
func promptProvenance(p contract.Profile) any {
	if p.PromptBundle == nil {
		return nil
	}
	b := p.PromptBundle
	return struct {
		Contract, SourceRevision, SourceManifestSHA256, CompilerVersion, ContentSHA256 string
	}{b.Contract, b.SourceRevision, b.SourceManifestSHA256, b.CompilerVersion, b.ContentSHA256}
}

// displayTasks changes only the CLI projection. The private database and SDK
// Task API retain the full accepted configuration needed for native recovery.
func displayTasks(tasks []work.Task) ([]work.Task, error) {
	return work.ModelTasks(tasks)
}
