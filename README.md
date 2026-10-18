# BC2 Local Replay & Recording

Tools for recording Battlefield: Bad Company 2 dedicated-server matches and
watching them through a local replay server and Project Rome client.
Recordings contain game state, not video; playback uses the actual game engine.

## Watch a recorded match

1. Download the standalone Windows ZIP from **[GitHub Releases](../../releases)**
   when a preview release is available, then extract it.
2. Follow the **[first-run guide](local-replay/USER_GUIDE.md)**: configure paths,
   run setup, select a Demo, enable the local redirect, start replay and launch BC2.
3. Use dummy credentials, join **Local Replay** and click **Play**.

The controller provides Play/Pause/Seek, free camera, Focus and experimental
Follow. Follow can be jittery; it is not native player possession.
The redirect sends the Rome login/server-list hostname to your local backend;
turn it off before returning to online Rome. See the guide for details.

You need your own supported BC2 client, dedicated server files and Project Rome.
No Python is needed for the standalone application. For building from source,
see **[local-replay/README.md](local-replay/README.md)**. GitHub Actions builds a
ZIP; version tags create a draft prerelease for review before publication.

## Dedicated server files

A user-provided third-party download is available here:
**[Host Server BC2-Vietnam 2025 (MediaFire)](https://www.mediafire.com/file/l06p1f5i90n1bjx/Host_Server_BC2-Vietnam_2025.rar/file)**.
This is not an official project download. We have not independently verified the
archive's contents, safety or redistribution permissions. Only download files
you trust and are entitled to use. Setup checks the expected server EXE hash;
that is a compatibility check, not a security audit of the whole archive.

## Record a match

See **[recording/README.md](recording/README.md)** for server requirements,
building the recorder patch, launching a recording server, saving a match and
checking its files. All recorder tools, launchers and example instance settings
are grouped in [`recording/`](recording/); the separate viewer is in
[`local-replay/`](local-replay/).

[`recordings_pt1.zip`](recording/recordings_pt1.zip) contains two sample matches. Extract their nine-file families
into the server's `demos/` folder. **Player names are retained; the recordings are
not anonymized.**

## Preview status and safety

- First-run startup and playback from a freshly extracted folder were confirmed
  on the development PC. Different-PC and disconnected-network validation remain
  pending; this is not a guarantee for every recording/build or camera case.
- Use **dummy login credentials only**, never your real online account details.
- Do not share generated `preview.json`, `runtime/`, private logs or hosts backups.
- Closing the controller does not stop backend/server processes. Follow the
  guide's shutdown steps and restore Redirect Off before normal online play.
- Game/server binaries and Project Rome DLLs are not distributed here. Bring
  your own files; unsupported executable versions are rejected.
