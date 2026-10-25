# WaterSheep Go supervisor

`go-bridge` owns the local WaterSheep child process and its observable launch status. The module does **not** grant model output execution authority.

Normal flow:

```text
Rust harness -> Go supervisor /v1/systemone -> WaterSheep
                 |
                 `-> GET /v1/status (observation-only)
```

`GET /v1/status` returns `watersheep.supervisor.status/v1` with `owner: go-bridge`, the Go module path, child PID, readiness, and upstream endpoint.

Dev-only direct output is intentionally separate from normal inference transport:

```bash
go run ./cmd/watersheep-supervisor serve --dev

go run ./cmd/watersheep-supervisor debug-ask \
  --request ../rust-harness/examples/request.json \
  --approve-readonly-direct-output
```

The debug call is refused unless both conditions are true:

1. the supervisor was launched with `--dev`; and
2. the caller supplies `--approve-readonly-direct-output` for that call.

Direct debug output and captured child logs are `observation-only`. They bypass the Rust receipt only for inspection and must not be consumed as authority.

For the bundle smoke without loading the real model:

```bash
python3 tools/pybundle/watersheep_bundle.py --smoke
python3 tools/pybundle/watersheep_bundle.py --smoke --dev
```

The first run proves the debug surface stays locked. The second additionally proves an explicitly approved direct-output call succeeds. Both route the normal request through the Go supervisor and, when Cargo is available, through the Rust receipt builder.
