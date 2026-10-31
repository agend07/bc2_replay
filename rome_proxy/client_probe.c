/* Read-only client probe (r247), see client_probe.h and README section 4.
 * Every game-memory read is under __try; nothing is written to the game.
 */
#include <windows.h>
#include <bcrypt.h>
#include <string.h>
#include "client_probe.h"
#include "version.h"

/* ---- identity of the retail BFBC2Game.exe (SHA-256 3911fcc8...3d54258) ---- */
#define EXE_BASE        0x00400000u
#define EXE_TIMESTAMP   0x4E55445Du
#define EXE_SIZEOFIMAGE 0x01913000u

static const BYTE EXE_SHA256[32] = {
    0x39, 0x11, 0xfc, 0xc8, 0x91, 0x4b, 0x01, 0x58, 0xc4, 0x34, 0xce, 0xe3, 0x6f, 0x19, 0x29, 0x53,
    0x16, 0xe7, 0x15, 0xd4, 0xc9, 0xa0, 0xe7, 0x0c, 0x85, 0x7e, 0x8c, 0x39, 0xe3, 0xd5, 0x42, 0x58
};

/* In-memory bytes of every future hook / read site; all must match or the probe exits. */
static const struct {
    DWORD va;
    DWORD len;
    BYTE bytes[8];
    const char *name;
} SITES[] = {
    { 0x0065642F, 5, { 0xe8, 0x6c, 0x21, 0xf0, 0xff }, "connect: call ClientConnection_Ctor 0x5585A0" },
    { 0x0063D5EB, 5, { 0xe8, 0x70, 0x1a, 0x1c, 0x00 }, "DestroyConnection: call g_ClientContext getter 0x7FF060" },
    { 0x00BEDF59, 7, { 0x83, 0xbe, 0x9c, 0x01, 0x00, 0x00, 0x00 }, "dispatcher: cmp [esi+0x19C], 0 (packet callback)" },
    { 0x00C299A7, 6, { 0x8b, 0x97, 0x38, 0x02, 0x00, 0x00 }, "ghost manager: mov edx, [edi+0x238]" },
    { 0x00505C1C, 3, { 0x89, 0x46, 0x04 }, "control BitStream wrapper store: mov [esi+4], eax" },
    { 0x007FF060, 5, { 0xb8, 0x01, 0x00, 0x00, 0x00 }, "g_ClientContext getter entry: mov eax, 1" },
    { 0x00656446, 5, { 0xe8, 0x15, 0x8c, 0x1a, 0x00 }, "connect: call g_ClientContext getter (alt site)" },
};

/* ---- game globals, offsets and vtables read by the poll loop ---- */
#define G_LEVEL          0x01571410u /* current level object */
#define G_CONN           0x01571430u /* g_ClientContext+0x58 = ClientConnection */
#define G_CTX_READY      0x01571434u /* bit 0 = context ready */
#define VT_CONNECTION    0x014182A8u
#define VT_MUX_DIRECT    0x0147D218u /* ConnectionStreamMux */
#define VT_MUX_FRAGMENT  0x0147D04Cu /* ConnectionFragmentStream */
#define VT_LEVEL         0x01432090u
#define OFF_DISPATCHER   0x2Cu       /* control+0xC */
#define OFF_PKT_STATE    0x34u       /* control BitStream wrapper +4 */
#define OFF_PKT_BITS     0x08u       /* [state+8] = bit count */
#define OFF_CB_SLOT      0x1BCu      /* control+0x19C packet callback */
#define OFF_GHOST_HEAD   (0x12CCu + 0x44u)
#define OFF_GHOST_BITS   (0x12CCu + 0x244u)
#define OFF_LEVEL_NAME_B 0x28u
#define OFF_LEVEL_NAME_E 0x2Cu
#define STAT_PERIOD_MS   5000u

static volatile LONG g_stop;
static HANDLE g_thread;
static char g_log_path[MAX_PATH];
static DWORD g_t0;

