package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func hasLoop(names []string, name string) bool {
	for _, v := range names {
		if v == name {
			return true
		}
	}
	return false
}
func (s *Service) renderResult(ctx context.Context, task work.Task, goal work.Goal, runID string) (string, error) {
	var profile contract.Profile
	if err := json.Unmarshal(task.Definition.Configuration, &profile); err != nil {
		return "", err
	}
	frame, err := s.opts.Context.Build(ctx, task.Scope, contextbuilder.Execution, "", task.ID)
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
		raw, ref, e := s.opts.Context.Source(ctx, task.Scope, source.Receipt)
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
		d, e := s.opts.Artifacts.Read(ctx, a.Path)
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
		old, e := s.opts.Artifacts.Read(ctx, path)
		var pkg storage.File
		if e == nil {
			if old.Body != content {
				return "", storage.ErrConflict
			}
			pkg = old
		} else if errors.Is(e, fs.ErrNotExist) {
			pkg, e = s.opts.Artifacts.Write(ctx, path, "", content)
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
func (s *Service) compose(ctx context.Context, p contract.Profile, scope work.Scope, role, receipt string, frame contextbuilder.Frame, policy, name, field, fallback string) (string, error) {
	frame.Gaps = append(frame.Gaps, "Verified prior result: "+fallback)
	var text string
	fn := &kernel.Function{Spec: kernel.Spec(name, "Propose a source-grounded delivery. This does not send it; the outbox verifies the receipt.", map[string]any{field: kernel.StringField()}, field), Before: func(c context.Context) error { return s.opts.Context.Revalidate(c, frame) }, Invoke: func(_ context.Context, _ string, a map[string]any) (any, error) {
		text = kernel.Str(a, field)
		return map[string]string{"validated_text": text}, nil
	}}
	out, err := s.opts.Runner.Run(ctx, kernel.Invocation{Profile: p, Scope: scope, Role: role, Receipt: receipt, Context: frame, System: policy, Tools: []*kernel.Function{fn}, Terminal: map[string]bool{name: true}, Check: func(c context.Context) error { return s.opts.Context.Revalidate(c, frame) }})
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
func (s *Service) recordDelivery(ctx context.Context, scope work.Scope, record Record) error {
	if record.State != "applied" || record.Receipt == nil || record.Intent.TaskID == "" {
		return nil
	}
	fact := work.DeliveryFact{IntentID: record.Intent.ID, ProviderID: record.Receipt.ProviderMessageID, BodySHA256: record.Receipt.BodySHA256, Kind: record.Intent.Kind, GoalRevision: record.Intent.GoalRevision, Stage: string(record.Receipt.Stage), Transport: record.Receipt.Transport, IntentSHA256: record.Receipt.IntentSHA256, TargetSHA256: record.Receipt.TargetSHA256}
	_, err := s.opts.Tasks.RecordDelivery(ctx, scope, record.Intent.TaskID, fact)
	return err
}

func (s *Service) ValidateDelivery(ctx context.Context, in Intent) error {
	if in.Target != nil && in.Target.Transport != "local-file" {
		if s.opts.AuthorizeDelivery == nil {
			return work.ErrScope
		}
		if err := s.opts.AuthorizeDelivery(ctx, in); err != nil {
			return err
		}
	}

	_, _, err := s.opts.Context.Source(ctx, in.Scope, in.SourceReceipt)
	if errors.Is(err, contextbuilder.ErrRevoked) || errors.Is(err, storage.ErrNotFound) {
		return ErrObsolete
	}
	if err != nil {
		return err
	}
	if in.Kind == "question" {
		if in.TaskID != "" {
			task, e := s.opts.Tasks.Get(ctx, in.Scope, in.TaskID)
			if e != nil {
				return e
			}
			if task.State != work.Waiting || task.Goal.Revision != in.GoalRevision {
				return ErrObsolete
			}
			for _, w := range task.Waits {
				if w.ID == in.ID && !w.Resolved {
					return nil
				}
			}
			return ErrObsolete
		}
		if s.opts.QuestionCheck == nil {
			return ErrObsolete
		}
		return s.opts.QuestionCheck(ctx, in)
	}
	return nil
}
