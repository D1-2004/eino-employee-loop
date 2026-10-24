# Evaluation cases and evidence

`evals` exercises the reference applications and Employee Loop framework.
Production code under `employee` must not depend on this directory.

```text
evals/
├── run.py            Build/run local cases and verify persisted evidence
├── cases/local/      Profiles, admitted input, scripted models and schedules
├── reports/          Version-specific evaluation results
├── reviews/          Earlier source reviews and non-building probe attachments
└── architecture/     Historical diagrams tied to earlier versions
```

Run the complete local acceptance from the repository root (Python 3.9+ and Go):

```sh
python3 evals/run.py
python3 evals/run.py --output /absolute/empty-evidence-directory
```

The runner builds `app/local`, checks six Owner documents and verified file
receipts, reopens state across processes, resumes native questions, rejects wrong
targets and confirms replay deduplication. It writes a summary, command output and
state evidence into a fresh temporary directory by default. `--output` must name
an empty directory. `--binary /absolute/path` tests an existing binary and records
its hash without claiming a source revision for it.

Run one local case directly:

```sh
go run ./app/local -profile evals/cases/local/allloops-profile.json -dir /tmp/el-eval -script evals/cases/local/allloops-script.json replay -in evals/cases/local/allloops-input.ndjson
```

Use a fresh directory for an independent case. Scripted models validate mechanism
and recovery behavior; real-model quality and online transport need separate
acceptance against the tested version/environment. See the [local application
instructions](../app/local/README.md) and [CLI guide](../employee/docs/local-entry.md).

Existing reports, reviews and historical diagrams preserve their original SHA,
paths, commands and environment. Moving a report here does not validate a new
revision or imply that its old paths are current usage instructions. Original
locations included `docs/evals`, `docs/reviews` and `docs/architecture`, then
`docs/archive`; historical framework plans remain under `employee/docs/archive/plans`.
Probe source attachments use `.go.txt` and do not enter current builds.
