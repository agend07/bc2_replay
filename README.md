# BC2 Local Replay & Recording

Record your own Battlefield: Bad Company 2 rounds on the client (a dinput8.dll
proxy), build replay demos from them and watch them through a local replay
server and Project Rome client.
Recordings contain game state, not video; playback uses the actual game engine.

## Watch a recorded match

1. Download the standalone Windows ZIP from **[GitHub Releases](../../releases)**
   when a preview release is available, then extract it.
2. Follow the **[first-run guide](USER_GUIDE.md)**: configure paths, click
   **Prepare**, select a Demo and click **▶**. The panel launches BC2, which
   auto-joins the local replay server; playback starts by itself. The proxy
   makes a redirect unnecessary. Use dummy credentials if asked to log in.
3. To watch your own online rounds: the proxy records them; open
   **Recordings…** and click **▶ Play** (builds the demo on demand and publishes
   it into `demos/`). See [USER_GUIDE section 7](USER_GUIDE.md#recordings-window-client-captures).

The controller provides Play/Pause/Seek, free camera, Focus and experimental
Follow. Follow can be jittery; it is not native player possession.

You need your own supported BC2 client, dedicated server files and Project Rome.
No Python is needed for the standalone application. For building from source, see
**Build sources** below. GitHub Actions builds a ZIP; version tags create a draft
prerelease for review before publication.

## Dedicated server files

A user-provided third-party download is available here:
**[Host Server BC2-Vietnam 2025 (MediaFire)](https://www.mediafire.com/file/l06p1f5i90n1bjx/Host_Server_BC2-Vietnam_2025.rar/file)**.
This is not an official project download. We have not independently verified the
archive's contents, safety or redistribution permissions. Only download files
you trust and are entitled to use. Setup checks the expected server EXE hash;
that is a compatibility check, not a security audit of the whole archive.

## Build sources

This folder contains only sources and build inputs, not generated executables.
End users should download the release ZIP from **GitHub Releases** and follow
[USER_GUIDE.md](USER_GUIDE.md). Game/server binaries and recordings are not bundled.

## Why a redirect is required

The Rome client uses a fixed login/server-list hostname, and it must reach the
local replay backend on `127.0.0.1` instead of normal online Rome. The
`dinput8.dll` proxy in [rome_proxy/](rome_proxy/README.md) does that only for
the processes the panel starts, with no `hosts` edit. The same page describes
how the panel's client launch logs in and joins the local server automatically
(`replay/web_launch.py`). After **▶** (play), the panel launches the game
by itself once the server is ready.

The panel no longer has Redirect buttons. The system-wide `hosts` redirect is a
manual fallback: `Redirect.ps1 -Mode on|off`, which needs UAC. It sends the
Rome login/server-list hostname to the local backend system-wide; turn it off
before returning to online Rome. Use dummy credentials only, and restart BC2
after changing modes. See
[the user guide](USER_GUIDE.md#why-is-the-redirect-needed).

## Source layout

- `preview.py`, `launcher_panel.py`, `preview_config.py`: command-line entry
  (`preview.py`, also the EXE entry), the controller's Setup section and local
  backend lifecycle, and configuration.
- `server_build.py`: verified server patches; creates no recorder-only or test variants.
- `replay/`: Timeline controls, validated client access, camera hooks and UI.
- `backend/`: local backend and sanitized protocol fixtures.
- `backend_lifecycle.py`: conservative cleanup of orphan backends from the same installation.
- `instance_defaults.py`: nonpersonal gameplay defaults for the local server instance.
- `release_pack.py`: assembles and checks the release ZIP from locally built parts.
- `rome_proxy/`: required `dinput8.dll` proxy, installed by **Prepare**: recorder,
  in-game chat commands, crash minidumps, host redirect (no `hosts` entry); see its README.
- `builder_go/`: the Go recording builder (`builder.exe`, standard library, offline build).
- `datapack/`: the builder's data pack (templates, baselines, gate whitelist).
- `docs/`: tester checklist.

Only Timeline playback is supported. The trace executable is an intermediate
server build, not a second controller mode. The diagnostic `python -m replay.…`
helpers (`clean_screen`, `clean_vignette`, `free_cam_fov`) are dev-only and not
reachable from the EXE. `vcr_overlay` is the panel's live VCR label; only its
`__main__` diagnostic is dev-only. Legacy hook migration
is not included; an incompatible camera session requires a client restart.

## Build locally (Windows)

Install Python 3.13 and run from this folder:

```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install -r requirements-build.txt
.\build.ps1
```

Default: `onedir`, with UPX disabled. Outputs:
`dist/onedir/bc2-local-replay-standalone.zip` and `dist/onedir/SHA256.txt`.
This is the panel-only build: the ZIP contains the EXE, `_internal/` runtime
directory, redirect helper and guide, but not the proxy, builder or data pack.
The full release ZIP is assembled by `release_pack.py` (see **Release
(maintainer)** below).
Keep the entire extracted folder together. Fixtures are included in the runtime.

For a comparison build, run `./build.ps1 -BundleMode onefile`; its outputs are
under `dist/onefile/`. Both modes remain unsigned. Onedir avoids self-extraction,
but changing packaging does not establish safety or guarantee browser acceptance.
Runtime requires Windows; Python is only needed to build or run from source.
No game files, secrets, local configuration or signing certificate are needed to build.
The generated EXE is unsigned; antivirus/SmartScreen warnings are possible.

## Builder CLI

`builder.exe` (from `builder_go\`) takes these commands:

```
builder list [--root DIR]... [--json]
builder extract <file.bc2raw> [--name DIR] [--level NAME] [--label TXT]
builder prune [--root DIR]... [--keep N] [--extracted] [--dry-run]
builder build <capture dir> [--suffix N] [--reference] [--publish] [--src STEM] [--env K=V|K+=V]... [--round K]
builder version [--json]
```

- `list`: capture folders and raw files with their rounds and status.
- `extract`: one `.bc2raw` into a capture folder.
- `prune`: delete raw files beyond the `--keep N` newest and/or those already
  extracted (`--extracted`); `--dry-run` only lists them.
- `build`: build a demo from one capture round; `--publish` copies the 9 demo
  files into the server's `demos/` folder (skipped when checks fail, off with
  `--reference`). The panel calls `build --publish` on **▶ Play**.
- `version`: builder version and Go version (`--json` for the panel).

## Testers

To test a release zip on a clean Windows account, follow
[docs/tester_checklist.md](docs/tester_checklist.md) and report with its result table.

## Release (maintainer)

The release ZIP `bc2-local-replay-<version>.zip` is assembled locally, not in CI.
From this folder:

1. `.\build.ps1` — the panel, PyInstaller onedir, into `dist\onedir\BC2LocalReplay\`.
2. `rome_proxy\build_shared.cmd` — the proxy, into `rome_proxy\out_shared\dinput8.dll`.
3. The recording builder is the Go `builder.exe` from `builder_go\` (Go only,
   standard library, offline). `release_pack.py` builds it itself with
   `builder_go\build.ps1 -Version <version>` (reproducible: same Go toolchain and
   sources give a byte-identical exe), or takes a prebuilt one with
   `--builder-exe` (e.g. the CI artifact). The data pack is the committed
   `datapack\`.
4. Assemble and verify the release:

   ```powershell
   python release_pack.py
   python release_pack.py --check dist\release\bc2-local-replay-<version>.zip
   ```

   Optional flags: `--builder-exe builder.exe`, `--datapack DIR`,
   `--dist dist\onedir\BC2LocalReplay`, `--proxy rome_proxy\out_shared\dinput8.dll`,
   `--out dist\release`, `--skip-exe-version`, `--dry-run`.

The zip ships `builder\builder.exe` plus `builder\builder.json` (format 2:
runtime `go`, Go version, version, commit, exe SHA-256); no Python runtime.
The Go builder is the only builder. `--check` also verifies the exe hash against `builder.json` and runs
`builder.exe version --json`. The script refuses when the parts disagree on the
version, or when a private, capture, log or config file would be included. Output in
`dist\release\`: the stage folder `bc2-local-replay-<version>\`, the ZIP (all
entries under one top-level folder `bc2-local-replay-<version>/`, with
`SHA256.txt` inside listing every file in sha256sum format) and
`bc2-local-replay-<version>.zip.sha256` next to it.

Then upload the ZIP and the `.zip.sha256` file by hand as assets of the GitHub
Release. Binaries are never committed.

**Version rule.** One version string is shared by the panel, the `dinput8.dll`
proxy and the builder, and `test_version_sync.py` checks that they agree. Bump
all three for every release: `preview_config.VERSION`, `rome_proxy\version.h`
and the builder (release_pack builds `builder.exe` with `-Version` =
`preview_config.VERSION` and writes `builder\builder.json`). The panel's setup check fails when the installed proxy
build differs from the panel.

## GitHub Actions

- Run **Build local replay** manually in Actions; choose `onedir` (default) or
  `onefile` for comparison. Download `bc2-local-replay-windows-onedir` or
  `bc2-local-replay-windows-onefile` respectively. Each artifact contains a ZIP
  and its SHA256 checksum. Version-tag builds use `onedir`.
- The workflow builds the panel ZIP (EXE, `_internal/`, `Redirect.ps1`,
  `USER_GUIDE.md`, `builder\builder.exe`) as a smoke build. It does not include
  the proxy or the data pack.
- Its `builder` job runs `go vet` and `go test` on `builder_go\` (the exams that
  need private capture data skip), builds `builder.exe` twice (the second time
  with an empty build cache), fails when the two SHA-256 differ, and uploads it
  as `bc2-builder-exe`; `build.ps1 -BuilderExe` puts it into the package.
- Push a tag such as `v0.1.0-preview.3` to create a **draft prerelease**. Its
  asset is that panel ZIP with its SHA256 checksum. Before publishing,
  the maintainer replaces the asset with the full ZIP from `release_pack.py`
  (see above). Review and test the draft before publishing it.
- Workflow configuration: `.github/workflows/build-local-replay.yml`.

CI compiles and packages the application; it does not validate game connection,
rendering, camera hooks, UAC or clean-machine/offline operation.
See [USER_GUIDE.md](USER_GUIDE.md) for current limitations.
