# BC2 / Project Rome — server-side match recording

Goal: record full Battlefield: Bad Company 2 matches on a Project Rome
dedicated server, so the data can be used later for cheat review / analysis.
This guide covers the **recorder** only. For watching a recording, see the
[first-run replay guide](../local-replay/USER_GUIDE.md); the project overview is in
[the main README](../README.md).

Status: **working and verified.** Two recorded matches are included.

---

## What we achieved

- BC2 dedicated servers have a **built-in demo/ghost recorder**. It captures all
  players: position + orientation at ~30 Hz, plus world destruction and spotting.
- Out of the box it could not be switched on from the command line (the setting
  was read but stayed empty). We fixed that with a **small binary patch** in a
  separate test copy of the server. The original executable is untouched.
- During play the recorder buffers to RAM (files on disk stay 0 bytes). The buffer
  is written to disk when the level unloads (round end / map change). We also added
  a way to **force a save while the match is still running**.
- Output is 9 companion files per recording. Verified readable.

## How it works

1. The server reads `Demo.RecordDemoFileName`. When set, it creates a ghost recorder.
2. In the stock binary the command-line value was ignored, so recording stayed off.
   The patch makes the recording-filename getter return the command-line value.
3. Frames accumulate in a RAM buffer during play (0-byte files on disk are normal).
4. On level unload the buffer is finalized and flushed to disk.
5. Forced save: clearing the "recording enabled" flag in the server's DemoControl
   object makes the server finalize the recorder and write to disk immediately.

## How to reproduce

### Requirements
- Windows, the extracted BC2 dedicated server folder, Project Rome `dinput8.dll`
  next to the server exe, Python 3.

For a user-provided third-party server download link and its caveats, see
[Dedicated server files](../README.md#dedicated-server-files). The tools require
the exact supported server executable listed below; the external archive itself
has not been independently verified.

### Install the recorder tools

Copy the four `.py` files and three `.cmd` launchers from this directory into
your dedicated server folder, beside `Frost.Game.Main_Win32_Final.exe`.
Keep them together: these tools resolve server/demo paths relative to their own
location, not relative to the directory from which you run a command.
Do not overwrite existing instance settings or server files.

The commands below are run **from that dedicated server folder** after copying.
The `.cmd` launchers can also be double-clicked or called from another working
directory. Python 3 must be available as `python` on PATH.
`config-examples/` contains examples only; use them as a reference when preparing
your own instance, not as replacements for a working server's identity/password.

### 1. Build the patched recorder
```
python BuildBC2RecordingTest.py
```
- Input: `Frost.Game.Main_Win32_Final.exe`
  SHA-256 `CA8169FA03A29E02AF1BF8DB8770AB0E68F13A95B010C52ECA23733068298347`
- Output: `Frost.Game.Main_Win32_Final.RecordingTest.exe`
  SHA-256 `0797D48205F559BCB7FB312C87A4B3290C310AA93E1B8D2D0D475E683AA2B309`
- Patch: redirects the recording-filename getter call at VA `0x00647995` to a
  63-byte adapter at `0x0165DB40` (verified zero padding in .text).

### 2. Configure the instance
- `Instance-bc2\maplist.txt` — map/mode (example used: `levels/mp_006cq`, Arica Harbor Conquest).
- `Instance-bc2\AdminScripts\Startup.txt` — server vars and `levelVars.set` for
  tickets. Note: BC2 clamps low ticket values (setting 100 showed as 350).

### 3. Launch the recording server
Double-click `StartBC2RecordingPatchedTest.cmd` in the server folder, or run:
```
Frost.Game.Main_Win32_Final.RecordingTest.exe ^
  -server -dedicated -serverInstancePath "Instance-bc2/" ^
  -mapPack2Enabled 1 -port 19567 -timeStampLogNames ^
  -Demo.RecordDemoFileName "demos/patched_<timestamp>"
```
Note the printed base name, e.g. `demos/patched_20261017_123219_486`.

### 4. Play
Players join through the Project Rome server browser (name from
`ServerOptions.ini`, e.g. "BC2 AIR India"). Keep the server running.

### 5. Save the recording
- Automatic: when the round ends / the level (re)loads.
- Forced, mid-match: run `ZapiszNagranie.cmd`
  (calls `bc2_stop_recording.py --confirm`).
- Do **not** force-kill the server: buffered data is lost.

### 6. Output files (per recording)
| Suffix | Meaning |
|---|---|
| `.fbgd` / `.fbgk` | ghost delta frames / keyframe index |
| `.fbidd` / `.fbidk` | impact destruction |
| `.fbcdd` / `.fbcdk` | composite destruction |
| `.fbgdd` / `.fbgdk` | group destruction |
| `.txt` | metadata (level, build) |

The eight binary files stay 0 bytes during play and are written on save.

### 7. Optional live monitor
`StartBC2RecorderMonitor.cmd` — read-only view of recording state, frame counter,
RAM buffer growth, and the demo files.

### 8. Verify a recording
```
python InspectBC2Demo.py demos/<base>
```
Prints level, keyframe/delta counts and duration.

## Included data (2 matches)

| Recording | Level | Duration | Players |
|---|---|---|---|
| `demos/patched_20261017_123219_486` | Arica Harbor Conquest | ~775 s | Air India, grimm_au |
| `demos/patched_20261017_125023_912` | Arica Harbor Conquest | ~3550 s | Air India, Pollux_cz |

Each recording is the full 9-file family. Keep all files of a family together.
They are included in this repository as `recordings_pt1.zip` (unzip into `demos/`
to use them). Note: they contain player names.

## Files in this directory

| File | Purpose |
|---|---|
| `README.md` | this recording guide |
| `BuildBC2RecordingTest.py` | builds the patched recording server exe |
| `StartBC2RecordingPatchedTest.cmd` | launches the recording server |
| `StartBC2RecorderMonitor.cmd` | starts the live monitor (runs `BC2RecorderMonitor.py`) |
| `BC2RecorderMonitor.py` | read-only live monitor of the recording state |
| `ZapiszNagranie.cmd` | forces a save of the current recording |
| `bc2_stop_recording.py` | clears the recording flag (used by `ZapiszNagranie.cmd`) |
| `InspectBC2Demo.py` | verifies/reads a recording |
| `config-examples/maplist.txt` | example map list |
| `config-examples/Startup.txt` | example server/`levelVars` startup script |
| `config-examples/ServerOptions.ini` | example server options |
| `recordings_pt1.zip` | the two recorded matches above (full 9-file families) |
The repository's root `.gitignore` excludes server binaries, runtime data and
extracted match files from publication.

Not included: the server/game binaries (EA/DICE).

## Notes / limits

- The recording is server-side ghost data (position/orientation), not a video.
- Player names are stored bit-packed in the payload; `InspectBC2Demo.py` can find
  a known name in the data.
- Recording starts with the server, so an empty intro before the first player
  joins is included.
- Ticket values are clamped/rounded by the engine for Conquest.