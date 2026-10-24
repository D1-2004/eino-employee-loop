# Local replay evaluation application

`app/eval` runs the same `app/local` reference application through recorded input
and scripted model responses. It orchestrates processes and inspects actual
SQLite/Markdown evidence; it does not implement an alternative Employee loop.
No model credentials or DingTalk connection are required. `go run` needs Go;
the compiled evaluator with `--binary` does not need a Go compiler or source checkout.

```sh
go run ./app/eval
go run ./app/eval --output /absolute/empty-evidence-directory
```

The default builds `./app/local` once, then checks all six Owner roles, Task/Run/
Review completion, actual artifact hashes and local delivery receipts. It reopens
the database in another process, tests native interruption and exact-target
resume, rejects the wrong target, and verifies that replay creates no duplicate
state, files or deliveries. A suite failure exits nonzero and retains command
output, persisted state and `summary.json`. Existing output is never overwritten. A retained exclusive `.eval-owner` claim
ensures concurrent evaluators cannot mix evidence in the same output directory.

Model and execution are separate capabilities: this suite selects the scripted
model through each case's `-script` fixture, and selects the local native atomic
executor through the reference application's assembly. SQLite state, Markdown
artifacts and local-file delivery remain the actual reference Providers. The
summary records each capability independently. Supporting another executor or
model requires a corresponding reference assembly and evidence adapter; a label
alone does not change the tested Provider.

For another development machine, server or container, copy these three things:
`employee-eval`, `local-employee` and the `evals/cases/local` directory as `fixtures`.
Then run from that bundle:

```sh
./employee-eval --binary ./local-employee --fixtures ./fixtures
```

Build with `go build -o employee-eval ./app/eval` and
`go build -o local-employee ./app/local` for the target platform. This mode needs
no repository metadata, `go.mod`, Git, Python or Go compiler at runtime.
`--root` only changes the child-process working directory and default fixture
location (default: discover the checkout from the current working directory,
otherwise use the current directory). `--fixtures` supplies a copied bundle. `--binary` skips the build and records the binary hash, with source
provenance explicitly unknown. Its runtime contract must match `app/local`.
There is no online service endpoint or remote business write mode.

`python3 evals/run.py` remains a compatibility wrapper for this Go application.
`--command-timeout` controls the timeout of each child process in seconds.
Each run records fixture SHA256 values, binary SHA256, child command arguments,
exit status and check results in the `local-acceptance/1` summary.

To exercise failure reporting on macOS/Linux, use an incompatible executable:
`./employee-eval --binary /usr/bin/false --fixtures ./fixtures --output ./failed-evidence`.
The suite exits 1, retains the failed command and writes `passed: false`. Running
again with the same output directory exits 2 and preserves the original evidence.
For a replay-format failure, copy the fixture bundle and replace
`allloops-input.ndjson` with malformed JSON; run with `--fixtures` pointing to the
copy and a fresh output directory. No live service is contacted in either case.
