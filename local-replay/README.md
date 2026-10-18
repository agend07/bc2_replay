# BC2 Local Replay — build sources

This folder contains only sources and build inputs, not generated executables.
End users should download the standalone ZIP from **GitHub Releases** and follow
[USER_GUIDE.md](USER_GUIDE.md). Game/server binaries and recordings are not bundled.

## Why redirect is required

The Rome client uses a fixed login/server-list hostname. The redirect maps it to
`127.0.0.1` so it reaches the local replay backend instead of normal online Rome.
It affects system-wide hostname resolution, requires UAC only for the hosts
change, and should be disabled before returning to online play. Use dummy
credentials only and restart BC2 after changing modes. See
[the user guide](USER_GUIDE.md#why-is-the-redirect-needed) for details.

## Source layout

- `preview.py`, `launcher_panel.py`, `preview_config.py`: launcher and configuration.
- `server_build.py`: verified server patches; creates no recorder-only or test variants.
- `replay/`: Timeline controls, validated client access, camera hooks and UI.
- `backend/`: local backend and sanitized protocol fixtures.

Only Timeline playback is supported. The trace executable is an intermediate
server build, not a second controller mode. Diagnostic CLIs and legacy hook
migration are not included; an incompatible camera session requires a client restart.

## Build locally (Windows)

Install Python 3.13 and run from this folder:

```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install -r requirements-build.txt
.\build.ps1
```

Outputs: `dist/bc2-local-replay-standalone.zip` and `dist/SHA256.txt`.
The ZIP contains the EXE, redirect helper and user guide. Fixtures are embedded.
Runtime requires Windows; Python is only needed to build or run from source.
No game files, secrets, local configuration or signing certificate are needed to build.
The generated EXE is unsigned; antivirus/SmartScreen warnings are possible.

## GitHub Actions

- Run **Build local replay** manually in Actions to download a build artifact.
- Push a tag such as `v0.1.0-preview.1` to create a **draft prerelease** with the ZIP
  and SHA256 checksum. Review and test the draft before publishing it.
- Workflow configuration: `../.github/workflows/build-local-replay.yml`.

CI compiles and packages the application; it does not validate game connection,
rendering, camera hooks, UAC or clean-machine/offline operation.
See [USER_GUIDE.md](USER_GUIDE.md) for current limitations.
