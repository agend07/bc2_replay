# dinput8.dll proxy: auto-join, no hosts entry, recorder, chat, dumps

Sections:

1. Auto-login and auto-join (web-launch path)
2. No hosts entry (dinput8.dll proxy), build and install
3. Read-only client probe
4. Client recorder
5. In-game chat commands
6. Crash minidump


Two additions let the BC2 client that **▶** in the Local Replay panel starts go
straight into the map on the local replay server, with no menu clicks and no `hosts` edit:

1. **Auto-login and auto-join**: `replay/web_launch.py`, used by `preview.py client`.
2. **No hosts entry**: the `dinput8.dll` proxy in this folder. `web_launch.local_env()` enables it for the
   client (`preview.py client`) and for the replay server (`replay/timeline.py`, `preview.py server`).

Both target the retail client `BFBC2Game.exe` (SHA-256 `3911fcc8…3d54258`) with
Project Rome's `dinput8.dll`. The addresses below refer to that exe. The proxy also carries Steam
`BFBC2Game.exe` profiles (SHA-256 `22e96001…`) for the chat commands and the recorder (see section 5).

## 1. Auto-login and auto-join (web-launch path)

The retail client still contains EA's "launch from browser" mode. Three command-line
options drive it:

```
-loginToken <any> -sessionId <any>
-requestState State_ConnectToGameId
-requestStateParams "<data personaref=\"LocalViewer\" clantag=\"\" gameid=\"1103806753150\"/>"
```

- The params are the **attributes** of `<data>`, not child elements (parser `0x5a97a0`).
- `personaref` and `clantag` are required. Without `personaref` the client raises
  `ERR_PARAM_MISSINGPERSONA` (`0x5b1030`).
- `gameid` is 64-bit: `(LID << 32) | GID`, and both halves must be nonzero (`0x5b3fa3`).
  For the local backend that is `(257 << 32) | 158078`. Values come from `backend/rome_local.py`.
- The local backend accepts any token. The login uses the backend's dummy account.

The client acts on these options only after something connects to the named pipe it
creates, `\\.\pipe\rome_snowroller`. EA's browser plugin used to be on the other end.
`web_launch.serve_pipe()` takes its place: it connects and then only reads, until
the client exits.

Pipe frames (dispatcher `0x5a95f0`) are
`'K' 'E' <total_len> <name_len> <name> <data_len> <data>`, each length one byte.
A successful launch logs the following (in `runtime/launcher_*.private.log`):

```
StateChanged State_Starting State_ConnectToGameId 0
StateChanged State_NotLoggedIn State_ConnectToGameId 0
StateChanged State_Connecting State_Game 0
StateChanged State_GameLoading State_Game 1000001
GameWaiting
StateChanged State_Game State_NA 1000001
```

`Alert ERR_PARAM_*` lines name the parameter that was rejected.

Normal `-Network.Plasma*` / `-Client.ServerIp` settings on the command line are
ignored by the retail client.

## 2. No hosts entry (dinput8.dll proxy)

Project Rome's `dinput8.dll` hooks `gethostbyname` and sends EA hostnames to
`bfbc2.emulatornexus.com`. That name is hard-coded and has no config option. Pointing
it at the local backend used to need a `hosts` entry `127.0.0.1 bfbc2.emulatornexus.com`;
the proxy's `BC2_ROME_HOST` redirect below replaces any such entry, and no `hosts` edit is used.

The proxy (`dinput8_proxy.c`) sits in front of Rome:

- Rome is renamed `dinput8_rome.dll`. The proxy loads it and forwards `DirectInput8Create`.
- **Without** `BC2_ROME_HOST` in the environment, the proxy does nothing else, so
  normal Rome online play is unchanged.
- **With** `BC2_ROME_HOST=127.0.0.1`, which the panel sets for the replay server
  and the client, the proxy rewrites `bfbc2.emulatornexus.com` in Rome's loaded image
  to that host. This covers the master host and the version-check URL. The result
  is written to the debug output (DebugView): `dinput8 proxy: Rome host -> …`.

The value must be at most 23 characters, the length of the original name.

### Build and install

```
rome_proxy\build_shared.cmd     # VS C++ x86 tools; output rome_proxy\out_shared\dinput8.dll
```

