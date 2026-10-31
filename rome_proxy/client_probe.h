/* Read-only client probe (r247) for BFBC2Game.exe: a polling thread that logs
 * connection / dispatcher / packet / ghost / level state changes. OFF by default;
 * enabled by BC2_PROBE=1 or %LOCALAPPDATA%\BC2Replay\config.ini [dll] probe=1.
 * Never writes to game memory.
 */
#ifndef BC2_CLIENT_PROBE_H
#define BC2_CLIENT_PROBE_H

/* 1 if BC2_PROBE is exactly "1" or the ini has [dll] probe=1, else 0. */
int bc2_client_probe_enabled(void);

/* Build %LOCALAPPDATA%\BC2Replay\logs\probe_<pid>.log; create_dirs creates the folders.
 * Returns 0 if LOCALAPPDATA is missing or the path does not fit. */
int bc2_client_probe_log_path(char *out, int out_size, int create_dirs);

/* Identity gate: 1 = this is the known BFBC2Game.exe; 0 = why holds the reason (>= 128 bytes). */
int bc2_client_probe_gate(char *why);

/* Called from bc2_mode_attach (client process, DllMain attach): starts the probe thread if enabled. */
void bc2_client_probe_attach(void);

/* Called from DllMain(DLL_PROCESS_DETACH): sets the stop flag, never waits. */
void bc2_client_probe_detach(void);

#endif
