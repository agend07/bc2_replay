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

#include <windows.h>

/* r302: per-build addresses of the recorder / probe (retail and Steam BFBC2Game.exe; Steam values from r301, every
 * site holds the same original bytes in both builds, struct offsets are unchanged). Independent of bc2_exe.h, which
 * is a byte-identical copy shared with bc2-chat-smoke. */
typedef struct {
    const char *name;            /* "retail" / "steam" */
    DWORD timestamp, size_of_image;
    BYTE sha256[32];             /* whole exe file on disk (also written into the .bc2raw header) */
    int deferred;                /* 1 = .text is encrypted at DllMain (SteamStub): install from a thread later */
    DWORD site_connect, site_connect_ret, tgt_ctor;
    DWORD site_destroy, site_destroy_ret, tgt_getter;
    DWORD cave_site, cave_resume;
    DWORD site_dispatcher, site_wrapper_store, site_alt_connect;
    DWORD g_level, g_conn, g_ctx_ready;
    DWORD vt_connection, vt_mux_direct, vt_mux_fragment, vt_level;
    DWORD cave_lit[5];           /* absolute game dwords inside the cave code (client_recorder.c CAVE_LIT_OFF) */
} bc2_rec_profile;

/* Profile by name ("retail" / "steam"), NULL if unknown. */
const bc2_rec_profile *bc2_rec_profile_by_name(const char *name);

/* Identity only (exe base, PE stamp + SizeOfImage, file SHA-256): the profile of the running exe or NULL (why holds
 * the reason, >= 128 bytes). Valid in DllMain on both builds (headers and the file are not encrypted). */
const bc2_rec_profile *bc2_client_probe_identify(char *why);

/* In-memory bytes of every hook / read site of p: 1 = all equal, 0 = why holds the first mismatch. */
int bc2_client_probe_sites_ok(const bc2_rec_profile *p, char *why);

/* Polls bc2_client_probe_sites_ok every 2 ms (Steam: until the stub has decrypted .text). Returns the ms waited, or -1
 * after timeout_ms (why = the last mismatch). */
int bc2_client_probe_wait_sites(const bc2_rec_profile *p, DWORD timeout_ms, char *why);

/* Identity gate: 1 = this is a known BFBC2Game.exe and its sites hold the original bytes; 0 = why holds the reason. */
int bc2_client_probe_gate(char *why);

/* Called from bc2_mode_attach (client process, DllMain attach): starts the probe thread if enabled. */
void bc2_client_probe_attach(void);

/* Called from DllMain(DLL_PROCESS_DETACH): sets the stop flag, never waits. */
void bc2_client_probe_detach(void);

#endif
