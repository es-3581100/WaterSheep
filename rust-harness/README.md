# WaterSheep Rust Harness

A Rust-owned contract and transport layer for local WaterSheep decisions.

The harness deliberately **does not replace WaterSheep inference**. WaterSheep stays the model/runtime and keeps serving the Jev-compatible `POST /v1/systemone` endpoint. Rust owns the typed boundary around that endpoint: validation, native transport, deterministic hashes, read-only decision receipts, TypeScript bindings, and a transport-neutral WebAssembly projection.

```text
WaterSheep (Python / local model)
        | POST /v1/systemone
        v
Rust harness
  |- typed request/response contracts
  |- validation
  |- deterministic request/response SHA-256
  |- evidence-only receipt
  |- native HTTP client + CLI
  |- TypeScript declarations (ts-rs)
  `- WASM receipt projector
        | JSON contract
        v
Go-App / browser adapter
        |
        v
MM-manager / App-Dir Matrix observatory
```

The output contract is `watersheep.harness.receipt/v1`. Its `authority` is always `evidence-only`: a model decision is evidence for a UI/runtime to inspect, never permission to mutate project state.

## Native use

Start WaterSheep:

```bash
watersheep --model samratduttaofficial/WaterSheep --serve
```

Then call it through the harness:

```bash
cargo run --manifest-path rust-harness/Cargo.toml -- \
  ask --request rust-harness/examples/request.json
```

`--request -` reads JSON from stdin. `WATERSHEEP_BASE_URL` overrides the default `http://127.0.0.1:8766`.

To keep the raw model response as well as the receipt:

```bash
cargo run --manifest-path rust-harness/Cargo.toml -- \
  ask --request request.json \
  --response-out response.json \
  --out receipt.json
```

To project an already captured request/response pair with no network activity:

```bash
cargo run --manifest-path rust-harness/Cargo.toml -- \
  project --request request.json --response response.json --out receipt.json
```

## WebAssembly

The WASM build intentionally contains **no HTTP client**. The browser/Go-App layer owns transport and passes request/response JSON into Rust for validation and receipt projection.

```bash
rustup target add wasm32-unknown-unknown
cargo install wasm-bindgen-cli
cargo build \
  --manifest-path rust-harness/Cargo.toml \
  --release \
  --target wasm32-unknown-unknown \
  --no-default-features \
  --features wasm

wasm-bindgen \
  --target web \
  --out-dir rust-harness/pkg \
  target/wasm32-unknown-unknown/release/watersheep_harness.wasm
```

Exports:

- `validate_request_json(request_json)`
- `project_receipt(request_json, response_json, endpoint)`

A browser bridge can expose the Rust projector to Go-App:

```js
import init, { project_receipt } from "./watersheep_harness.js";
await init();
globalThis.waterSheepProjectReceipt = project_receipt;
```

Go-App can invoke that function through `syscall/js` and feed the resulting receipt JSON into the Matrix ingest/runtime layer. The Go and Rust WASM modules communicate through a browser JSON seam instead of trying to link one WASM binary into the other.

## TypeScript bindings

The contract structs derive `ts-rs` bindings under the default `typescript` feature:

```bash
cargo test --manifest-path rust-harness/Cargo.toml
```

That produces TypeScript declarations for the request, response, receipt, and decision contracts.

## Matrix contract

The JSON Schema is at:

```text
rust-harness/schema/watersheep-harness-receipt-v1.schema.json
```

A receipt carries:

- canonical request and response SHA-256 hashes;
- the WaterSheep model name and token usage;
- normalized decisions, probabilities, confidence, selected labels, and in-band errors;
- `authority: evidence-only`.

A response is rejected if its answer names do not exactly match the request question names. This catches stale or crossed request/response pairs before they reach the matrix.

## Design rules

- WaterSheep remains the inference authority for model outputs.
- Predictions never become execution authority merely by entering the harness.
- Request/response hashes are canonicalized so JSON object-key order does not change receipt identity.
- Native HTTP and WASM projection share the same contracts and receipt builder.
- The boundary stays domain-neutral and contains no KoL-specific data or taxonomy.
