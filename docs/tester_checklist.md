# BC2 Local Replay 0.1.0-preview.4 — tester checklist

This checklist walks a clean Windows account through the whole preview, one step at
a time: install, one recorded online round, building a demo from that round, playing
it back locally, and removing everything again. Each step lists what to do, what you
should see (`Expected:`), and what to record. The background for each step is in
[USER_GUIDE.md](../USER_GUIDE.md).

**What you need**

- Windows with your own retail `BFBC2Game.exe` and Project Rome's `dinput8.dll`
  already beside it (normal online Rome works).
- The dedicated server folder from the MediaFire archive (it contains
  `Frost.Game.Main_Win32_Final.exe`).
- The release files `bc2-local-replay-0.1.0-preview.4.zip` and
  `bc2-local-replay-0.1.0-preview.4.zip.sha256`. You do not need Python or the repository.
- A way to take screenshots (Win+Shift+S) and a text file for notes.

**Time:** about 60–90 minutes. One online round takes 20–30 minutes and a build
takes a few minutes.

**Folders used below.** `%LOCALAPPDATA%\BC2Replay\` holds `logs\` (the proxy's
`recorder_<pid>.log`, `chat_<pid>.log`, `dump_<pid>.log`), `captures\` (raw recordings,
`<yyyymmdd>_<hhmmss>_<pid>.bc2raw`) and `dumps\` (crash minidumps). Paste
`%LOCALAPPDATA%\BC2Replay` into the Explorer address bar to open it. `<pid>` is the
game's process id. The same `<pid>` links the three logs of one game session.

## Steps

1. **Verify the download.** Put the ZIP and its `.zip.sha256` in the same folder. Open
   PowerShell in that folder and run:

   ```powershell
   (Get-FileHash .\bc2-local-replay-0.1.0-preview.4.zip -Algorithm SHA256).Hash -eq (Get-Content .\bc2-local-replay-0.1.0-preview.4.zip.sha256).Split(' ')[0]
   ```

   It must print `True`. If it prints `False`, stop and report both hashes.
   - Record: a screenshot of the PowerShell output.

2. **Extract and start.** Extract the **whole** ZIP into a writable folder (not the
   game folder, not inside the ZIP). Open `bc2-local-replay-0.1.0-preview.4\BC2LocalReplay.exe`.
   SmartScreen may warn, because the EXE is unsigned. USER_GUIDE section 2 explains what to check first.
   - Expected: the window title ends in `— controls`, and the Setup section `Setup — paths, Prepare and Clean up` is open.
   - Expected: the summary reads `Setup needed: set both paths, then click Prepare.`
   - Record: the exact SmartScreen or antivirus wording, if any, and a screenshot of the window.

3. **Configure paths.** Under Setup, use **Browse…** to set **Server folder** (the
   folder containing `Frost.Game.Main_Win32_Final.exe`) and **BC2 client EXE** (the
   real `BFBC2Game.exe`, not a shortcut). Paths save automatically when valid.
   - Expected: no red or error text next to the paths, and the summary includes `Builder found.`
   - Record: a screenshot of the Setup section.

4. **Prepare (one UAC prompt).** Close the game and any dedicated server, then click **Prepare**.
   - Expected: a confirmation listing `Install the dinput8.dll proxy into` (game folder) with `(Windows will ask for administrator permission)`.
   - Expected: it also lists `Build the playback server executables in` and `Create the local preview instance under`, and ends with `Nothing else is changed. Continue?`
   - Click **Yes**, then answer the Windows UAC prompt once.
   - Expected: the status line runs through `Prepare: installing client proxy… (answer the Windows permission prompt)` … `Prepare: checking setup…`
   - If you see `Prepare stopped:` or `Prepare failed at`, write down the whole line. A `msimg32.dll present: remove it first.` stop means `msimg32.dll` (the standalone chat/smoke filter from github.com/agend07/bc2-chat-smoke, or the older bc2_mute) is in the game folder; remove it first (both swap the same vtables).
   - Record: a screenshot of the confirmation dialog, and the number of UAC prompts you answered (expected: 1).

5. **Check 6/6.** Read the status line under the Prepare button.
   - Expected: `Setup check: all` followed by "6 ok.", and the button now reads `Check`.
   - The **Setup check** report window opens only when a check fails. If it opens, a
     passing run would show lines such as `BC2 client EXE verified (` and `writable (captures, logs and crash dumps go there)`;
     record every `FAIL` line.
   - In the report window (if it opened), the client proxy line should read as follows.
   - Expected: `Client proxy installed (` … `Rome chain ok.`
   - Record: a screenshot of the status line (and of the report window if it opened).

6. **One ONLINE round with the proxy.** Close the controller. Start BC2 **the normal
   Rome way** (your usual shortcut or launcher), **not** with the controller's ▶.
   Games started by the controller do not record. Log in with your real Rome account as usual,
   join a normal online server and play at least one full round.
   - While in a round, type `/rec status` in chat and press Enter.
   - Expected: a chat line from `[dinput8]` that starts with `rec: ` and contains `drops`.
   - The full reply looks like `rec: recording, conn <hex>, file <path>.bc2raw, drops N, Q N`. It must say recording, not PAUSED, and must name a file.
   - Bad replies: `no file open`, `PAUSED`, or `rec: not installed (off, refused or replay session)`. Record them exactly.
   - Quit BC2 normally after the round.
   - Open the newest `recorder_<pid>.log` in the `logs` folder.
   - Expected: it contains `profile retail` (or `profile steam`), `installed (` and `writer thread running`, then `FILE open` and `FILE close`.
   - Expected: no line containing `GATE fail` or `install FAILED`.
   - Check that a new `.bc2raw` file appeared in `%LOCALAPPDATA%\BC2Replay\captures` (about 6.7 MB per minute).
   - Record: a screenshot of the `/rec status` reply, plus the `.bc2raw` file name, size and round length.

7. **Crash-dump check (no crash needed).** Open the `dump_<pid>.log` with the same
   `<pid>` as the recorder log from step 6. Its first line names the version 0.1.0-preview.4.
   - Expected: a line containing `filter installed prev=`.
   - Expected: a line containing `re-asserted over` with the value 0x00401570, ending in `(now the chain target)`.
   - If no `re-asserted over` line exists, or it shows a different value, report the whole file.
   - If the game crashed during any step, there is also a line with `CRASH code 0x` and a file
     `%LOCALAPPDATA%\BC2Replay\dumps\BFBC2Game_<pid>_<yyyymmdd_hhmmss>.dmp`. Please report
     that file's size in bytes. Dump sizes have barely been measured; one real dump was 25.7 MB.
     A line with `dump FAILED` means no dump was written. Record that line.
   - Record: the `re-asserted over` line (copy-paste), plus the `.dmp` size if one exists.

8. **Recordings window: automatic extract.** Open the controller again and click **Recordings…** (top right).
   - Expected: a window titled `Recordings (client captures)` with the note `Captures contain every player name and chat; conversion never emits chat.`
   - Expected: your step-6 file is listed with status `raw, not extracted` and its size. Selecting it shows `extracted automatically about a minute after recording stops`.
   - Do not click anything. The window extracts raw files by itself, one at a time, once the file has not changed
     for a minute, and refreshes the list every 5 seconds.
   - Expected: the status shows `Extracting`, then `Extracted in`. The raw row is replaced by one row per round
     (`· round K` is added to the name only when the file holds more than one round). Rounds shorter than 30 s are
     grey with status `too short`.
   - Record: a screenshot of the list before and after, and the extract time.

9. **▶ Play builds the round (trial build with gate).** Select the round you played (status `ready, not built yet`)
   and click **▶ Play**.
   - Expected: a confirmation that ends with `A build takes a few minutes.`, then `Building…`, with a progress bar and `STEP` lines in the log.
   - Expected: near the end the log shows `RESULT:` with a gate summary. The builder prints it as
     "gate N unseen on K ghosts, 0 structural" or "gate clean". A non-zero "structural" count is a failure.
   - The build log file (`builds/<demo>.build.txt` in the capture folder) names the decoder: "wire engine py"
     (the pure, non-native wire decoder, not Python). The only builder is `builder\builder.exe` (Go, no Python and
     no game EXE needed). To confirm which builder ran, look at the panel's Setup line: it names "Go builder" and the
     Go version, and builder\builder.json says "runtime": "go".
   - The builder rejects malformed input: a recording can end with `RESULT: FAIL` and an error line. If that
     happens, send the whole Recordings log box text (see Report a problem below).
   - Expected: a `DEMO` line, then `Done in` … `(new demo`. The Recordings window hides and the new demo starts in the
     main window by itself (step 10).
   - Record: the whole RESULT line (copy it from the log box), the build time, and the demo name.
     A refusal saying `Building next to a live test is not allowed` means the controller still runs
     a replay. Close and reopen the controller, then build again.
   - **Rebuild** builds the selected round again without playing it.

10. **▶ play on the local server.** Step 9 starts it by itself. Later, **▶ Play** on a round that already has a demo
    plays it at once (or use ▶ beside **Demo** under `Server Recording (demos/)` in the main window; no confirmation).
    - Expected: `Server started`, then `Server ready: launching BC2; it logs in and joins 'Local Replay' by itself.`
    - If BC2 asks for a login, use **dummy** credentials only.
    - Expected: after the map loads the round you played replays by itself (`playback starts once it is in the map`); SPACE pauses.
    - The newest `recorder_<pid>.log` (this replay game) should show that replays are not recorded.
    - Expected: `off (BC2_REPLAY=1 or ini record=off); nothing installed`
    - Try F (free camera) and the Left/Right arrow keys for a minute, then close the controller (it stops the game and server).
    - Record: a screenshot in game showing the VCR label, and any error text in the controller.

11. **Clean up….** In the **Setup** section (not the Recordings window's raw-file
    Clean up…), with the game and servers closed, click **Clean up…**.
    - Expected: a confirmation starting `Clean up removes what Prepare created` that says `Your game, server files and recordings stay.`
    - Answer the UAC prompt.
    - Expected: `Clean up: removing client proxy… (answer the Windows permission prompt)`, then `Clean up finished:` with the removed items listed.
    - Check the game folder: `dinput8.dll` is Rome's again and `dinput8_rome.dll` is gone.
    - Record: a screenshot of the final status line.

12. **Normal online play still works.** Start BC2 the normal Rome way and join an online server for a few minutes.
    - Expected: login, server list and joining behave exactly as before the test.
    - No new `recorder_<pid>.log` appears, because the proxy has been removed.
    - Optional: click **Prepare** again if you want to keep recording your online rounds.
    - Record: pass/fail, plus anything that behaves differently from before.

## Results

| Step | What | Pass / Fail | Notes (file names, times, sizes) |
|-----:|------|-------------|----------------------------------|
| 1 | ZIP hash matches `.zip.sha256` | | |
| 2 | Extract, start, SmartScreen | | |
| 3 | Paths saved, builder found | | |
| 4 | Prepare, one UAC prompt | | |
| 5 | Setup check 6/6, button reads Check | | |
| 6 | Online round, `/rec status` reply, `.bc2raw` written | | |
| 7 | `re-asserted over` 0x00401570; `.dmp` size if any | | |
| 8 | Automatic extract, round rows | | |
| 9 | ▶ Play builds, gate summary | | |
| 10 | ▶ local replay plays | | |
| 11 | Clean up… restores Rome | | |
| 12 | Normal online play after Clean up | | |

Also note your Windows version, your antivirus, and where the game and server folders are
(for example "Program Files" or "D: drive"). Do not give full paths that include your user name.

## Report a problem

Attach **only** these files, and only the ones from the session that failed (matching `<pid>`):

- `%LOCALAPPDATA%\BC2Replay\logs\recorder_<pid>.log`
- `%LOCALAPPDATA%\BC2Replay\logs\dump_<pid>.log`
- `%LOCALAPPDATA%\BC2Replay\logs\chat_<pid>.log`: only if a chat command misbehaved
  (it records commands, and the names of players whose chat was blocked).
- If a build (**▶ Play** or **Rebuild**) failed: the whole Recordings log box text (an error line there, e.g. a decode
  error or "not ported", usually means the recording contains a case the builder has not seen yet or
  malformed input the Go builder rejects). The `.bc2raw` itself only on request and privately (see Privacy below).
- Screenshots and copied text from the steps above (status line, Setup check window,
  Recordings log box, `/rec status` reply).
- A `.dmp` file **only on request**, and only if you accept that it contains game memory,
  including player names and chat.

**Never attach** the application's `runtime\` folder (private logs, generated admin
credentials, local paths), `preview.json`, or any file not listed above.

**Privacy:** captures (`.bc2raw` files in `captures\`, extracted capture folders) and the
demos built from them contain every player's name and the chat of the round you played.
Do not share them publicly. Send them only if asked, and only privately.
