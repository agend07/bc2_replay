/* Client recorder (r248) for BFBC2Game.exe: embeds the live-proven recorder_v3 ghost cave (0xC299A7), a packet
 * observer installed on every new ClientConnection (call site 0x65642F) and a destroy hook (0x63D5EB), all
 * writing into one in-process ring that a writer thread drains into %LOCALAPPDATA%\BC2Replay\captures\*.bc2raw.
 * ON by default; off with BC2_REPLAY=1 or %LOCALAPPDATA%\BC2Replay\config.ini [dll] record=off.
 * The hooks only copy bytes the client already received; nothing in the game's own state changes except the
 * (unused, zero) packet-callback slot of the connection's stream control.
 */
#ifndef BC2_CLIENT_RECORDER_H
#define BC2_CLIENT_RECORDER_H

/* 1 unless BC2_REPLAY is exactly "1" or the ini has [dll] record=off. */
int bc2_client_recorder_enabled(void);

/* Called from bc2_mode_attach (client process, DllMain PROCESS_ATTACH). static_load = DllMain's lpReserved != NULL.
 * Gate + all-or-nothing install. Returns 1 when the hooks are installed (then the read-only probe must not run:
 * its site byte check would see the patched bytes), 0 when the recorder is off or refused (nothing written). */
int bc2_client_recorder_attach(int static_load);

/* Called from DllMain(DLL_PROCESS_DETACH): sets the writer's stop flag, never waits. */
void bc2_client_recorder_detach(void);

/* r255 chat commands. status: one line (installed/off, connection, file, paused, ring drops) into out.
 * pause(1) = the writer closes the current file and discards records until pause(0), which starts a new file
 * (an S record is re-emitted on the next packet). Returns 0 when the recorder is not installed. */
void bc2_client_recorder_status(char *out, int out_size);
int bc2_client_recorder_pause(int paused);

#endif