**Normal use:** the panel's **Prepare** installs (and upgrades) the shared proxy into BOTH the game folder and the
dedicated server folder, and **Clean up…** removes it again (Rome's `dinput8.dll` restored). The client folder needs a
UAC prompt (a small elevated helper script does the file operations; the panel itself is not elevated); the server
folder normally does not. Close the game and the server first. The panel and the release zip use the shared build
`out_shared\dinput8.dll` (`proxy\dinput8.dll` in the zip).

The build embeds the release version (`version.h`, `BC2_VERSION_STRING`). It shows in the panel's proxy status
(`build 0.1.0-preview.5`) and as the first line of each `*_<pid>.log` under `%LOCALAPPDATA%\BC2Replay\logs\`. The
panel's checks fail on a version mismatch between the installed proxy and the panel; **Prepare** from the extracted
zip upgrades it.

## Troubleshooting

- The client stays in the menu: open the launcher log. No `Launch pipe connected`
  means the pipe never opened. An `Alert …` line names the rejected parameter.
- The client or server cannot reach the backend with the proxy installed: check
  DebugView for the proxy line. If the string count is 0 or the line is missing,
  Rome's build differs.

## 3. Read-only client probe (r247)

`client_probe.c` (shared build only) adds an opt-in diagnostic thread to `BFBC2Game.exe`. It is
**OFF by default**: nothing starts unless `BC2_PROBE=1` is set in the launching process
(as the panel does with `BC2_ROME_HOST`) or `%LOCALAPPDATA%\BC2Replay\config.ini` contains

```
[dll]
probe=1
```

(for a client started from the Rome launcher). When enabled, a thread first checks the exe
identity (base `0x400000`, PE timestamp/SizeOfImage, SHA-256 of the exe file, bytes of the
future hook sites), then polls every 10 ms (1 ms stuttered the game in the r247 runs) and logs connection, dispatcher, packet, ghost and
level changes plus a `STAT` line every 5 s. It only reads game memory (every read under
`__try`); it never writes to the game or changes page protection.

Log: `%LOCALAPPDATA%\BC2Replay\logs\probe_<pid>.log` (never next to the exe) and DebugView
(`dinput8 proxy: probe ...`). First line is `GATE ok` or `GATE fail <why>`; on fail the
thread exits. Offline check: `out_shared\client_probe_selftest.exe`.

## 4. Client recorder (r248, shared build)

`client_recorder.c` (+ `bc2_ring.c`, `bc2_capture.c`, generated `client_cave.h`) records the network stream the
client receives, in `BFBC2Game.exe` only. It is **on by default** (`record=auto`: every online session the proxy
sees is captured) and off when the launching process sets `BC2_REPLAY=1` or `%LOCALAPPDATA%\BC2Replay\config.ini`
has

```
[dll]
record=off
```

The BC2 client that the panel's **▶** starts (`preview.py client`, env from `replay.web_launch.client_env()`) sets `BC2_REPLAY=1`,
so replay sessions record nothing; the log then reads `off (BC2_REPLAY=1 or ini record=off); nothing installed`.
A client started from the desktop records as usual. No `record=` key in the ini means auto.

It installs only on the static (import-time) load of `dinput8.dll`, before the exe entry point runs, after the same
identity gate as the probe (PE timestamp/SizeOfImage, exe SHA-256, bytes of every hook site). Then it allocates a
4 MB record ring and a 4 KB code page, relocates the live-proven ghost cave (`client_cave.h`, generated by a
maintainer-private tool, not in this repository; 37 ring + 1 code fixups) and rewrites three
`.text` sites, all or nothing:

| VA | old bytes | new bytes | what |
|---|---|---|---|
| `0xC299A7` | `8b 97 38 02 00 00` | `e9 <rel32> 90` | jmp to the ghost cave (G/M/C/P records, unchanged logic) |
| `0x65642F` | `e8 6c 21 f0 ff` (call ClientConnection ctor) | `e9 <rel32>` | jmp to the connect stub |
| `0x63D5EB` | `e8 70 1a 1c 00` (call context getter) | `e9 <rel32>` | jmp to the destroy stub |

The ctor is `__thiscall` with 5 stack args and `ret 0x14`, so both hooks are **jmp**s whose stubs call the original
target and come back with `push <site+5>; ret` (a `call stub` that itself called the ctor would shift every argument
by one slot). The connect stub stores a static callback object into the new connection's dormant packet-callback
slot (`conn+0x1BC`, zero on this build); the observer (`__thiscall`, `ret 4`) copies every received packet into a Q
record (same validation as the cave's P record, so Q and P carry the same bytes on the direct mux) and emits an S
record (level name, dispatcher vtable, ghost id width) on connect and whenever those change. The destroy stub writes
E after the connection is freed and never touches it. A writer thread drains the ring every 50 ms into
`%LOCALAPPDATA%\BC2Replay\captures\<utc>_<pid>.bc2raw` (128 B header: magic `BC2RAW`, format 1, exe sha, dll
version, session GUID, FILETIME, InterruptTime, ring size; then the ring records verbatim), one file per
connection (closed at E), `FlushFileBuffers` every 2 s, a T sync record every second while connected. Log:
`%LOCALAPPDATA%\BC2Replay\logs\recorder_<pid>.log`.

The probe (section 3) and the recorder are mutually exclusive: with the recorder installed the probe's site byte
check fails, so `bc2_mode_attach` skips the probe whenever the recorder reports installed.

Offline checks: `out_shared\bc2_ring_selftest.exe out_shared` (ring protocol vs the Python reference, capture file,
leaves `selftest.bc2raw` for a maintainer-private reader test, not in this repository) and
`out_shared\client_recorder_selftest.exe out_shared` (cave relocation SHA, stub stack discipline against a fake
ctor/getter, observer `ret 4` + Q/S/E records, one writer pass). `builder.exe extract` reads the files.

To remove it, use **Clean up…** in the panel, which restores Rome's `dinput8.dll`. The panel's **Prepare** stops (changing nothing) while
`msimg32.dll` is present in the game folder (see the install note in section 5).

## 5. In-game chat commands (r255, shared build)

`client_chat.c` ports the bc2_mute `msimg32.dll` chat filter into this proxy: two of its instance-vtable clone
hooks (UI event dispatcher `0x009640D0`, chat input submit `0x00751380`; the HUD subobject hook `0x007875B0` is not
installed, it never sees chat code 0x1A, r254), no
`.text` writes (except the opt-in `/smoke per` call patch below), so the recorder's site gate is unaffected. A 250 ms monitor thread re-attaches when the UI recreates
the objects. **On by default** in `BFBC2Game.exe`; off with `[dll]` `chat=off` in `%LOCALAPPDATA%\BC2Replay\config.ini`.

| command | effect (reply shown only to the local player, never sent) |
|---|---|
| `/mute <name or word>` / `/unmute <...>` / `/mutelist` | mute list `%LOCALAPPDATA%\BC2Replay\muted_players.txt` (case-insensitive substring on sender and first text segment; hot-reloaded every 2 s) |
| `/hash <name>` / `/unhash <name>` / `/hashlist` | a hashed sender's messages are rewritten in place, same length: letters/digits -> `#$%&@!*?` (by byte + position), spaces/punctuation/UTF-8 kept; list `hashed_players.txt` |
| `/rec` or `/rec status` | recorder state: installed / paused, connection, current `.bc2raw`, ring drops |
| `/rec off` / `/rec on` | pause = the writer closes the file and discards records; on = new file, S record re-emitted |
| `/smoke <color>` / `/smoke #rrggbb` | recolor ALL smoke-grenade smoke (vapor cloud, trail, projectile trail; launcher + hand grenade): patches the loaded `PolynomialColorInterpData` Color0/Color1 (r252/r253) on the monitor thread, re-applied after every level load (EmitterManager `[0x01575A50]` change, scans at +4 s and +20 s, then every 60 s) and persisted in the ini `[smoke] color=`; names: red green blue yellow cyan magenta orange purple pink white black lime teal gold grey, Halloween presets pumpkin ghost blood; `/smoke duo <young> <old>` = two fixed colours (fresh -> old particles, r297); `/smoke halloween` = `duo pumpkin blood` |
| `/smoke rainbow [s]` / `/smoke psycho [s]` / `/smoke pulse <color> [s]` | animation (r255d): the monitor thread rewrites the known colour objects 20x/s; rainbow = hue cycle (default 6 s), psycho = young/old particles get opposite rotating hues, each emitter 120 deg apart (8 s), pulse = brightness sine (2 s); persisted (`mode=`, `period=`) |
| `/smoke per [N]` | per-grenade colours (r259): every smoke grenade cloud gets its own stable colour from an N-hue palette (2..12, default 8, idx 0 = red); the shared colour objects keep their greys and a per-instance hook swaps the render-time eval block's colour pointer for a DLL-owned clone (key = effect root above the emitter entity, so all emitters of one detonation share a colour; the flight trail is a separate effect and may get a different colour). The one `.text` write of the chat module: the `call` at `0xB29362` (eval-block compiler), installed by the monitor thread when the mode is activated, never removed (`/smoke off` only disables it); the recorder's gate sites are elsewhere. Persisted `mode=per`, `per=N`; status shows `hook ok c<calls> r<replaced>` or `hook refused: <why>` |
| `/smoke alpha|ambient|vpl|normal <0..1|orig>` | template tunables (r255c): AlphaAsColorBlendFactor (0.615 -> 0 by default while a colour is active; the grey-core suspect), AmbientLightBlendFactor, VertexPixelLightingBlendFactor, GlobalLocalNormalBlendFactor of the smoke EmitterTemplateData objects |
| `/smoke off` / `/smoke status` | restore the original greys / counts of found + patched objects, scans, manager pointer |
| `/fx list [group [page]]` / `/fx <name> <color>` / `/fx <name> off` / `/fx off` / `/fx status` | recolour other particle effects of the level by name (muzzle flashes, tracers, impacts, blood, fire, explosions; `client_fx.c`, ported from bc2-chat-smoke 0.2.0): the same emitter scan as `/smoke`, rules persisted in the ini `[fx]` as `<name>=<color>`, `off` restores the originals; long replies span several chat lines |
| `/help` | the list above |

Unknown `/text` is sent to the server as typed. Only chat code 0x1A is touched (r254: codes 0x11/0x12 are different
objects whose +0x6C is not a name; bc2_mute read them as one). Log: `%LOCALAPPDATA%\BC2Replay\logs\chat_<pid>.log`.
The `/smoke` code is in `client_smoke.c` and `client_smoke_hook.c` (`/smoke per`); `client_chat.c` only dispatches
the command. `/smoke` is the first live test of the r252/r253 hypothesis that Color0/Color1 tint the smoke; a scan takes ~1-2 s of
the monitor thread (not a game thread) and matches only the exact asset greys + vtable `0x0145BE04` + evaluator type 0xB.

**Install note:** remove `msimg32.dll` (the standalone chat/smoke filter from github.com/agend07/bc2-chat-smoke, or
the older bc2_mute) from the game folder before using this build, or both DLLs swap the same vtables; copy its `muted_players.txt` to `%LOCALAPPDATA%\BC2Replay\` to keep the list. The panel's **Prepare** stops (changing nothing) while `msimg32.dll` is present.

Offline check: `out_shared\client_chat_selftest.exe out_shared` (command parser, mute/hash file round trips, hash
transform, `/rec` without a recorder, smoke color parsing / mapping / object matcher / animation, `/smoke per` parser + per-instance clone callback, `/fx`; 117 checks). Game addresses come from per-exe profiles (`bc2_exe.h`: retail 3911fcc8..., Steam 22e96001...), so chat, `/smoke` and `/fx` also run on the Steam exe; the recorder and probe have their own two-build profile table (`client_probe.c` PROFILES; PE stamp + SHA-256 gate, any other exe is refused): on Steam the recorder installs from a thread once SteamStub has decrypted `.text`, and the capture header carries the recording exe's SHA-256 (Steam recording is not yet live-tested); the crash dump filter uses no game address.

To remove the proxy, use **Clean up…** in the panel (Rome's `dinput8.dll` is restored).

## 6. Crash minidump (r263, shared build)

`client_dump.c` installs a chained top-level exception filter in `BFBC2Game.exe`. On an unhandled exception it writes
`%LOCALAPPDATA%\BC2Replay\dumps\<exe>_<pid>_<yyyymmdd_hhmmss>.dmp` with dbghelp `MiniDumpWriteDump` (loaded from the
system directory only inside the filter; `MiniDumpWithIndirectlyReferencedMemory | MiniDumpWithDataSegs`), logs one line
to `%LOCALAPPDATA%\BC2Replay\logs\dump_<pid>.log`, `CRASH code 0x... addr 0x... tid ... dump <path> <bytes> B` (or
`dump FAILED ...`; a second dump of the same process in the same second gets a `_<n>` suffix), and then calls the previous filter, or returns `EXCEPTION_CONTINUE_SEARCH` when there is none.
**On by default**; off with `[dll]` `dumps=0` in `%LOCALAPPDATA%\BC2Replay\config.ini`.

Why a re-assert thread: the CRT's `__CxxUnhandledExceptionFilter` (`0xC528CC`) is installed at CRT init, and then the
game's `main` installs its own top-level filter from call site `0x401E4A` (filter function `0x401570`: formats its crash
text and returns `EXCEPTION_EXECUTE_HANDLER` without chaining). Both happen after `DllMain`, so a filter installed at
attach is silently replaced. A 1 s thread puts ours back on top and keeps the replaced filter as the chain target, so
the game's own crash handling (crash text, optional own minidump gated by `[0x155D0A2]`, then `0x536120` = exit(1), probable)
still runs after the dump is written.

Retention: the newest 5 dumps are kept; older ones are deleted at attach, never inside the filter. **Privacy:** a dump
holds game-process memory (player names, chat text); look at it before you share it. Limitation: a stack overflow gets
no dump (the filter runs on the faulting thread's stack).

Offline check: `out_shared\client_dump_selftest.exe out_shared` (prune, in-process dump, chain to the previous filter,
re-assert, end-to-end child crash with exit code 0xC0000005; writes only under `out_shared\dump_selftest\`; fails under a
debugger, which takes the child's exception before any top-level filter).
