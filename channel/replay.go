// Package channel adapts explicit local input to Entry. Replay never connects
// to DingTalk and carries no proof of real IM authentication or delivery.
package channel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/D1-2004/eino-employee-loop/entry"
	"github.com/D1-2004/eino-employee-loop/internal/jsoncheck"
)

const ReplayVersion = "local-dws-replay/1"

// ReplayRecord is the portable, versioned simulation format. Native stores the
// original record for evidence; neither Actor nor Scene is inferred by a model.
type ReplayRecord struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Scene   string `json:"scene"`
	Actor   string `json:"actor"`
	Text    string `json:"text"`
}

type ReplayOptions struct {
	Owner  string
	Scenes []string
	// NativeOwner and NativeScenes explicitly map a previously captured DWS
	// record into the local profile. Empty mappings require exact equality.
	NativeOwner  string
	NativeScenes map[string]string
}

// Replay validates each line and calls accept in order, preserving duplicate
// message IDs so the durable Entry can distinguish replay from conflicting data.
// On a later bad line, already accepted lines remain accepted and recoverable.
func Replay(r io.Reader, opts ReplayOptions, accept func(entry.Input) error) (int, error) {
	if opts.Owner == "" || len(opts.Scenes) == 0 || accept == nil {
		return 0, errors.New("replay: owner, scenes and accept required")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	count, line := 0, 0
	for sc.Scan() {
		line++
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		in, ok, err := DecodeReplay(sc.Bytes(), opts)
		if err != nil {
			return count, fmt.Errorf("replay line %d: %w", line, err)
		}
		if !ok {
			continue
		}
		if err = accept(in); err != nil {
			return count, fmt.Errorf("replay line %d: %w", line, err)
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return count, fmt.Errorf("replay line %d: %w", line+1, err)
	}
	return count, nil
}

func DecodeReplay(raw []byte, opts ReplayOptions) (entry.Input, bool, error) {
	if err := jsoncheck.Unique(raw, 32); err != nil {
		return entry.Input{}, false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return entry.Input{}, false, errors.New("replay: JSON object required")
	}
	var in entry.Input
	if _, canonical := fields["version"]; canonical {
		var record ReplayRecord
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&record); err != nil {
			return in, false, err
		}
		if record.Version != ReplayVersion {
			return in, false, errors.New("replay: unsupported version")
		}
		in = entry.Input{ID: record.ID, Scene: record.Scene, Actor: record.Actor, Text: record.Text, Origin: "local-replay"}
	} else {
		// Captured `dws event +listen-im -f ndjson` transport envelope or flat
		// message record. Notifications are skipped; text messages fail closed
		// if their identity cannot be bound to the configured replay owner.
		var envelope map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&envelope); err != nil {
			return in, false, err
		}
		kind := field(envelope, "type", "event_type")
		message := envelope
		if data, ok := envelope["data"].(map[string]any); ok {
			message = data
		}
		if nested := field(message, "event_type", "type"); nested != "" {
			kind = nested
		}
		if kind != "" && !strings.Contains(kind, "message") {
			return in, false, nil
		}
		text := field(message, "content", "text")
		if strings.TrimSpace(text) == "" {
			return in, false, nil
		}
		nativeOwner := opts.NativeOwner
		if nativeOwner == "" {
			nativeOwner = opts.Owner
		}
		if field(message, "sender_open_dingtalk_id", "sender_id") != nativeOwner {
			return in, false, errors.New("replay: native actor does not match configured owner")
		}
		scene := field(message, "conversation_id", "open_conversation_id")
		if mapped, exists := opts.NativeScenes[scene]; exists {
			scene = mapped
		}
		in = entry.Input{ID: field(message, "message_id", "open_msg_id"), Scene: scene, Actor: opts.Owner, Text: text, Origin: "local-dws-capture-replay"}
	}
	if in.ID == "" || in.Scene == "" || in.Actor != opts.Owner || strings.TrimSpace(in.Text) == "" {
		return in, false, errors.New("replay: complete authorized message identity and text required")
	}
	allowed := false
	for _, scene := range opts.Scenes {
		if in.Scene == scene {
			allowed = true
			break
		}
	}
	if !allowed {
		return in, false, errors.New("replay: scene is outside the profile")
	}
	in.Native = append(json.RawMessage(nil), raw...)
	return in, true, nil
}

func field(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case json.Number:
			return v.String()
		}
	}
	return ""
}
