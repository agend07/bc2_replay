# BC2 Local Replay — first-run guide

Watch a recorded Battlefield: Bad Company 2 match using your own game client and
a local replay server. This is a technical preview, not a video player.

## 1. What you need

- Windows and your own supported BC2 client, including `BFBC2Game.exe` and the
  Project Rome `dinput8.dll` beside it.
- Your own supported BC2 dedicated server folder containing
  `Frost.Game.Main_Win32_Final.exe` and its normal game data/DLLs. Selecting the
  folder containing only a copied EXE is not enough.
- A compatible recording. Keep all **nine companion files** together inside
  the dedicated server's `demos/` folder:
  `.fbgd`, `.fbgk`, `.fbgdd`, `.fbgdk`, `.fbidd`, `.fbidk`, `.fbcdd`, `.fbcdk`, `.txt`.
  Do not rename individual companions or select just one of them.

Game/server files, Project Rome and recordings are not bundled in the standalone
ZIP. Unsupported executable versions are rejected; this preview does not support
arbitrary game/server builds.

A user-provided, unofficial server mirror:
[Host Server BC2-Vietnam 2025 (MediaFire)](https://www.mediafire.com/file/l06p1f5i90n1bjx/Host_Server_BC2-Vietnam_2025.rar/file).
The archive's contents, safety and redistribution permissions have not been
independently verified. Download only files you trust and are entitled to use.
The setup EXE hash check verifies compatibility, not the safety of the full archive.

### Download the sample recordings from this repository

**The application ZIP does not include any recordings. Download them separately
before clicking Setup / Build:**

1. Open [recording/recordings_pt1.zip on GitHub](https://github.com/agend07/bc2_replay/blob/main/recording/recordings_pt1.zip).
2. Click **Download raw file** (the download icon). Save the ZIP to your PC;
   do not save the GitHub web page as a file.
   Alternatively, use this [direct ZIP download](https://github.com/agend07/bc2_replay/raw/refs/heads/main/recording/recordings_pt1.zip).
3. Extract the ZIP to a temporary folder. Open its **`matches/`** directory:
   it contains two recordings, each with nine companion files. Keep every
   recording's files together and keep their original names.
4. Copy the **files from inside `matches/`** into **`demos/` inside your dedicated
   server folder** — the folder you select as **Server folder** in the controller.
   Create `demos` if it does not exist. Do **not** put them beside
   `BC2LocalReplay.exe`, in the client folder, or in `runtime/`.
5. Check that the files are directly inside `demos/`, not nested under a
   `matches/`, another `demos/` or a `recordings_pt1/` directory. For example:

   ```text
   D:\BC2-Server\
     Frost.Game.Main_Win32_Final.exe
     demos\
       patched_20261017_125023_912.fbgd
       patched_20261017_125023_912.fbgk
       patched_20261017_125023_912.fbgdd
       patched_20261017_125023_912.fbgdk
       patched_20261017_125023_912.fbidd
       patched_20261017_125023_912.fbidk
       patched_20261017_125023_912.fbcdd
       patched_20261017_125023_912.fbcdk
       patched_20261017_125023_912.txt
   ```

6. Save the server/client paths in the controller, complete **Setup / Build**,
   then click **Refresh list** under **Server Recording (demos/)** and choose a
   match from the **Demo** menu. Click **Start Replay** when ready to start it.

For initial setup, the current default is the Arica Harbor Conquest sample
`patched_20261017_125023_912`. Its nine files must be present before Setup / Build.
The archive also contains `patched_20261017_123219_486`; you can select it after
setup. Sample recordings retain the original gameplay player names and are
**not anonymized**.

## 2. Extract and open the application

1. Download the standalone ZIP from GitHub Releases.
2. **Extract the entire ZIP** into a writable folder. Do not run the EXE from
   inside the ZIP, and do not put it in the game installation folder.
3. Keep **everything from the ZIP** together:
   - `BC2LocalReplay.exe`
   - `_internal/` — required libraries/data in the default folder (`onedir`) build;
     do not delete it or move the EXE away from it.
   - `Redirect.ps1`
   - `USER_GUIDE.md`
   A comparison `onefile` build has no `_internal/` folder and extracts its runtime
   temporarily instead. Neither packaging mode is digitally signed.
4. Open `BC2LocalReplay.exe`. Python is **not required**.
5. Leave the controller window open while playing. Do not open another controller.

The EXE is unsigned, so Windows/antivirus may warn about it. If the browser or
antivirus calls the download dangerous, do not disable protection or add an
exception blindly: report the exact warning and file hash for investigation.
Different packaging is not proof that a file is safe. Only run packages from a
source you trust. Do not run the entire application as administrator;
only the redirect helper needs elevation.

### SmartScreen: "Windows protected your PC" / "Unknown publisher"

Preview builds are **not digitally signed** and may have no established Windows
reputation. SmartScreen can therefore show "Windows protected your PC" and
"Publisher: Unknown publisher". This is different from an antivirus naming a
specific threat, but neither this explanation nor a clean scan proves safety.

Before deciding whether to run the application:
1. Confirm it came from this project's GitHub Release or Actions build.
2. Compare the ZIP hash with the accompanying `SHA256.txt`, for example:
   `Get-FileHash .\bc2-local-replay-standalone.zip -Algorithm SHA256`.
   Matching hashes check file integrity, not publisher identity or absence of malware.
3. Scan the entire extracted folder with Microsoft Defender.

If those checks are satisfactory and you choose to trust this unsigned preview,
SmartScreen's **More info → Run anyway** permits that individual launch, if the
option is available. Do not disable SmartScreen/Defender globally or add blanket
exclusions. If a browser labels the file dangerous or antivirus names a threat,
stop and report the exact warning and hash instead of assuming a false positive.

## 3. Configure paths and perform one-time setup

The **Setup / Launcher** panel separates its buttons into three groups:
**Setup** (paths/build), **Redirect** (local/online routing), and **Start** (replay/game).

In the **Setup** group:

1. Beside **Server folder**, click **Browse…** and select the dedicated server
   folder containing `Frost.Game.Main_Win32_Final.exe` (not the client folder).
2. Beside **BC2 client EXE**, click **Browse…** and select the actual
   `BFBC2Game.exe` file (not a shortcut or launcher).
3. Click **Save paths**. Wait for the confirmation in the panel.
4. Click **Setup / Build** and wait until it reports completion. Do not click
   another start button while setup is working.

Setup verifies the supplied files, builds separate playback server executables
and creates `runtime/Instance-local-preview`. It does not overwrite your original
server EXE or copy an existing server's admin password/identity.

You normally do this once per extracted application folder. Configuration is
saved as `preview.json` beside the EXE. An existing preview instance is preserved.

## 4. Select the recorded match — the menu may initially be empty

**The application does not initially know your recording folder. The Demo field
can be blank on first launch, even after you save paths. Select the match explicitly:**

1. Find **Server Recording (demos/)** below the setup panel.
2. Click **Refresh list** after saving your paths and placing recordings in `demos/`.
3. Open the **Demo** drop-down menu and **choose the recorded match you want to watch**.
   Entries show the recording name, map and duration.

If the menu is still empty, check the server folder path and that all nine files
are extracted directly into its `demos/` directory. Click Refresh list again.
Invalid/incomplete recordings are not listed.

**Selecting an entry does not start playback or switch the server by itself.**
Use **Start Replay** in the launcher panel after selecting the match.
**Switch Recording** beside the Demo menu uses the same startup/switch operation.

## Why is the redirect needed?

The Project Rome client normally contacts `bfbc2.emulatornexus.com` for its login
and server-list services. This preview runs replacement services **on your own
PC**. Without the redirect, the client would contact the normal online service,
not this local backend, and would not discover the local replay through it.

**Redirect On** adds one marked block to the Windows `hosts` file, mapping that
hostname to `127.0.0.1` (your PC). It changes where the client connects; it does
not patch the game, download a server list or start the backend. Start Replay is
still required to run the local backend and replay server.

Windows administrator permission is needed only to change `hosts`. The mapping
affects the hostname system-wide, not just this controller. While it is enabled,
use **dummy credentials only**, and do not try normal online Rome login.
**Redirect Off** removes only this application's marked block so normal name
resolution can resume. It does not remove another tool's mapping. Restart BC2
after either change because an already-running game may retain old connections
or cached addresses. The helper also requests a Windows DNS-cache flush.

The displayed status checks the hosts file; it does not prove network isolation
or validate VPN/DNS software. Do not expose the backend to other computers.

## 5. Start the local replay services and game

Close any older BC2 client, dedicated server or local backend before starting.
Do not run two copies of this stack at once.

1. Check the **Redirect** status below the buttons. If it says **OFF**, click
   **Redirect On** and approve the Windows administrator (UAC) prompt; wait for
   completion. If it already says **LOCAL**, On is disabled because the preview
   redirect is already enabled; you do not need to enable it again.
   This redirects the Rome login hostname to your local PC.
   **Restart BC2 if it was running when you changed redirect mode.**
   Only the relevant On/Off action is enabled. An externally managed mapping,
   malformed preview block or unreadable hosts file is reported instead of
   being changed automatically. Status is read-only and refreshes every two seconds;
   it checks hosts, not the routing/DNS state already cached inside a running game.
2. With your match selected in **Demo**, click **Start Replay** and confirm the
   dialog. It starts the local backend, waits for its readiness message, then
   starts the replay server with the selected match. Wait for the server-started
   message before continuing; do not repeatedly click Start Replay.
3. Click **Launch Game**.

If an older backend is still using the ports, Start Replay stops with an error
rather than silently reusing it. Close the older backend first. Switching matches
from the same open controller reuses the backend it already started.

**"Local backend port 80 is occupied" and Play stays disabled:** a backend from
an older application folder may still be running, even after its controller was
closed. Close BC2 first. In Task Manager → Details, show the **Command line**
column and identify the old `BC2LocalReplay.exe` process with the `backend`
argument and the old folder path. End only that old backend, not unrelated
processes. Then select the Demo in the new controller, click Start Replay, wait
for the server to start, and Launch Game again. Reinstalling or repeatedly
clicking Play will not free occupied ports.

If a button reports failure, stop at that step and inspect the newest
`runtime/launcher_*.private.log` locally. A started process is not yet proof that
BC2 has connected successfully.

## 6. Log in, join and press Play

1. In BC2, log in using **any dummy email/password — NEVER your real credentials**.
   This is a local test backend, not your normal online account.
2. Open **Server Browser**, clear restrictive name filters and click **Search**.
3. Find and join **Local Replay**. Your recording is chosen in the controller's
   Demo menu, not by logging in as one of its original players.
4. Return to the controller and click **Play**. The button is disabled until a
   Demo is selected, that recording has been applied with Start Replay, and the
   server reports ready. Selecting a different match disables Play until you
   start/switch to it. Space uses the same guard.
   **Timeline starts paused**; joining alone does not start the recorded match.
   You can click Play while the game is still loading, once the button is enabled.
5. Return to BC2 and wait for the map and moving replay scene to appear.

Do not repeatedly restart the stack just because loading takes a while. If the
server is missing, check backend/server launch results and confirm Redirect On
completed before the client was started.

## 7. Timeline and camera controls

- **Play / Pause**: start or stop replay time.
- **Time**: enter seconds, `mm:ss` or `hh:mm:ss`, then **Seek and pause**.
  Seeking leaves playback paused; click Play to continue.
- **Rewind to start**, **±1s / ±5s**: move through the recording.
- **Enable free camera**: use mouse + WASD in BC2; hold Shift to move faster.
- With free camera enabled and BC2 focused:
  - Space: Play/Pause.
  - Left/Right: step backward/forward 1 second.
  - Shift + Left/Right: step backward/forward 5 seconds.
- **Refresh players**: populate the player menu from currently spawned players.
  That menu can also initially be empty; join/load the replay before refreshing.
- Choose a player, then **Focus** for a one-shot camera move, or **Follow** to track
  them. Follow is experimental and can look jittery.
- **Stop Follow** leaves free camera at its last position.
- **Restore player view** restores the saved view and detaches the camera hook.

To change matches later: stop Follow, choose another Demo and click
**Switch Recording** (or **Start Replay**). You will be disconnected; rejoin Local Replay and
click Play again. Close/restart BC2 if its loading/disconnect screen does not recover.

## 8. Finish and restore normal online Rome

1. Close BC2.
2. Close the replay server and local backend processes. **Closing the controller
   does not stop them.** GUI-launched services may have no separate visible console;
   use Windows Task Manager if needed, taking care not to end unrelated processes.
3. In the **Redirect** group, click **Redirect Off**, approve UAC and wait for
   completion. Off is disabled if there is no preview block to remove.
   It removes only this application's block, not externally managed mappings.
4. Close the controller. With BC2 already closed, no camera restoration is needed.
   If restoration fails while BC2 is running, the controller asks whether to close
   anyway; restart BC2 to clear any remaining hook if you choose that option.
   Restart BC2 before attempting normal online Rome login.

If the GUI cannot open, run this from PowerShell in the extracted application folder:

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
