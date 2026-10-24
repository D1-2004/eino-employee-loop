package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func (a *acceptance) allLoops() {
	const c = "allloops"
	const p = "allloops-profile.json"
	const s = "allloops-script.json"
	state := a.state(a.cli("allloops-first", c, p, s, true, "replay", "-in", filepath.Join(a.fixtures, "allloops-input.ndjson")))
	tasks := list(state["tasks"])
	a.check("allloops: single completed Task", len(tasks) == 1 && obj(tasks[0])["State"] == "completed")
	task := obj(tasks[0])
	runs := list(task["Runs"])
	a.check("allloops: proven execution exit", len(runs) == 1 && obj(runs[0])["State"] == "succeeded" && obj(runs[0])["ExitProven"] == true)
	run := obj(runs[0])
	goal := obj(task["Goal"])
	a.check("allloops: Review completed Goal", goal["State"] == "completed" && task["NeedsReview"] != true && strings.Contains(str(goal["Summary"]), "SHA256"))
	expected := ""
	for _, r := range list(readJSON(filepath.Join(a.fixtures, s))["rules"]) {
		for _, t := range list(obj(r)["tool_calls"]) {
			call := obj(t)
			if call["name"] == "write_file" {
				expected = str(obj(call["arguments"])["content"])
			}
		}
	}
	a.check("allloops: fixture contains expected file write", expected != "")
	artifacts := list(run["Artifacts"])
	a.check("allloops: execution reported real artifact", len(artifacts) == 1)
	artifact := obj(artifacts[0])
	path := a.file(c, str(artifact["Path"]))
	info, err := os.Stat(path)
	must(err)
	a.check("allloops: exact file content, size and SHA256", readText(path) == expected && digest(path) == artifact["SHA256"] && float64(info.Size()) == artifact["Bytes"])
	delivered := a.deliveries(c, state, 1)
	body := str(obj(obj(delivered[0])["Intent"])["Body"])
	a.check("allloops: expression and package present", strings.Contains(body, "report.md 已创建并核验。") && strings.Contains(body, "packages/"))
	var packages []string
	must(filepath.WalkDir(filepath.Join(a.output, c, "workspace", "packages"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			packages = append(packages, path)
		}
		return nil
	}))
	a.check("allloops: real package readback", len(packages) == 1 && strings.Contains(readText(packages[0]), "report.md 的实际文件、SHA256 和原始任务信息"))
	owners := list(obj(a.cli("allloops-owners-first", c, p, s, true, "owner-status")[0])["documents"])
	roles := []string{}
	for _, d := range owners {
		doc := obj(d)
		roles = append(roles, str(doc["Role"]))
		evidence := list(doc["Evidence"])
		a.check("allloops: original Owner citation", doc["Invalidated"] != true && len(evidence) == 1 && obj(obj(evidence[0])["Ref"])["ID"] == "allloops-message-1" && strings.Contains(str(obj(evidence[0])["Quote"]), "我偏好简洁中文"))
		a.check("allloops: Owner actual file SHA256", digest(a.file(c, str(doc["Path"]))) == doc["SHA256"])
	}
	sort.Strings(roles)
	a.check("allloops: all six Owner roles", reflect.DeepEqual(roles, []string{"attention", "dreaming", "memory", "reflection", "summary", "tone"}))
	database := a.database(c, "allloops-first")
	completed := false
	for _, e := range list(database["events"]) {
		event := obj(e)
		if event["namespace"] == "work-events" && event["kind"] == "goal.completed" && strings.HasPrefix(str(event["id"]), "review-") {
			completed = true
		}
	}
	a.check("allloops: durable Review completion event", completed)
	files := a.workspace(c)
	replay := a.cli("allloops-reopened-replay", c, p, s, true, "replay", "-in", filepath.Join(a.fixtures, "allloops-input.ndjson"))
	after := a.state(replay)
	a.check("allloops: replay admission deduplicated", replayed(replay))
	a.check("allloops: reopened Task, Run and delivery unchanged", reflect.DeepEqual(after["tasks"], tasks) && reflect.DeepEqual(after["deliveries"], delivered))
	a.check("allloops: no repeated file writes", reflect.DeepEqual(a.workspace(c), files))
	ownersAfter := list(obj(a.cli("allloops-owners-reopened", c, p, s, true, "owner-status")[0])["documents"])
	a.check("allloops: reopened Owner documents unchanged", reflect.DeepEqual(ownersAfter, owners))
	a.check("allloops: persisted events and model state unchanged", reflect.DeepEqual(a.database(c, "allloops-reopened"), database))
	obj(a.summary["cases"])[c] = object{"passed": true, "task_id": task["ID"], "run_id": run["ID"], "owner_roles": roles, "delivery_count": 1}
}
func (a *acceptance) interruption() {
	const c = "ask"
	const p = "profile.json"
	const s = "ask-script.json"
	state := a.state(a.cli("ask-first", c, p, s, true, "submit", "-id", "question-1", "-text", "请创建报告"))
	pending := obj(state["pending"])
	a.check("ask: one native interrupt job", len(pending) == 1)
	var job, target string
	for k, v := range pending {
		job = k
		targets := list(v)
		a.check("ask: one native interrupt target", len(targets) == 1)
		target = str(targets[0])
	}
	interrupted := a.database(c, "ask-interrupted")
	a.check("ask: native interruption checkpoint persisted", interrupted["checkpoint_count"].(int64) > 0)
	first := a.deliveries(c, state, 1)
	a.check("ask: question delivered before answer", obj(obj(first[0])["Intent"])["Kind"] == "question")
	beforeRejected := a.workspace(c)
	a.cli("ask-wrong-target-rejected", c, p, s, false, "answer", "-job", job, "-target", "wrong-target", "-id", "bad-answer", "-text", "标题")
	a.check("ask: rejected target preserves checkpoint and files", reflect.DeepEqual(a.database(c, "ask-rejected"), interrupted) && reflect.DeepEqual(a.workspace(c), beforeRejected))
	state = a.state(a.cli("ask-exact-resume", c, p, s, true, "answer", "-job", job, "-target", target, "-id", "answer-1", "-text", "本地验证报告"))
	a.check("ask: exact persisted target resumed", len(obj(state["pending"])) == 0 && len(list(state["tasks"])) == 0)
	delivered := a.deliveries(c, state, 2)
	kinds := map[string]bool{}
	for _, d := range delivered {
		kinds[str(obj(obj(d)["Intent"])["Kind"])] = true
	}
	a.check("ask: question and final reply", len(kinds) == 2 && kinds["question"] && kinds["reply"])
	files, database := a.workspace(c), a.database(c, "ask-resumed")
	replay := a.cli("ask-reopened-replay", c, p, s, true, "submit", "-id", "question-1", "-text", "请创建报告")
	after := a.state(replay)
	a.check("ask: original admission replay deduplicated", replayed(replay))
	a.check("ask: replay has no new delivery or question", reflect.DeepEqual(after["deliveries"], delivered) && len(obj(after["pending"])) == 0 && len(list(after["tasks"])) == 0)
	a.check("ask: replay unchanged files and native state", reflect.DeepEqual(a.workspace(c), files) && reflect.DeepEqual(a.database(c, "ask-reopened"), database))
	obj(a.summary["cases"])[c] = object{"passed": true, "job_id": job, "native_target": target, "delivery_count": 2}
}