/* Read 4 bytes of (game) memory; 0 on access fault. */
static int rd32(DWORD addr, DWORD *out)
{
    __try {
        *out = *(const volatile DWORD *)(UINT_PTR)addr;
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

static int rdbuf(DWORD addr, void *dst, DWORD len)
{
    __try {
        memcpy(dst, (const void *)(UINT_PTR)addr, len);
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

/* 1 = equal, 0 = differ, -1 = fault */
static int cmpmem(DWORD addr, const BYTE *bytes, DWORD len)
{
    __try {
        return memcmp((const void *)(UINT_PTR)addr, bytes, len) == 0;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return -1;
    }
}

static int local_appdata(char *out, DWORD size)
{
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", out, size);
    return n > 0 && n < size;
}

int bc2_client_probe_enabled(void)
{
    char flag[8], ini[MAX_PATH];
    DWORD n = GetEnvironmentVariableA("BC2_PROBE", flag, sizeof(flag));
    if (n == 1 && flag[0] == '1')
        return 1;
    if (!local_appdata(ini, MAX_PATH - 32))
        return 0;
    lstrcatA(ini, "\\BC2Replay\\config.ini");
    return GetPrivateProfileIntA("dll", "probe", 0, ini) == 1;
}

int bc2_client_probe_log_path(char *out, int out_size, int create_dirs)
{
    char dir[MAX_PATH], name[48];
    if (!local_appdata(dir, MAX_PATH - 64))
        return 0;
    lstrcatA(dir, "\\BC2Replay");
    if (create_dirs)
        CreateDirectoryA(dir, NULL);
    lstrcatA(dir, "\\logs");
    if (create_dirs)
        CreateDirectoryA(dir, NULL);
    wsprintfA(name, "\\probe_%lu.log", GetCurrentProcessId());
    if (lstrlenA(dir) + lstrlenA(name) >= out_size)
        return 0;
    lstrcpyA(out, dir);
    lstrcatA(out, name);
    return 1;
}

static void probe_log(const char *text)
{
    char line[1100];
    DWORD ms = GetTickCount() - g_t0, written;
    HANDLE f;
    wsprintfA(line, "dinput8 proxy: probe +%lu %s\r\n", ms, text);
    OutputDebugStringA(line);
    if (!g_log_path[0])
        return;
    wsprintfA(line, "+%lu %s\r\n", ms, text);
    f = CreateFileA(g_log_path, FILE_APPEND_DATA, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL,
                    OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (f == INVALID_HANDLE_VALUE)
        return;
    WriteFile(f, line, lstrlenA(line), &written, NULL);
    CloseHandle(f);
}

/* SHA-256 of a whole file; 0 on any failure. */
static int sha256_file(const char *path, BYTE out[32])
{
    HANDLE f = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, 0, NULL);
    LARGE_INTEGER size;
    BYTE *buf = NULL;
    DWORD got = 0;
    BCRYPT_ALG_HANDLE alg = NULL;
    int ok;
    if (f == INVALID_HANDLE_VALUE)
        return 0;
    ok = GetFileSizeEx(f, &size) && size.QuadPart > 0 && size.QuadPart < 0x10000000;
    if (ok)
        buf = (BYTE *)VirtualAlloc(NULL, (SIZE_T)size.QuadPart, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    ok = ok && buf && ReadFile(f, buf, (DWORD)size.QuadPart, &got, NULL) && got == (DWORD)size.QuadPart;
    CloseHandle(f);
    if (ok && BCryptOpenAlgorithmProvider(&alg, BCRYPT_SHA256_ALGORITHM, NULL, 0) >= 0) {
        ok = BCryptHash(alg, NULL, 0, buf, got, out, 32) >= 0;
        BCryptCloseAlgorithmProvider(alg, 0);
    } else {
        ok = 0;
    }
    if (buf)
        VirtualFree(buf, 0, MEM_RELEASE);
    return ok;
}

int bc2_client_probe_gate(char *why)
{
    DWORD base = (DWORD)(UINT_PTR)GetModuleHandleA(NULL), lfanew = 0;
    IMAGE_NT_HEADERS32 nt;
    char exe[MAX_PATH];
    BYTE hash[32];
    DWORD n;
    int i, rc;

    if (base != EXE_BASE) {
        wsprintfA(why, "exe base mismatch (%08lx)", (unsigned long)base);
        return 0;
    }
    if (!rd32(base + 0x3C, &lfanew) || lfanew > 0x1000 || !rdbuf(base + lfanew, &nt, sizeof(nt))
        || nt.Signature != IMAGE_NT_SIGNATURE) {
        wsprintfA(why, "no PE header");
        return 0;
    }
    if (nt.FileHeader.TimeDateStamp != EXE_TIMESTAMP || nt.OptionalHeader.SizeOfImage != EXE_SIZEOFIMAGE) {
        wsprintfA(why, "PE mismatch (timestamp %08lx, image size %08lx)",
                  (unsigned long)nt.FileHeader.TimeDateStamp, (unsigned long)nt.OptionalHeader.SizeOfImage);
        return 0;
    }
    n = GetModuleFileNameA(NULL, exe, sizeof(exe));
    if (n == 0 || n >= sizeof(exe)) {
        wsprintfA(why, "cannot query exe path");
        return 0;
    }
    if (!sha256_file(exe, hash)) {
        wsprintfA(why, "cannot hash exe file");
        return 0;
    }
    if (memcmp(hash, EXE_SHA256, 32) != 0) {
        wsprintfA(why, "exe SHA-256 mismatch (%02x%02x%02x%02x...)", hash[0], hash[1], hash[2], hash[3]);
        return 0;
    }
    for (i = 0; i < (int)(sizeof(SITES) / sizeof(SITES[0])); i++) {
        rc = cmpmem(SITES[i].va, SITES[i].bytes, SITES[i].len);
        if (rc != 1) {
            wsprintfA(why, "site %08lx %s (%s)", (unsigned long)SITES[i].va,
                      rc < 0 ? "unreadable" : "bytes differ", SITES[i].name);
            return 0;
        }
    }
    return 1;
}

/* LEVEL <ptr> vt <vt> "<name>"; name only for the known level vtable and a sane range. */
static void level_text(DWORD level, DWORD vt, char *out)
{
    DWORD b = 0, e = 0, i, len;
    char name[256];
    if (!level) {
        lstrcpyA(out, "LEVEL 0");
        return;
    }
    name[0] = 0;
    if (vt == VT_LEVEL && rd32(level + OFF_LEVEL_NAME_B, &b) && rd32(level + OFF_LEVEL_NAME_E, &e)
        && b && e > b && e - b < 256) {
        len = e - b;
        if (rdbuf(b, name, len)) {
            for (i = 0; i < len; i++)
                if (name[i] < 0x20 || name[i] > 0x7E)
                    name[i] = '?';
            name[len] = 0;
        } else {
            name[0] = 0;
        }
    }
    wsprintfA(out, "LEVEL %08lx vt %08lx \"%s\"", (unsigned long)level, (unsigned long)vt, name);
}

typedef struct {
    DWORD conn;
    int slot_seen, bits_seen, pkt_seen, ghost_seen;
    DWORD slot, bits, disp, disp_vt;
    DWORD stat_state, stat_bits;
} conn_state;

static void conn_reset(conn_state *c, DWORD conn)
{
    memset(c, 0, sizeof(*c));
    c->conn = conn;
}

static const char *disp_kind(DWORD vt)
{
    return vt == VT_MUX_DIRECT ? "direct" : vt == VT_MUX_FRAGMENT ? "fragment" : "unknown";
}

static DWORD WINAPI probe_thread(LPVOID arg)
{
    char why[256], line[512], last_level[512];
    conn_state c;
    DWORD v, v2, ctx_prev = 0xFFFFFFFF, stat_t, now;
    DWORD level = 0, level_vt = 0;
    unsigned long pkt_samples = 0, bits_changes = 0;
    (void)arg;

    g_t0 = GetTickCount();
    if (!bc2_client_probe_log_path(g_log_path, sizeof(g_log_path), 1))
        g_log_path[0] = 0;
    probe_log("version " BC2_VERSION_STRING);  /* r266: first line of every probe_<pid>.log */
    if (!bc2_client_probe_gate(why)) {
        wsprintfA(line, "GATE fail %s", why);
        probe_log(line);
        return 0;
    }
    probe_log("GATE ok");

    conn_reset(&c, 0);
    last_level[0] = 0;
    stat_t = GetTickCount();
    while (!g_stop) {
        if (rd32(G_CTX_READY, &v) && (v & 1) != ctx_prev) {
            ctx_prev = v & 1;
            wsprintfA(line, "CTX %lu", (unsigned long)ctx_prev);
            probe_log(line);
        }

        if (rd32(G_LEVEL, &level)) {
            if (!level || !rd32(level, &level_vt))
                level_vt = 0;
            level_text(level, level_vt, line);
            if (lstrcmpA(line, last_level) != 0) {
                lstrcpyA(last_level, line);
                probe_log(line);
            }
        }

        if (rd32(G_CONN, &v) && v != c.conn) {
            if (c.conn) {
                wsprintfA(line, "CONN gone %08lx", (unsigned long)c.conn);
                probe_log(line);
            }
            conn_reset(&c, v);
            if (v) {
                if (!rd32(v, &v2))
                    v2 = 0;
                wsprintfA(line, "CONN new %08lx vt %08lx", (unsigned long)v, (unsigned long)v2);
                probe_log(line);
                if (v2 != VT_CONNECTION) {
                    wsprintfA(line, "CONN vt-mismatch %08lx vt %08lx expected %08lx", (unsigned long)v,
                              (unsigned long)v2, (unsigned long)VT_CONNECTION);
                    probe_log(line);
                }
            }
        }

        if (c.conn) {
            if (rd32(c.conn + OFF_CB_SLOT, &v) && (!c.slot_seen || v != c.slot)) {
                c.slot_seen = 1;
                c.slot = v;
                wsprintfA(line, "SLOT %08lx", (unsigned long)v);
                probe_log(line);
            }
            if (rd32(c.conn + OFF_DISPATCHER, &v)) {
                if (!v || !rd32(v, &v2))
                    v2 = 0;
                if (v != c.disp || v2 != c.disp_vt) {
                    c.disp = v;
                    c.disp_vt = v2;
                    wsprintfA(line, "DISP %08lx vt %08lx %s", (unsigned long)v, (unsigned long)v2, disp_kind(v2));
                    probe_log(line);
                }
            }
            if (rd32(c.conn + OFF_PKT_STATE, &v)) {
                if (!v || !rd32(v + OFF_PKT_BITS, &v2))
                    v2 = 0;
                if (v && !c.pkt_seen) {
                    c.pkt_seen = 1;
                    wsprintfA(line, "PKT first %08lx bits %lu", (unsigned long)v, (unsigned long)v2);
                    probe_log(line);
                }
                if (v != c.stat_state || v2 != c.stat_bits)
                    pkt_samples++;
                if (v2 != c.stat_bits)
                    bits_changes++;
                c.stat_state = v;
                c.stat_bits = v2;
            }
            if (!c.ghost_seen && rd32(c.conn + OFF_GHOST_HEAD, &v) && v) {
                c.ghost_seen = 1;
                wsprintfA(line, "GHOST first %08lx", (unsigned long)v);
                probe_log(line);
            }
            if (rd32(c.conn + OFF_GHOST_BITS, &v) && (!c.bits_seen || v != c.bits)) {
                c.bits_seen = 1;
                c.bits = v;
                wsprintfA(line, "BITS %lu", (unsigned long)v);
                probe_log(line);
            }
        }

        now = GetTickCount();
        if (now - stat_t >= STAT_PERIOD_MS) {
            stat_t = now;
            wsprintfA(line, "STAT pkt-samples %lu bits-changes %lu conn %08lx", pkt_samples, bits_changes,
                      (unsigned long)c.conn);
            probe_log(line);
            pkt_samples = 0;
            bits_changes = 0;
        }
        Sleep(10); /* 1 ms made the game stutter in the r247 live runs (CPU unchanged, frame pacing worse); 10 ms is enough for event order */
    }
    probe_log("END");
    return 0;
}

void bc2_client_probe_attach(void)
{
    if (!bc2_client_probe_enabled()) {
        OutputDebugStringA("dinput8 proxy: client probe off\r\n");
        return;
    }
    g_stop = 0;
    g_thread = CreateThread(NULL, 0, probe_thread, NULL, 0, NULL);
    OutputDebugStringA(g_thread ? "dinput8 proxy: client probe thread started\r\n"
                                : "dinput8 proxy: client probe thread failed\r\n");
}

void bc2_client_probe_detach(void)
{
    g_stop = 1;
}
