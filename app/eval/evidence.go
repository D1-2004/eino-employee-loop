package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type object = map[string]any

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func obj(v any) object {
	if v == nil {
		return object{}
	}
	return v.(map[string]any)
}
func list(v any) []any {
	if v == nil {
		return nil
	}
	return v.([]any)
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return v.(string)
}
func jsonFile(path string, value any) {
	b, err := json.MarshalIndent(value, "", "  ")
	must(err)
	must(os.WriteFile(path, append(b, '\n'), 0600))
}
func readJSON(path string) object {
	b, err := os.ReadFile(path)
	must(err)
	v := object{}
	must(json.Unmarshal(b, &v))
	return v
}
func digest(path string) string {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(err)
	return hex.EncodeToString(h.Sum(nil))
}
func (a *acceptance) check(name string, passed bool, detail ...any) {
	c := object{"name": name, "passed": passed}
	if len(detail) > 0 {
		c["detail"] = detail[0]
	}
	a.summary["checks"] = append(a.summary["checks"].([]any), c)
	if !passed {
		panic(fmt.Errorf("check failed: %s", name))
	}
}
func (a *acceptance) state(rows []any) object {
	var states []object
	for _, row := range rows {
		r := obj(row)
		if r["type"] == "local_state" {
			states = append(states, r)
		}
	}
	a.check("exactly one scene snapshot", len(states) == 1)
	s := states[0]
	a.check("no recorded worker failures", len(list(s["failures"])) == 0)
	return s
}
func (a *acceptance) file(caseName, relative string) string {
	base := filepath.Join(a.output, caseName, "workspace")
	path := filepath.Join(base, relative)
	rel, err := filepath.Rel(base, path)
	a.check("artifact stays inside local workspace", err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
	// Resolve actual paths too, so evidence cannot escape through a symlink.
	actual, err := filepath.EvalSymlinks(path)
	a.check("artifact exists", err == nil, relative)
	actualBase, err := filepath.EvalSymlinks(base)
	must(err)
	rel, err = filepath.Rel(actualBase, actual)
	a.check("artifact real path stays inside workspace", err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	info, err := os.Stat(actual)
	a.check("artifact is regular file", err == nil && info.Mode().IsRegular(), relative)
	return actual
}
func readText(path string) string { b, err := os.ReadFile(path); must(err); return string(b) }
func (a *acceptance) deliveries(caseName string, state object, count int) []any {
	records := list(state["deliveries"])
	a.check(caseName+": delivery count", len(records) == count, len(records))
	ids := map[string]bool{}
	for _, r := range records {
		record := obj(r)
		intent := obj(record["Intent"])
		ids[str(intent["ID"])] = true
		receipt := obj(record["Receipt"])
		a.check(caseName+": verified local-file receipt", record["State"] == "applied" && receipt["Stage"] == "local_applied" && receipt["Transport"] == "local-file")
		evidence := obj(receipt["Evidence"])
		path := a.file(caseName, str(evidence["path"]))
		hash := digest(path)
		a.check(caseName+": delivery actual body and SHA256", readText(path) == intent["Body"] && hash == receipt["BodySHA256"] && hash == evidence["sha256"])
	}
	a.check(caseName+": unique intents", len(ids) == count)
	return records
}
func (a *acceptance) workspace(caseName string) object {
	base := filepath.Join(a.output, caseName, "workspace")
	files := object{}
	must(filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = digest(path)
		}
		return nil
	}))
	return files
}
func (a *acceptance) database(caseName, name string) object {
	path := filepath.Join(a.output, caseName, "state", "state.sqlite")
	wal, err := os.Stat(path + "-wal")
	a.check(caseName+": closed SQLite checkpoint", os.IsNotExist(err) || (err == nil && wal.Size() == 0))
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	db, err := sql.Open("sqlite", uri+"?mode=ro&immutable=1")
	must(err)
	defer db.Close()
	events := []any{}
	rows, err := db.Query("SELECT namespace,id,kind,body FROM events ORDER BY sequence")
	must(err)
	for rows.Next() {
		var namespace, id, kind string
		var body []byte
		must(rows.Scan(&namespace, &id, &kind, &body))
		var value any
		must(json.Unmarshal(body, &value))
		events = append(events, object{"namespace": namespace, "id": id, "kind": kind, "body": value})
	}
	must(rows.Err())
	must(rows.Close())
	docs := []any{}
	rows, err = db.Query("SELECT namespace,scope,id,revision,body FROM documents ORDER BY namespace,scope,id")
	must(err)
	for rows.Next() {
		var namespace, scope, id string
		var revision int64
		var body []byte
		must(rows.Scan(&namespace, &scope, &id, &revision, &body))
		hash := sha256.Sum256(body)
		docs = append(docs, object{"namespace": namespace, "scope": scope, "id": id, "revision": revision, "sha256": hex.EncodeToString(hash[:])})
	}
	must(rows.Err())
	must(rows.Close())
	var checkpoints int64
	must(db.QueryRow("SELECT count(*) FROM eino_checkpoints").Scan(&checkpoints))
	value := object{"events": events, "documents": docs, "checkpoint_count": checkpoints}
	jsonFile(filepath.Join(a.output, name+".sqlite-inspection.json"), value)
	return value
}
func replayed(rows []any) bool {
	for _, r := range rows {
		row := obj(r)
		if row["type"] == "admission" && obj(row["admission"])["Replay"] == true {
			return true
		}
	}
	return false
}
