package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/work"
)

func hasLoop(names []string, name string) bool {
	for _, v := range names {
		if v == name {
			return true
		}
	}
	return false
}
func (s *standalone) renderResult(ctx context.Context, task work.Task, goal work.Goal, runID string) (string, error) {
	var profile Profile
	if err := json.Unmarshal(task.Definition.Configuration, &profile); err != nil {
		return "", err
	}
	frame, err := s.context.Build(ctx, task.Scope, contextbuilder.Execution, "", task.ID)
	if err != nil {
		return "", err
	}
	// A historical completion event supplies the original Task snapshot even if a
	// later human command has opened a successor Goal before delivery ran.
	frame.Tasks = []work.Task{task}
	frame.Sources = nil
	frame.Refs = nil
	seen := map[string]bool{}
	for _, source := range task.Sources {
		if seen[source.Receipt] {
			continue
		}
		raw, ref, e := s.context.Source(ctx, task.Scope, source.Receipt)
		if e != nil {
			return "", e
		}
		if raw.Actor != source.Actor || raw.Text != source.Text {
			return "", work.ErrScope
		}
		frame.Sources = append(frame.Sources, raw)
		frame.Refs = append(frame.Refs, ref)
		seen[source.Receipt] = true
	}
	artifacts := []work.Artifact{}
	for _, r := range task.Runs {
		if r.ID == runID {
			artifacts = r.Artifacts
		}
	}
	for _, a := range artifacts {
		d, e := s.files.Read(ctx, a.Path)
		if e != nil || d.SHA256 != a.SHA256 {
			return "", errors.New("delivery: artifact missing or changed")
		}
		frame.Materials = append(frame.Materials, d)
	}
	body := goal.Summary
	receipt := "result-" + contextbuilder.Digest(encode([]any{task.ID, goal.Revision, runID}))
	if hasLoop(profile.DeliveryLoops, "package") {
		summary, e := s.compose(ctx, profile, task.Scope, "package", receipt, frame, "Package the verified execution artifacts for the original human. Preserve actual file identities, original constraints, method, unresolved gaps and delivery scope. Do not invent a file or completed action.", "publish_package", "summary", body)
		if e != nil {
			return "", e
		}
		content := "# Delivery package\n\n" + summary + "\n\n## Verified files\n"
		for _, a := range artifacts {
			content += "\n- " + a.Path + " · SHA256 " + a.SHA256 + fmt.Sprintf(" · %d bytes", a.Bytes)
		}
		path := "packages/" + contextbuilder.OwnerKey(task.Scope) + "/" + receipt + ".md"
		old, e := s.files.Read(ctx, path)
		var pkg markdown.Document
		if e == nil {
			if old.Body != content {
				return "", storage.ErrConflict
			}
			pkg = old
		} else if errors.Is(e, fs.ErrNotExist) {
			pkg, e = s.files.Write(ctx, path, "", content)
			if e != nil {
				return "", e
			}
		} else {
			return "", e
		}
		artifacts = append(artifacts, work.Artifact{Path: pkg.Path, SHA256: pkg.SHA256, Bytes: int64(len(pkg.Body))})
		frame.Materials = append(frame.Materials, pkg)
		body = summary
	}
	if hasLoop(profile.DeliveryLoops, "expression") {
		text, e := s.compose(ctx, profile, task.Scope, "expression", receipt, frame, "Express this verified business completion to the original human using current Tone context where available. State only evidenced results and any gaps. The Host appends verified artifact links and hashes; do not fabricate platform receipts.", "publish_result", "text", body)
		if e != nil {
			return "", e
		}
		body = text
	}
	for _, a := range artifacts {
		body += "\n\n[" + a.Path + "](" + a.Path + ") · SHA256 " + a.SHA256
	}
	return body, nil
}
func (s *standalone) compose(ctx context.Context, p Profile, scope work.Scope, role, receipt string, frame contextbuilder.Frame, policy, name, field, fallback string) (string, error) {
	frame.Gaps = append(frame.Gaps, "Verified prior result: "+fallback)
	var text string
	fn := &function{spec: spec(name, "Propose a source-grounded delivery. This does not send it; the outbox verifies the receipt.", map[string]any{field: stringField()}, field), before: func(c context.Context) error { return s.context.Revalidate(c, frame) }, run: func(_ context.Context, _ string, a map[string]any) (any, error) {
		text = str(a, field)
		return map[string]string{"validated_text": text}, nil
	}}
	out, err := s.runAgent(ctx, p, scope, role, receipt, frame, policy, []*function{fn}, map[string]bool{name: true}, false)
	if err != nil {
		return "", err
	}
	if text == "" && out.Output != nil {
		var result struct {
			Text string `json:"validated_text"`
		}
		if json.Unmarshal([]byte(out.Output.Content), &result) == nil {
			text = result.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("delivery: missing validated expression")
	}
	return text, nil
}

// recordDelivery makes the verified transport fact visible to the original
// Task and its event Wait/Review, without granting a second Goal owner.
func (s *standalone) recordDelivery(ctx context.Context, scope work.Scope, record interaction.Record) error {
	if record.State != "applied" || record.Receipt == nil || record.Intent.TaskID == "" {
		return nil
	}
	fact := work.DeliveryFact{IntentID: record.Intent.ID, ProviderID: record.Receipt.ProviderMessageID, BodySHA256: record.Receipt.BodySHA256, Kind: record.Intent.Kind, GoalRevision: record.Intent.GoalRevision}
	_, err := s.work.RecordDelivery(ctx, scope, record.Intent.TaskID, fact)
	return err
}

func (s *standalone) validateDelivery(ctx context.Context, in interaction.Intent) error {
	_, _, err := s.context.Source(ctx, in.Scope, in.SourceReceipt)
	if errors.Is(err, contextbuilder.ErrRevoked) || errors.Is(err, storage.ErrNotFound) {
		return interaction.ErrObsolete
	}
	if err != nil {
		return err
	}
	if in.Kind == "question" {
		if in.TaskID != "" {
			task, e := s.work.Get(ctx, in.Scope, in.TaskID)
			if e != nil {
				return e
			}
			if task.State != work.Waiting || task.Goal.Revision != in.GoalRevision {
				return interaction.ErrObsolete
			}
			for _, w := range task.Waits {
				if w.ID == in.ID && !w.Resolved {
					return nil
				}
			}
			return interaction.ErrObsolete
		}
		docs, e := s.db.List(ctx, "entry-jobs", s.opts.Profile.Key())
		if e != nil {
			return e
		}
		for _, d := range docs {
			var j job
			if e = json.Unmarshal(d.Body, &j); e != nil {
				return e
			}
			if j.Input.ID != in.SourceReceipt || j.Input.Scene != in.Scope.Scene {
				continue
			}
			if j.State == "interrupted" {
				return nil
			}
			return interaction.ErrObsolete
		}
		return interaction.ErrObsolete
	}
	return nil
}
