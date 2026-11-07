# `kol-watersheep` — installable KoLmafia/Xpra observation-only launcher

This is an **additive, standalone Go module** that packages the previously working
KoLmafia/Xpra + Don Combo + WaterSheep startup kit as one CLI. It does **not**
change `go-bridge/go.mod`, Rust crate sources, Don Combo, or KoLmafia runtime
preferences. The Python launcher and dual GET/POST adapter are embedded in the
Go binary and extracted into a versioned directory in the user's private cache.

The existing generic Go supervisor (`go-bridge/`) still launches the Python
WaterSheep model and exposes `127.0.0.1:8777`. The adapter on port `8768`
bridges Don Combo to the supervisor. The Rust harness remains an **optional,
independent evidence-only tool**; it is not silently inserted into the request
path. **No KoL game actions are run by this package.**

## Requirements

- **Linux**, Go 1.22+, Git, Docker with the Compose plugin, Python 3 with `venv`
  (or an existing Conda installation).
- An existing `kolmafia-xpra` checkout containing `compose.yaml` and a working
  Docker configuration. First-time KoLmafia login is done manually by the user.
- **Don Combo installed inside KoLmafia**. The startup adapter is not itself the
  Don Combo plugin. The package verifies that a published Don Combo adapter
  file exists in the Xpra data tree; it will not install KoLmafia scripts or
  change the active KoLmafia character's preferences without consent.
- Enough disk space/network capacity for CPU PyTorch and the model download.

On first launch, if `watersheep/kol-watersheep` does not exist, the launcher
clones `https://github.com/donCannoli-burns/KOL-WaterSheep.git` into that
subdirectory. The original `kolstack setup` also clones the generic
`es-3581100/WaterSheep` branch `feature/rust-wasm-harness` into the separate
`watersheep/watersheep-go-rust-core` directory if missing. Both clones are
**non-overwriting**; existing directories are not pulled/updated.

**Provenance caveat:** those first-use Git clones are branch-head based, not
locked to a commit. For a reproducible release, pin and record both source
commits (and model revision) before publishing a stable tag. A public Go
installation installs this *launcher* at a selected commit/tag; it does not
pin every runtime dependency automatically.

## Try locally (before publishing)

Copy this `kol-watersheep` folder under the **`feature/rust-wasm-harness`**
branch checkout of `es-3581100/WaterSheep` (alongside `go-bridge/` and
`rust-harness/`). Its `go.mod` is deliberately nested:

```sh
cd ~/repos/es-3581100/kolmafia-xpra/watersheep/watersheep-go-rust-core/kol-watersheep
go test ./...
go install .
```

Then, from your Xpra checkout, use:

```sh
cd ~/repos/es-3581100/kolmafia-xpra
$(go env GOPATH)/bin/kol-watersheep start
```

Or from anywhere:

```sh
$(go env GOPATH)/bin/kol-watersheep start \
  --xpra-root "$HOME/repos/es-3581100/kolmafia-xpra"
```

If the Go executable directory is on your `PATH`, simply use `kol-watersheep`.

**Already running on the old launcher?** The new CLI automatically discovers
and reads the old `kolstack-watersheep-go-rust-kit/config.env` under your Xpra
checkout if you have not created a newer configuration. It reuses the same
`WS_STATE`, PID/start-tick ownership record, Conda environment, Go binary,
model, and Docker bridge. `start` will run the existing-process health checks
and **not** start duplicates. The user's existing config is never overwritten.

## One command after publishing

After this nested module has been **committed and published** in the upstream
GitHub repository and tagged, users can run:

```sh
go run github.com/es-3581100/WaterSheep/kol-watersheep@v0.1.0 start
```

Or install the binary once:

```sh
go install github.com/es-3581100/WaterSheep/kol-watersheep@v0.1.0
kol-watersheep start
```

> These remote commands are *not yet live*. The tag must refer to a commit
> containing this nested module; Go releases for subdirectory modules use a
> Git tag such as `kol-watersheep/v0.1.0`. Until it is published, use the local
> `go install .` route shown above. A `go.mod` on its own does not install
> Python, Torch, Docker, or KoLmafia.

For someone who already has Xpra + Don Combo configured, the **single command
that launches and verifies inference** is `kol-watersheep start`. From a fresh
installation, it will provision the missing local model environment and compile
the supervisor on first run; later starts simply verify/reuse it.

## Commands

```sh
kol-watersheep start               # first-time setup if needed, then start/verify
kol-watersheep status              # read-only service status
kol-watersheep up                  # start only (requires setup)
kol-watersheep setup --skip-rust   # explicit install/build
kol-watersheep rust-check          # optional evidence-only Rust receipt
kol-watersheep down                # stop only launcher-owned processes
kol-watersheep down --with-xpra    # also stop Docker/Xpra (no volume deletion)
kol-watersheep start --dry-run     # show plan, no writes or processes
kol-watersheep start --xpra-root /path/to/kolmafia-xpra
```

The launcher's health checks are a **synthetic shadow-only six-class (A–F)
request**, container-to-private-adapter connectivity, and the actual published
Xpra HTTP port. They do not send beach comb actions, in-game commands, Kmail,
or change game preferences.

## Configuration and data locations

1. If `--config PATH` is provided, use that existing config file.
2. Otherwise, use `$XDG_CONFIG_HOME/kol-watersheep/config.env`, if present.
3. Otherwise, reuse `<xpra-root>/kolstack-watersheep-go-rust-kit/config.env`,
   if present (the working legacy installer).
4. Otherwise, create a per-user config with `0600` permissions under
   `$XDG_CONFIG_HOME/kol-watersheep/config.env` (default `~/.config/…`).

For new users, the default config chooses a **dedicated Python virtualenv** in
`~/.local/state/kolstack-watersheep/venv` instead of requiring Conda. It
installs official **CPU-only PyTorch** first to avoid large CUDA/Triton wheels,
then installs the local KoL-WaterSheep fork. To reuse Conda, set
`WS_USE_CONDA=1` and `WS_CONDA_ENV=<name>` or reuse your existing legacy config.
To use a pre-provisioned GPU Torch, configure `WS_TORCH_BACKEND=existing`
explicitly; this does **not** install drivers.

The bundle is extracted into `~/.cache/kol-watersheep/launcher/<asset-hash>/`
(or `$XDG_CACHE_HOME/…`) and the launcher-owned logs/process record remain
under `~/.local/state/kolstack-watersheep/` by default. Configuration files
are never committed to source control. Do not share credential files.

**Don Combo preference:** the CLI prints the discovered Docker-gateway URL,
for example `http://172.29.0.1:8768/v1/kolmafia`. It checks whether the
on-disk preference matches, but never writes it or runs gCLI automatically.
If needed, set the endpoint manually once in KoLmafia's CLI, then use read-only
`doncombo info` and `don_combo status` checks.

## Validation

From this module folder:

```sh
go test ./...
go vet ./...
python3 -m unittest discover -s tests -v
kol-watersheep start --dry-run --xpra-root /path/to/kolmafia-xpra
```

These tests run without KoL gameplay. The HTTP compatibility test uses local
fake HTTP servers, not a real model; a live first run still requires actual
Docker, model installation, and an available network.

## Future release hardening

For upstream review, additionally validate clean install in a fresh Linux
profile, first-use interrupted pip recovery, CLI upgrade with running
processes, package source revision pinning, non-8080 host port, active Don
Combo preference identity, and safe server shutdown. The embedded Python
launcher owns PID checks and an `flock` runtime lock; this CLI does not yet
provide a systemd watchdog, GPU auto-detection, or an automated KoLmafia plugin
installation mechanism.
