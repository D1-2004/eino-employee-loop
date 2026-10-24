#!/usr/bin/env python3
"""Exercise the local CLI through existing scripted fixtures and inspect real files.

This verifies mechanisms with SQLite, Eino checkpoints and local-file delivery.
It does not evaluate real model quality or send DingTalk messages.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "evals" / "cases" / "local"


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


class Acceptance:
    def __init__(self, output, timeout):
        self.output, self.timeout = output, timeout
        self.summary = {
            "schema": "local-acceptance/1", "passed": False,
            "scope": {"model": "scripted", "transport": "local-file", "real_model": False,
                      "real_im": False, "online_parity": "not established"},
            "commands": [], "checks": [], "cases": {},
            "fixtures": {p.name: sha256(p) for p in sorted(FIXTURES.iterdir()) if p.is_file()},
        }
        self.env = dict(os.environ)
        for name in ("LLM_BASE_URL", "LLM_API_KEY", "OPENAI_API_KEY"):
            self.env.pop(name, None)

    def check(self, name, condition, detail=None):
        item = {"name": name, "passed": bool(condition)}
        if detail is not None:
            item["detail"] = detail
        self.summary["checks"].append(item)
        if not condition:
            raise AssertionError(name)

    def command(self, name, args, success=True):
        stdout = self.output / (name + ".stdout.ndjson")
        stderr = self.output / (name + ".stderr.txt")
        record = {"name": name, "argv": [str(a) for a in args],
                  "stdout": stdout.name, "stderr": stderr.name,
                  "expected_success": success}
        self.summary["commands"].append(record)
        started = time.monotonic()
        # Redirect directly to files so partial output survives timeouts or failure.
        with stdout.open("wb") as out, stderr.open("wb") as err:
            try:
                result = subprocess.run(record["argv"], cwd=ROOT, env=self.env,
                                        stdout=out, stderr=err, timeout=self.timeout)
                record["returncode"] = result.returncode
            except subprocess.TimeoutExpired:
                record["timed_out"] = True
                raise RuntimeError(f"{name} exceeded {self.timeout}s") from None
            finally:
                record["duration_seconds"] = round(time.monotonic() - started, 3)
        if (result.returncode == 0) != success:
            raise RuntimeError(f"{name}: unexpected exit {result.returncode}; see {stderr}")
        if not success:
            return []
        return [json.loads(line) for line in stdout.read_text(encoding="utf-8").splitlines()
                if line.strip()] if name != "build" else []

    def cli(self, name, case, profile, script, *args, success=True):
        return self.command(name, [self.binary, "-profile", FIXTURES / profile,
                            "-dir", self.output / case, "-script", FIXTURES / script,
                            *args], success=success)

    def state(self, rows):
        states = [row for row in rows if row.get("type") == "local_state"]
        self.check("exactly one scene snapshot", len(states) == 1)
        state = states[0]
        self.check("no recorded worker failures", not state.get("failures"))
        return state

    def file(self, case, relative):
        base = (self.output / case / "workspace").resolve()
        path = (base / relative).resolve()
        self.check("artifact stays inside local workspace", path.is_relative_to(base))
        self.check("artifact exists", path.is_file(), relative)
        return path

    def deliveries(self, case, state, count):
        records = state.get("deliveries") or []
        self.check(case + ": delivery count", len(records) == count, len(records))
        self.check(case + ": unique intents", len({r["Intent"]["ID"] for r in records}) == count)
        for record in records:
            receipt = record.get("Receipt") or {}
            self.check(case + ": verified local-file receipt",
                       record["State"] == "applied" and receipt.get("Stage") == "local_applied"
                       and receipt.get("Transport") == "local-file")
            evidence = receipt.get("Evidence") or {}
            path = self.file(case, evidence["path"])
            self.check(case + ": delivery actual body and SHA256",
                       path.read_text(encoding="utf-8") == record["Intent"]["Body"]
                       and sha256(path) == receipt["BodySHA256"] == evidence["sha256"])
        return records

    def workspace(self, case):
        base = self.output / case / "workspace"
        return {str(p.relative_to(base)): sha256(p) for p in sorted(base.rglob("*")) if p.is_file()}

    def database(self, case, name):
        path = self.output / case / "state" / "state.sqlite"
        # Every CLI process has exited and closed its provider. Refuse an active
        # WAL rather than silently inspecting a stale main database. Immutable
        # read-only mode also avoids creating WAL/SHM files during inspection.
        wal = path.with_name(path.name + "-wal")
        self.check(case + ": closed SQLite checkpoint", not wal.exists() or wal.stat().st_size == 0)
        with sqlite3.connect(path.resolve().as_uri() + "?mode=ro&immutable=1", uri=True) as db:
            events = [{"namespace": row[0], "id": row[1], "kind": row[2],
                       "body": json.loads(row[3])}
                      for row in db.execute("SELECT namespace,id,kind,body FROM events ORDER BY sequence")]
            documents = [{"namespace": row[0], "scope": row[1], "id": row[2],
                          "revision": row[3], "sha256": hashlib.sha256(bytes(row[4])).hexdigest()}
                         for row in db.execute("SELECT namespace,scope,id,revision,body FROM documents ORDER BY namespace,scope,id")]
            checkpoints = db.execute("SELECT count(*) FROM eino_checkpoints").fetchone()[0]
        result = {"events": events, "documents": documents, "checkpoint_count": checkpoints}
        write_json(self.output / (name + ".sqlite-inspection.json"), result)
        return result

    def run_allloops(self):
        case, profile, script = "allloops", "allloops-profile.json", "allloops-script.json"
        rows = self.cli("allloops-first", case, profile, script, "replay", "-in", FIXTURES / "allloops-input.ndjson")
        state = self.state(rows)
        tasks = state.get("tasks") or []
        self.check("allloops: single completed Task", len(tasks) == 1 and tasks[0]["State"] == "completed")
        task = tasks[0]
        runs = task.get("Runs") or []
        self.check("allloops: proven execution exit", len(runs) == 1 and runs[0]["State"] == "succeeded" and runs[0]["ExitProven"])
        self.check("allloops: Review completed Goal", task["Goal"]["State"] == "completed" and not task["NeedsReview"]
                   and "SHA256" in task["Goal"]["Summary"])
        rules = json.loads((FIXTURES / script).read_text(encoding="utf-8"))["rules"]
        expected = next(call["arguments"]["content"] for rule in rules for call in rule.get("tool_calls", [])
                        if call["name"] == "write_file")
        artifacts = runs[0].get("Artifacts") or []
        self.check("allloops: execution reported real artifact", len(artifacts) == 1)
        artifact = artifacts[0]
        path = self.file(case, artifact["Path"])
        self.check("allloops: exact file content, size and SHA256", path.read_text(encoding="utf-8") == expected
                   and sha256(path) == artifact["SHA256"] and path.stat().st_size == artifact["Bytes"])
        delivered = self.deliveries(case, state, 1)
        self.check("allloops: expression and package present", "report.md 已创建并核验。" in delivered[0]["Intent"]["Body"]
                   and "packages/" in delivered[0]["Intent"]["Body"])
        packages = list((self.output / case / "workspace" / "packages").rglob("*.md"))
        self.check("allloops: real package readback", len(packages) == 1 and
                   "report.md 的实际文件、SHA256 和原始任务信息" in packages[0].read_text(encoding="utf-8"))
        owners = self.cli("allloops-owners-first", case, profile, script, "owner-status")[0]["documents"]
        self.check("allloops: all six Owner roles", {d["Role"] for d in owners} ==
                   {"attention", "memory", "dreaming", "tone", "reflection", "summary"} and len(owners) == 6)
        for doc in owners:
            evidence = doc.get("Evidence") or []
            self.check("allloops: original Owner citation", not doc["Invalidated"] and len(evidence) == 1
                       and evidence[0]["Ref"]["ID"] == "allloops-message-1" and "我偏好简洁中文" in evidence[0]["Quote"])
            self.check("allloops: Owner actual file SHA256", sha256(self.file(case, doc["Path"])) == doc["SHA256"])
        database = self.database(case, "allloops-first")
        self.check("allloops: durable Review completion event", any(e["namespace"] == "work-events" and
                   e["kind"] == "goal.completed" and e["id"].startswith("review-") for e in database["events"]))
        files = self.workspace(case)
        replay = self.cli("allloops-reopened-replay", case, profile, script, "replay", "-in", FIXTURES / "allloops-input.ndjson")
        after = self.state(replay)
        self.check("allloops: replay admission deduplicated", any(r.get("type") == "admission" and r["admission"]["Replay"] for r in replay))
        self.check("allloops: reopened Task, Run and delivery unchanged", after["tasks"] == tasks and after["deliveries"] == delivered)
        self.check("allloops: no repeated file writes", self.workspace(case) == files)
        owners_after = self.cli("allloops-owners-reopened", case, profile, script, "owner-status")[0]["documents"]
        self.check("allloops: reopened Owner documents unchanged", owners_after == owners)
        self.check("allloops: persisted events and model state unchanged", self.database(case, "allloops-reopened") == database)
        self.summary["cases"][case] = {"passed": True, "task_id": task["ID"], "run_id": runs[0]["ID"],
                                       "owner_roles": sorted(d["Role"] for d in owners), "delivery_count": 1}

    def run_interruption(self):
        case, profile, script = "ask", "profile.json", "ask-script.json"
        state = self.state(self.cli("ask-first", case, profile, script, "submit", "-id", "question-1", "-text", "请创建报告"))
        pending = state.get("pending") or {}
        self.check("ask: one native interrupt target", len(pending) == 1 and len(next(iter(pending.values()))) == 1)
        job, targets = next(iter(pending.items()))
        target = targets[0]
        interrupted = self.database(case, "ask-interrupted")
        self.check("ask: native interruption checkpoint persisted", interrupted["checkpoint_count"] > 0)
        first = self.deliveries(case, state, 1)
        self.check("ask: question delivered before answer", first[0]["Intent"]["Kind"] == "question")
        self.cli("ask-wrong-target-rejected", case, profile, script, "answer", "-job", job, "-target", "wrong-target",
                 "-id", "bad-answer", "-text", "标题", success=False)
        state = self.state(self.cli("ask-exact-resume", case, profile, script, "answer", "-job", job, "-target", target,
                                    "-id", "answer-1", "-text", "本地验证报告"))
        self.check("ask: exact persisted target resumed", not state.get("pending") and not state.get("tasks"))
        delivered = self.deliveries(case, state, 2)
        self.check("ask: question and final reply", {d["Intent"]["Kind"] for d in delivered} == {"question", "reply"})
        files, database = self.workspace(case), self.database(case, "ask-resumed")
        replay = self.cli("ask-reopened-replay", case, profile, script, "submit", "-id", "question-1", "-text", "请创建报告")
        after = self.state(replay)
        self.check("ask: original admission replay deduplicated", any(r.get("type") == "admission" and r["admission"]["Replay"] for r in replay))
        self.check("ask: replay has no new delivery or question", after["deliveries"] == delivered and not after.get("pending") and not after.get("tasks"))
        self.check("ask: replay unchanged files and native state", self.workspace(case) == files and self.database(case, "ask-reopened") == database)
        self.summary["cases"][case] = {"passed": True, "job_id": job, "native_target": target, "delivery_count": 2}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="empty evidence directory (default: a retained temporary directory)")
    parser.add_argument("--binary", type=Path, help="test an existing CLI; its source provenance remains unknown")
    parser.add_argument("--command-timeout", type=int, default=180, help="per-process timeout in seconds")
    args = parser.parse_args()
    if args.command_timeout <= 0:
        parser.error("--command-timeout must be positive")
    output = args.output.expanduser().resolve() if args.output else Path(tempfile.mkdtemp(prefix="eel-local-acceptance-"))
    if output.exists() and any(output.iterdir()):
        parser.error("--output must be empty; prior evidence will not be overwritten")
    output.mkdir(parents=True, exist_ok=True)
    runner = Acceptance(output, args.command_timeout)
    code = 1
    try:
        if args.binary:
            runner.binary = args.binary.expanduser().resolve()
            runner.summary["binary"] = {"path": str(runner.binary), "origin": "provided", "source_provenance": "unknown"}
        else:
            runner.binary = output / "local-employee"
            runner.summary["binary"] = {"path": str(runner.binary), "origin": "built-by-runner",
                                        "build_command": ["go", "build", "-o", str(runner.binary), "./app/local"]}
            runner.command("build", runner.summary["binary"]["build_command"])
        runner.summary["binary"]["sha256"] = sha256(runner.binary)
        runner.run_allloops()
        runner.run_interruption()
        runner.summary["passed"] = True
        code = 0
    except Exception as exc:
        runner.summary["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        write_json(output / "summary.json", runner.summary)
        print(json.dumps({"passed": runner.summary["passed"], "summary": str(output / "summary.json"),
                          "error": runner.summary.get("error")}, ensure_ascii=False))
    return code


if __name__ == "__main__":
    sys.exit(main())
