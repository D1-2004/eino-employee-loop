// Package jsoncheck validates immutable operator configuration and model JSON
// without silently overwriting duplicate object members.
package jsoncheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func Unique(raw []byte, maximumDepth int) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := value(d, 0, maximumDepth); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func value(d *json.Decoder, depth, maximum int) error {
	if depth > maximum {
		return fmt.Errorf("JSON depth exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate JSON member")
			}
			seen[name] = true
			if e = value(d, depth+1, maximum); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if err = value(d, depth+1, maximum); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	end, err := d.Token()
	if err != nil {
		return err
	}
	if delimiter == '{' && end != json.Delim('}') || delimiter == '[' && end != json.Delim(']') {
		return fmt.Errorf("unclosed JSON container")
	}
	return nil
}
