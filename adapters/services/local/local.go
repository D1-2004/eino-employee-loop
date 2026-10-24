package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	host "github.com/D1-2004/eino-employee-loop/ports/services"
	"io/fs"
	"path"
	"strings"

	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

type local struct {
	opts     host.Bindings
	services host.Services
}

// NewLocal supplies read-only services over the existing admitted source and
// Task stores. It neither owns nor closes those dependencies.
func NewLocal(opts host.Bindings) (host.Services, error) {
	if opts.Authorize == nil || opts.Sources == nil ||
		opts.Config == nil && opts.Overrides.Config == nil ||
		(opts.Tasks == nil || opts.Artifacts == nil) && (opts.Overrides.Resources == nil || opts.Overrides.Files == nil) {
		return host.Services{}, host.ErrInvalid
	}
	l := &local{opts: opts}
	s := host.Services{Config: l, Messages: l, Resources: l, Tools: l, Files: l}
	if opts.Overrides.Config != nil {
		s.Config = opts.Overrides.Config
	}
	if opts.Overrides.Messages != nil {
		s.Messages = opts.Overrides.Messages
	}
	if opts.Overrides.Resources != nil {
		s.Resources = opts.Overrides.Resources
	}
	if opts.Overrides.Files != nil {
		s.Files = opts.Overrides.Files
	}
	if opts.Overrides.Tools != nil {
		s.Tools = opts.Overrides.Tools
	}
	l.services = s
	return s, nil
}
func (l *local) check(ctx context.Context, c host.Call) error {
	if _, err := c.Scope.Key(); err != nil {
		return err
	}
	if c.RunID != "" && c.TaskID == "" {
		return host.ErrInvalid
	}
	if len(c.Binding) != 0 {
		var v map[string]json.RawMessage
		if json.Unmarshal(c.Binding, &v) != nil || v == nil {
			return host.ErrInvalid
		}
	}
	if err := l.opts.Authorize(ctx, c); err != nil {
		return err
	}
	if c.SourceReceipt != "" {
		if _, _, err := l.opts.Sources.Source(ctx, c.Scope, c.SourceReceipt); err != nil {
			return err
		}
	}
	return nil
}
func (l *local) GetConfig(ctx context.Context, c host.Call) (host.ConfigSnapshot, error) {
	if err := l.check(ctx, c); err != nil {
		return host.ConfigSnapshot{}, err
	}
	v, err := l.opts.Config(ctx, c)
	if err != nil {
		return host.ConfigSnapshot{}, err
	}
	if v.ID == "" || v.Version == "" || !json.Valid(v.Value) {
		return host.ConfigSnapshot{}, host.ErrInvalid
	}
	v.Value = bytes.Clone(v.Value)
	if err = l.check(ctx, c); err != nil {
		return host.ConfigSnapshot{}, err
	}
	return v, nil
}
func (l *local) GetMessages(ctx context.Context, c host.Call, q host.MessageRequest) (host.Messages, error) {
	if err := l.check(ctx, c); err != nil {
		return host.Messages{}, err
	}
	if len(q.IDs) == 0 || len(q.IDs) > 100 {
		return host.Messages{}, host.ErrInvalid
	}
	out := host.Messages{Items: []contextbuilder.Source{}, Coverage: host.MessageCoverage{Source: "admitted_local_sources", Complete: true, MissingIDs: []string{}}}
	seen := map[string]bool{}
	for _, id := range q.IDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return host.Messages{}, host.ErrInvalid
		}
		seen[id] = true
		s, _, err := l.opts.Sources.Source(ctx, c.Scope, id)
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			out.Coverage.MissingIDs = append(out.Coverage.MissingIDs, id)
			continue
		}
		if err != nil {
			return host.Messages{}, err
		}
		out.Items = append(out.Items, s)
	}
	if err := l.check(ctx, c); err != nil {
		return host.Messages{}, err
	}
	// Recheck every returned source before releasing the batch.
	for _, s := range out.Items {
		current, _, err := l.opts.Sources.Source(ctx, c.Scope, s.ID)
		if err != nil {
			return host.Messages{}, err
		}
		if current != s {
			return host.Messages{}, contextbuilder.ErrRevoked
		}
	}
	return out, nil
}
func (l *local) task(ctx context.Context, c host.Call) (work.Task, []work.Run, error) {
	if c.TaskID == "" {
		return work.Task{}, nil, host.ErrUnsupported
	}
	t, err := l.opts.Tasks.Get(ctx, c.Scope, c.TaskID)
	if err != nil {
		return work.Task{}, nil, err
	}
	if t.Scope != c.Scope || t.ID != c.TaskID {
		return work.Task{}, nil, work.ErrScope
	}
	if c.RunID == "" {
		return t, t.Runs, nil
	}
	for _, r := range t.Runs {
		if r.ID == c.RunID {
			return t, []work.Run{r}, nil
		}
	}
	return work.Task{}, nil, host.ErrInvalid
}
func validArtifact(t work.Task, a work.Artifact) bool {
	prefix := "tasks/" + contextbuilder.OwnerKey(t.Scope) + "/" + t.ID + "/"
	return strings.HasPrefix(a.Path, prefix) && len(a.Path) > len(prefix) && path.Clean(a.Path) == a.Path && !strings.Contains(a.Path, "\\") && len(a.SHA256) == 64 && a.Bytes >= 0
}
func (l *local) ListFiles(ctx context.Context, c host.Call) (host.Files, error) {
	if err := l.check(ctx, c); err != nil {
		return host.Files{}, err
	}
	t, runs, err := l.task(ctx, c)
	if err != nil {
		return host.Files{}, err
	}
	out := host.Files{Items: []host.ResourceRef{}, Complete: true}
	for _, r := range runs {
		for _, a := range r.Artifacts {
			if !validArtifact(t, a) {
				return host.Files{}, host.ErrInvalid
			}
			f, err := l.opts.Artifacts.Read(ctx, a.Path)
			if err != nil {
				return host.Files{}, err
			}
			if f.Path != a.Path || f.SHA256 != a.SHA256 || contextbuilder.Digest([]byte(f.Body)) != a.SHA256 || int64(len(f.Body)) != a.Bytes {
				return host.Files{}, host.ErrChanged
			}
			out.Items = append(out.Items, host.ResourceRef{TaskID: t.ID, RunID: r.ID, Path: a.Path, Version: a.SHA256, SHA256: a.SHA256})
		}
	}
	if err = l.check(ctx, c); err != nil {
		return host.Files{}, err
	}
	return out, nil
}
func (l *local) ReadResource(ctx context.Context, c host.Call, ref host.ResourceRef) (host.Resource, error) {
	if ref.TaskID != c.TaskID || c.TaskID == "" || ref.RunID == "" || c.RunID != "" && c.RunID != ref.RunID || ref.Version == "" || ref.Version != ref.SHA256 {
		return host.Resource{}, host.ErrInvalid
	}
	q := c
	q.RunID = ref.RunID
	files, err := l.ListFiles(ctx, q)
	if err != nil {
		return host.Resource{}, err
	}
	found := false
	for _, f := range files.Items {
		if f == ref {
			found = true
			break
		}
	}
	if !found {
		return host.Resource{}, host.ErrChanged
	}
	f, err := l.opts.Artifacts.Read(ctx, ref.Path)
	if err != nil {
		return host.Resource{}, err
	}
	if f.Path != ref.Path || f.SHA256 != ref.SHA256 || contextbuilder.Digest([]byte(f.Body)) != ref.SHA256 {
		return host.Resource{}, host.ErrChanged
	}
	if err = l.check(ctx, c); err != nil {
		return host.Resource{}, err
	}
	return host.Resource{Ref: ref, Content: []byte(f.Body)}, nil
}
