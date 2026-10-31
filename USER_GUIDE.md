# BC2 Local Replay — first-run guide

Watch a recorded Battlefield: Bad Company 2 match using your own game client and
a local replay server. This is a technical preview, not a video player.

## 1. What you need

- Windows and your own supported BC2 client, including `BFBC2Game.exe` and the
  Project Rome `dinput8.dll` beside it. Rome's `dinput8.dll` must already be
  beside `BFBC2Game.exe` **before** you click **Prepare** (section 3);
  Prepare refuses to continue without it.
- Your own supported BC2 dedicated server folder containing
  `Frost.Game.Main_Win32_Final.exe` and its normal game data/DLLs. Selecting the
  folder containing only a copied EXE is not enough.
- A compatible recording. Keep all **nine companion files** together inside
  the dedicated server's `demos/` folder:
  `.fbgd`, `.fbgk`, `.fbgdd`, `.fbgdk`, `.fbidd`, `.fbidk`, `.fbcdd`, `.fbcdk`, `.txt`.
  Do not rename individual companions or select just one of them.

### Steam version of the game (experimental)

The panel supports the EA (non-Steam) `BFBC2Game.exe` (SHA-256 `3911fcc8...`) only. The proxy `dinput8.dll` can also
**record** on the Steam `BFBC2Game.exe` (SHA-256 `22e96001...`): install it by hand in the Steam game folder (rename
Project Rome's `dinput8.dll` to `dinput8_rome.dll`, copy `proxy\dinput8.dll` from the ZIP beside it) and play online; the
capture appears in `%LOCALAPPDATA%\BC2Replay\captures\` and its log in `logs\recorder_<pid>.log` (`profile steam`). The
builder turns a Steam capture into a normal recording, but **watching it needs the EA client** in the panel. Steam
recording is not yet confirmed by a live test; please report the log if it fails.

Game/server files, Project Rome and recordings are not bundled in the release
ZIP. Unsupported executable versions are rejected; this preview does not support
arbitrary game/server builds.

A user-provided, unofficial server mirror:
[Host Server BC2-Vietnam 2025 (MediaFire)](https://www.mediafire.com/file/l06p1f5i90n1bjx/Host_Server_BC2-Vietnam_2025.rar/file).
The archive's contents, safety and redistribution permissions have not been
independently verified. Download only files you trust and are entitled to use.
The setup EXE hash check verifies compatibility, not the safety of the full archive.

### Where recordings come from

**The application ZIP does not include any recordings.** There are two sources:

1. **Record your own match** (the normal way). After **Prepare** (section 3) the
   installed proxy records every online round you play into a capture file.
   Open **Recordings…** (section 7, "Recordings window"), select the round and
   click **▶ Play**: the builder turns the capture into a server recording,
   publishes it into your dedicated server's `demos/` folder and starts the
   replay. Nothing to download or copy by hand.
2. **A server-side recording** made with the dedicated server's own demo
   recorder. Copy its nine companion files directly into `demos/` inside your
   dedicated server folder (the folder you select as **Server folder** in the
   controller). Create `demos` if it does not exist; do not nest the files in a
   sub-folder and do not put them beside `BC2LocalReplay.exe`, in the client
   folder, or in `runtime/`. For example:

   ```text
   D:\BC2-Server\
     Frost.Game.Main_Win32_Final.exe
     demos\
       mymatch.fbgd
       mymatch.fbgk
       mymatch.fbgdd
       mymatch.fbgdk
       mymatch.fbidd
       mymatch.fbidk
       mymatch.fbcdd
       mymatch.fbcdk
       mymatch.txt
   ```

Any recording whose nine companion files are all present under `demos/` is
accepted, and the Demo list (section 4) shows every complete recording from the
start. A recording with missing companion files is rejected with the exact
missing names, and nothing is applied until you choose a Demo and click ▶
(play). Recordings retain the original gameplay player names and are **not
anonymized**.

## 2. Extract and open the application

Testing a release end to end? Follow [docs/tester_checklist.md](docs/tester_checklist.md) instead and fill in its result table.

1. Download the release ZIP (`bc2-local-replay-<version>.zip`) from GitHub
   Releases.
2. **Extract the entire ZIP** into a writable folder. It extracts into one
   folder, `bc2-local-replay-<version>`. Do not run the EXE from inside the
   ZIP, and do not put it in the game installation folder.
3. Keep **everything from the ZIP** together:
   - `BC2LocalReplay.exe`
   - `_internal/` — required libraries/data in the default folder (`onedir`) build;
     do not delete it or move the EXE away from it.
   - `proxy/` — the client `dinput8.dll` proxy (`proxy\dinput8.dll`) that the
     panel installs for you.
   - `builder/` and `datapack/` — the recording builder (`builder.exe`) and its data pack, used by the Recordings window.
   - `Redirect.ps1`
   - `USER_GUIDE.md`
   - `SHA256.txt` — SHA-256 of every file in the ZIP.
   A comparison `onefile` build has no `_internal/` folder and extracts its runtime
   temporarily instead. Neither packaging mode is digitally signed.
4. Open `BC2LocalReplay.exe`. Python is **not required**.
5. Leave the controller window open while playing. Do not open another controller.

The EXE is unsigned, so Windows/antivirus may warn about it. If the browser or
antivirus calls the download dangerous, do not disable protection or add an
exception blindly: report the exact warning and file hash for investigation.
Different packaging is not proof that a file is safe. Only run packages from a
source you trust. Never run the entire application as administrator. Only two
small helpers ask Windows for elevation, each with its own UAC prompt: the
client-proxy install helper that **Prepare** starts (section 3) and `Redirect.ps1`.

### SmartScreen: "Windows protected your PC" / "Unknown publisher"

Preview builds are **not digitally signed** and may have no established Windows
reputation. SmartScreen can therefore show "Windows protected your PC" and
"Publisher: Unknown publisher". This is different from an antivirus naming a
specific threat, but neither this explanation nor a clean scan proves safety.

Before deciding whether to run the application:
1. Confirm it came from this project's GitHub Release or Actions build.
2. Compare the ZIP hash with the `.zip.sha256` file published next to the ZIP
   on the Release, for example:
   `Get-FileHash .\bc2-local-replay-0.1.0-preview.4.zip -Algorithm SHA256`.
   `SHA256.txt` inside the ZIP lists every extracted file; run `Get-FileHash`
   on any file and compare by hand.
   Matching hashes check file integrity, not publisher identity or absence of malware.
3. Scan the entire extracted folder with Microsoft Defender.

If those checks are satisfactory and you choose to trust this unsigned preview,
SmartScreen's **More info → Run anyway** permits that individual launch, if the
option is available. Do not disable SmartScreen/Defender globally or add blanket
exclusions. If a browser labels the file dangerous or antivirus names a threat,
stop and report the exact warning and hash instead of assuming a false positive.

## 3. Configure paths and click Prepare

The **Setup — paths, Prepare and Clean up** section at the top of the controller
is collapsed once everything is ready. It opens automatically while a path is
missing or Prepare has not run yet. Click **▸ Setup** to open it yourself.

1. Beside **Server folder**, click **Browse…** and select the dedicated server
   folder containing `Frost.Game.Main_Win32_Final.exe` (not the client folder).
2. Beside **BC2 client EXE**, click **Browse…** and select the actual
   `BFBC2Game.exe` file (not a shortcut or launcher).
3. Valid paths are saved automatically as you edit or browse. The recording list refreshes automatically. Check the panel for validation or save errors.
4. Close the game and the replay server, then click **Prepare**. Do not copy any
   DLL by hand. No recordings are needed yet; demos come later (Recordings
   window or copied demo files).
5. Read the confirmation. Prepare does only what is missing, in this order, and
   the dialog lists exactly those steps:
   - "Install the dinput8.dll proxy into <game folder> (Windows will ask for
     administrator permission)". Project Rome's `dinput8.dll` is kept as
     `dinput8_rome.dll` and the project's proxy is copied in as `dinput8.dll`.
     It loads `dinput8_rome.dll` and forwards to it, so Rome keeps working and
     online play is unchanged. An older proxy build is upgraded and the previous
     one kept as `dinput8.dll.pre_proxy`.
   - "Install the dinput8.dll proxy into <server folder>". The server folder
     needs it too: without it the replay server talks to the real online
     backend instead of the local one. This normally needs no UAC prompt.
   - "Build the playback server executables in <server folder>":
     `Frost.Game.Main_Win32_Final.ReplayTimeline.exe` and
     `Frost.Game.Main_Win32_Final.PlaybackOnlineTrace.exe` next to the original.
     Your original server EXE is not overwritten and no admin password or
     identity is copied from an existing server.
   - "Create the local preview instance under runtime\"
     (`runtime\Instance-local-preview`; an existing one is preserved).

   The dialog ends "Nothing else is changed. Continue?". If nothing is needed
   there is no dialog, only the checks.
6. Answer the Windows permission (UAC) prompt. The game folder is usually under
   `C:\Program Files\EA Games\Battlefield Bad Company 2`, so Windows asks for
   administrator permission for a small PowerShell helper script
   (`runtime\client_install_<stamp>.private.ps1`, kept so you can inspect it)
   that only does the file operations. The panel itself never runs as
   administrator.
7. Wait. Progress shows in the status line under the buttons ("Prepare:
   installing client proxy… (answer the Windows permission prompt)", "Prepare:
   building the playback server executables…", "Prepare: checking setup…"). Do
   not click another start button while Prepare is working.
8. Read the result in the status line. Prepare ends with six checks. "Setup
   check: all 6 ok." means you are done; the button then reads **Check**
   (clicking it re-checks and still does anything that turned out missing). If a
   check failed, a **Setup check** report window opens (only then) with one line
   per check: `OK`, `FAIL` or `--`, then the name and a text. Fix any `FAIL`
   line and click the button again:
   - **BC2 client EXE verified.** Your `BFBC2Game.exe` must be the retail build
     (SHA-256 `3911fcc8...`); other builds are unsupported. Fix: select the
     correct file in **BC2 client EXE**.
   - **Client proxy installed.** Fix: click **Prepare** (step 4) and read the
     status line if it stops.
   - **Server folder** contains `Frost.Game.Main_Win32_Final.exe`. Fix: select the
     dedicated server folder, not the client folder.
   - **Server folder proxy installed.** Fix: click **Prepare** again with the
     replay server stopped.
   - **Builder and data pack** found next to the panel. Fix: re-extract the whole
     ZIP so `builder/` and `datapack/` sit beside `BC2LocalReplay.exe`.
   - **`%LOCALAPPDATA%\BC2Replay` is writable** (captures, logs, crash dumps).
     Fix: make sure your user account can write to that folder and that
     security software is not blocking it.

The setup check also reports a version mismatch between the installed proxy and
the panel (`proxy build x, panel y: reinstall from this zip`, a `FAIL`). Fix:
run **Prepare** from the ZIP you extracted; it upgrades the proxy.

Two messages mean something specific:

- **"Prepare stopped: <reason>"**: nothing was changed. Reasons: `msimg32.dll`
  (the standalone chat/smoke filter from github.com/agend07/bc2-chat-smoke, or
  the older bc2_mute) is in the game folder (remove it yourself first: the
  proxy contains the same chat commands, both swap the same vtables, and the
  two must never be loaded together); the game's `dinput8.dll` is neither Project Rome nor this
  project's proxy; there is no `dinput8.dll` at all (install Project Rome
  first); or a path is not set.
- **"Prepare failed at <step>: <message>"**: that step failed. Earlier steps
  stay done. Fix the cause and click the button again.

Leave the proxy installed. It stays in place for normal online play too, where
it records your online rounds (see **Recordings window** in section 7 and
**Privacy and limits**), provides the in-game chat commands (such as `/mute`),
writes crash minidumps (section 6) and lets local replay work without a `hosts`
entry. Its chat commands also include `/smoke` (colours by name or `#rrggbb`,
animations, `/smoke halloween`, presets pumpkin/ghost/blood), `/fx` (recolour
other effects: muzzle flashes, tracers, impacts, fire), `/hash` and
`/help`; see [rome_proxy/README.md section 5](rome_proxy/README.md#5-in-game-chat-commands-r255-shared-build).

You normally do this once per extracted application folder. Configuration is
saved as `preview.json` beside the EXE.

### Clean up…

**Clean up…** removes what Prepare created: the proxy in the game folder
(Project Rome's `dinput8.dll` is restored; administrator prompt), the proxy in
the server folder, the built playback executables
`Frost.Game.Main_Win32_Final.ReplayTimeline.exe` and
`Frost.Game.Main_Win32_Final.PlaybackOnlineTrace.exe`, and
`runtime\Instance-local-preview`. Your game, server files and recordings stay.
It asks for confirmation first and refuses while the game or a dedicated
server is running, so close both first. Use it before uninstalling the game or
to go back to a plain Rome install; click **Prepare** later to set up again.

## 4. Select the recorded match — the menu may initially be empty

**The application does not initially know your recording folder. The Demo field
can be blank on first launch, even after you save paths. Select the match explicitly:**

1. Find **Server Recording (demos/)** below the Setup section.
2. Click the refresh button (⟳) after saving your paths and placing recordings in `demos/`.
3. Open the **Demo** drop-down menu and **choose the recorded match you want to watch**.
   Entries show the recording name, map and duration.

If the menu is still empty, check the server folder path and that all nine files
are extracted directly into its `demos/` directory. Click the refresh button again.
Invalid/incomplete recordings are not listed.

**Selecting an entry does not start playback or switch the server by itself.**
Click the **▶** (play) button beside the Demo menu after selecting the match.

## Why is the redirect needed?

The controller no longer has Redirect buttons. With the `dinput8.dll` proxy
(`rome_proxy/README.md`), the game started by the controller connects to your PC
without a `hosts` entry. Without the proxy, run `Redirect.ps1` yourself
(`-Mode on` / `-Mode off`, see section 8). The rest of this section describes
that manual fallback.

The Project Rome client normally contacts `bfbc2.emulatornexus.com` for its login
and server-list services. This preview runs replacement services **on your own
PC**. Without the redirect, the client would contact the normal online service,
not this local backend, and would not discover the local replay through it.

`Redirect.ps1 -Mode on` adds one marked block to the Windows `hosts` file, mapping that
hostname to `127.0.0.1` (your PC). It changes where the client connects; it does
not patch the game, download a server list or start the backend. The ▶ (play) button
is still required to run the local backend and replay server.

Windows administrator permission is needed only to change `hosts`. The mapping
affects the hostname system-wide, not just this controller. While it is enabled,
use **dummy credentials only**, and do not try normal online Rome login.
`-Mode off` removes only this application's marked block so normal name
resolution can resume. It does not remove another tool's mapping. Restart BC2
after either change because an already-running game may retain old connections
or cached addresses. The helper also requests a Windows DNS-cache flush.

The hosts file entry does not prove network isolation and does not validate VPN/DNS software. Do not expose the backend to other computers.

## 5. Start the replay; the game starts by itself

Close any older dedicated server or local backend before starting.
Do not run two copies of this stack at once.

1. With your match selected in **Demo**, click **▶** (play). It starts
   at once: it starts the local backend, waits for its readiness message, then
   starts the replay server with the selected match. Do not click it repeatedly.
2. When the server reports ready, the controller starts BC2 itself. The game logs in and joins
   **Local Replay** automatically. If BC2 was already running, ▶ closes it first
   (a client left running gets confused by the new match) and starts a fresh one.

If an older backend is still using the ports, Replay stops with an error
rather than silently reusing it. Close the older backend first. Switching matches
from the same open controller reuses the backend it already started.

**"Local backend port 80 is occupied":** a backend from an older application
folder may still be running (for example after a crash; a normal close stops
it). Close BC2 first. In Task Manager → Details, show the **Command line** column and identify
the old `BC2LocalReplay.exe` process with the `backend` argument and the old
folder path. End only that old backend, not unrelated processes. Then select the
Demo in the new controller and click ▶ (play) again. Reinstalling will
not free occupied ports.

If a step reports failure, stop at that step and inspect the newest
`runtime/launcher_*.private.log` locally. A started process is not yet proof that
BC2 has connected successfully.

## 6. Log in, join and play

1. If BC2 asks for a login, use **any dummy email/password — NEVER your real
   credentials**. This is a local test backend, not your normal online account.
2. If it did not join by itself: open **Server Browser**, clear restrictive name
   filters, click **Search** and join **Local Replay**. Your recording is chosen in
   the controller's Demo menu, not by logging in as one of its original players.
3. **Playback starts by itself** once BC2 is in the map (after ▶ the controller
   waits for the map, then plays). Press **SPACE** in the BC2 window to pause.
4. Many recordings start with an empty load phase (nothing moves for about a
   minute). Once the game accepts seeks (a few seconds after the map shows), the
   controller seeks by itself to about 3 seconds before the game starts, keeping
   play/pause as it is. It does this once per join and not at all if you seek
   first; recordings without such a gap are left alone.

Do not repeatedly restart the stack just because loading takes a while. If the
server is missing, check the backend/server launch results in the controller.

**If the game crashes:** the `dinput8.dll` proxy writes a minidump to
`%LOCALAPPDATA%\BC2Replay\dumps\BFBC2Game_<pid>_<yyyymmdd_hhmmss>.dmp` and a one-line
log to `%LOCALAPPDATA%\BC2Replay\logs\dump_<pid>.log` (the newest 5 dumps are kept;
the game's own crash handling still runs afterwards). To report it, send the newest
`.dmp` plus the matching `dump_<pid>.log` and `recorder_<pid>.log` from the `logs`
folder (`<pid>` is the number in the dump name). The dump holds memory of the game
process, including player names and chat, so send it only if you are fine with
that; `dumps=0` under `[dll]` in `%LOCALAPPDATA%\BC2Replay\config.ini` turns dumps off.

## 7. Timeline and camera controls

The **ⓘ Help** button in the controller shows a short version of this list.

- **Time**: type seconds, `mm:ss` or `hh:mm:ss` and press **Enter** to seek.
  Times outside the recording are rejected next to the field. Seeking leaves
  playback paused; press SPACE in the game to continue. A few seek points in
  some recordings would make the game drop or crash the client (the followed
  player's view still points at an object deleted at that moment); a seek there
  lands on the previous safe point, a few seconds earlier, and the status
  message says so; press Play to watch through that moment.
- **Keep destruction (slower seek)**: a plain seek is instant but destruction does
  not follow it (the game itself works that way, in server demos too): going back,
  the controller repairs walls and houses, so the map looks as at the start; going
  forward, what was destroyed on the way is missing. With this option on, a seek
  (typed time and the in-game arrows) plays to the target fast instead: up
  to 50x, slowing down at the end; going back (or after a plain seek) it first
  repairs walls and houses and replays from 0:00. The map then shows exactly the
  destruction up to that moment. It takes about 1.3 s per minute of recording
  (10:00 = about 13 s) and lands within about a second; playback is paused there
  as after a plain seek. While it replays, the middle of the game window reads
  `REWINDING… please wait` (or `FAST FORWARD…`); other seeks wait until it is gone. Small props that break in steps (some fences, crates)
  cannot be repaired and may stay broken after going back. The repair needs the
  map state from when BC2 joined: if BC2 joined after the game had started, walls
  are not repaired.
- Hover the mouse over a control for a short explanation.
- **▾ Messages** above the message text at the bottom hides or shows it (kept for the next start).
- There is no Launch client button: ▶ (play) starts BC2. **VCR label** shows the replay time over
  the game window: green ▶ PLAY or red ⏸ PAUSE, the time, then FREE (free camera)
  or FPV (player view); a second line reads `SPACE to pause` (playing) or `SPACE to play` (paused). With **Show looked-at player** (on by default) the FREE
  label also names the player nearest the screen centre (within about 10 degrees
  and the **max** distance next to the checkbox: 10-300 m, default 50 m, kept for
  this session only), e.g. `⏸ PAUSE 03:45 FREE · Davorich`. The top-right
  corner shows the name of the demo being played (e.g. `cl04_1088`) in the same
  colour; it follows the **VCR label** checkbox too.
- The label is a separate window, so exclusive fullscreen hides it. Cheap fix:
  in the game's video options choose **Windowed** at your desktop resolution
  and put `"client_borderless": true` in `preview.json`; the panel then strips
  the window frame and stretches BC2 over the monitor as soon as its window
  appears (the overlay stays visible, Alt-Tab keeps working).
- In-game keys (BC2 window in front, server ready):
  - F: free camera on/off (off restores the normal player view).
  - Space: Play/Pause.
  - Left/Right: step backward/forward 3 seconds.
  - Shift + Left/Right: step backward/forward 10 seconds.
  - `<` / `>` (the comma and period keys): Focus the previous / next player in
    the **Player** menu order, skipping players without a live soldier and
    wrapping around. Starts the free camera if it is off. One step per press.
- Free camera: use mouse + WASD in BC2; hold Shift to move faster. Left / right
  mouse button = up / down (also while the deploy screen is still up).
- The **Player** menu fills itself from the demo's player list, and from the
  game once you have joined.
- Choose a player, then **Focus** for a one-shot camera move (it starts the free
  camera if needed), or **Follow (experimental)** to track them; it can look jittery.
- While following, the same button reads **Stop follow**: it leaves the free camera
  at its last position. Pressing F in game also ends Follow and restores the player
  view. Closing the controller ends everything it started (section 8).

To change matches later: stop Follow, choose another Demo, click
**▶ (play)**. BC2 is closed and started again for the new match; press SPACE
again once it has joined.

### Recordings window (client captures)

**Recordings…** (top right, next to Help) opens a list of client captures made
with the gameplay recorder, newest first, one row per round: name (with
` · round K` when the capture holds more than one round, e.g. the next round
on the same server), date and time recorded, map, length of the round, packets,
drops and status (`ready, not built yet`, the demos already built from that
round, or why it cannot be built, e.g. `no capture.json`). Rounds shorter than
30 s are grey with status `too short`; empty rounds are not listed. The release
zip ships the builder (`builder\`) and its data
pack (`datapack\`) next to `BC2LocalReplay.exe`; the panel finds them
automatically and the Setup section shows whether they were found. Its working
files go to `runtime\builder\`. The builder is a single `builder\builder.exe`
(no Python needed). `preview.json` `"pov_builder": {"exe": ...}` is only for
developers (another `builder.exe`) (optional
`"capture_roots": [...]` adds capture folders). The window writes nothing to
the game or the server; the list refreshes every 5 s while it is open and idle.

- **▶ Play** plays the selected round's demo: the window hides and the main
  window starts its replay (reopen it with **Recordings…**). When the round has
  no demo yet, ▶ Play builds and publishes it to `demos/` first (a few minutes,
  after a confirmation; a progress bar shows the current step, the elapsed time
  and the expected total, the log shows each step) and plays it when the build
  succeeds.
- **Rebuild** builds the selected round again and publishes a new demo, without
  playing it.
- `preview.json` `"recordings_hide"` (optional, a list of name patterns such as
  `["v3-trial-*", "cap_r248_*"]`) leaves matching captures and raw files out of
  the window; nothing is deleted. Without the key the builder's own reference
  trials (`v3-trial-*`) are hidden; `[]` shows everything.
- Raw recorder files (`.bc2raw` in `%LOCALAPPDATA%\BC2Replay\captures`, written
  by the recorder DLL) show as `raw, not extracted` with their size. The window
  extracts them by itself, one at a time, into capture folders in the builder's
  working files once a file has not changed for a minute (a file still being
  recorded is left alone), never during a build or while the controller runs a
  replay. The raw row is then replaced by its round rows. A file that fails to
  extract is not retried until the window is reopened.
- Raw files take about 6.7 MB per minute. The row under the buttons,
  **Raw files: keep newest [N] (0 = all)** and **delete raws already
  extracted**, sets what may be removed (saved in `preview.json` under
  `"captures"`). Nothing is deleted automatically: **Clean up…** first checks
  which raw files the setting would remove and lists them with their sizes
  (files that may still be recording or are in use are skipped); they are
  deleted only after you confirm, then the list refreshes.
- One build or extract at a time. A build is refused while the controller runs
  a replay server or a BC2 it started: close the controller (it stops both),
  reopen it, then build.
- Captures contain every player name and chat. Conversion never emits chat,
  but do not share the capture folders.

**Supported maps.** Builds use the data pack. Maps with a reference baseline
today: MP_006CQ (Arica Harbor), MP_005 (Atacama Desert), MP_SP_005CQ (Heavy
Metal). MP_007 (Laguna Presa) and MP_003 (Laguna Alta) have a server-demo
baseline only (first builds, untested in game). Other maps in the map pack
build but may fail the gate until their templates are added.

## 8. Finish and restore normal online Rome

1. Close the controller. It stops what it started, in this order: the BC2 it
   launched after ▶ (play) (asked to close, ended
   after about 5 seconds), the replay server, then the local backend. Nothing
   else is touched: a BC2 you started yourself keeps running and gets its normal
   player view back first. If restoration fails while that BC2 is running, the
   controller asks whether to close anyway; restart BC2 to clear any remaining
   hook if you choose that option. `BC2LocalReplay.exe stop-server` in the
   application folder still stops this installation's replay server by hand.
2. If the controller was force-ended without its normal close (for example from
   Task Manager), the next controller you open stops the BC2 and replay server
   the old one started (only those exact processes) when it opens.
3. If you turned the hosts redirect on by hand, turn it off from PowerShell in
   the extracted application folder (approve UAC). Restart BC2 before
   attempting normal online Rome login.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Redirect.ps1 -Mode off
```

Do not delete/move `Redirect.ps1` until online mode has been restored.

## Privacy and limits

- Do not share generated `preview.json` or `runtime/`: they contain local paths,
  session state, private logs, generated admin credentials and hosts backups.
- Do not expose the local backend to the internet.
- Startup and playback from a freshly extracted folder were user-confirmed on
  the development PC. A different-PC installation and disconnected-network test
  remain pending. This is not a guarantee for every recording/build or camera case.
- A successful GitHub Actions build is not a gameplay test.
- Crash dumps under `%LOCALAPPDATA%\BC2Replay\dumps` contain memory of the game
  process (player names, chat text). Keep them private and share them only on
  request; `dumps=0` in `config.ini` disables them.
