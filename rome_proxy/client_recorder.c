/* Client recorder (r248), see client_recorder.h and README section 5.
 *
 * Install (DllMain PROCESS_ATTACH of the statically imported dinput8.dll, before the exe entry point, so no game
 * thread exists while .text is written): identity gate (client_probe.c), ring + relocated recorder_v3 cave, then
 * three 5/6-byte .text writes, all-or-nothing (retail addresses below; r302: the Steam exe uses its own profile from
 * client_probe.c and installs from a thread once SteamStub has decrypted .text, see deferred_install_thread):
 *   0xC299A7  mov edx,[edi+0x238]   -> jmp cave (+ nop)        ghost entries (G/M/C/P records, live-proven cave bytes)
 *   0x65642F  call ClientConnection_Ctor -> jmp stub_connect   installs the packet observer on the new connection
 *   0x63D5EB  call g_ClientContext getter -> jmp stub_destroy  E record (the connection is already freed here)
 * The ctor is __thiscall with 5 stack args and `ret 0x14`, so the hook must be a jmp whose stub returns with
 * push/ret: a `call stub` that itself called the ctor would shift every argument by one slot (r248 correction of
 * the r247 design).
 * The observer runs on the dispatch thread before the packet is bound/dispatched and copies the packet bytes into
 * a Q record (same validation as the cave's P record, so Q == P on the direct mux). A writer thread drains the
 * ring every 50 ms into %LOCALAPPDATA%\BC2Replay\captures\*.bc2raw.
 * Every game-memory read is under __try; the game thread never blocks on the ring (try-lock, drop on contention).
 */
#include <windows.h>
#include <string.h>
#include "client_cave.h"
#include "bc2_ring.h"
#include "bc2_capture.h"
#include "client_probe.h"
#include "client_recorder.h"
#include "version.h"   /* BC2_DLL_VERSION (.bc2raw header) + BC2_VERSION_STRING, r266 */

/* ---- game addresses: retail BFBC2Game.exe (SHA-256 3911fcc8..., r245/r247 facts) by default; the identified
 * profile (client_probe.c PROFILES, r302: retail / Steam) overwrites them in attach before anything is hooked ---- */
static DWORD g_vt_connection = 0x014182A8u;   /* ClientConnection vtable, stored by the ctor at 0x5585CC */
static DWORD g_vt_mux_fragment = 0x0147D04Cu; /* ConnectionFragmentStream (direct mux = 0x147D218) */
static DWORD g_vt_level = 0x01432090u;
#define VT_CONNECTION        g_vt_connection
#define VT_MUX_FRAGMENT      g_vt_mux_fragment
#define VT_LEVEL             g_vt_level
#define OFF_CB_SLOT          0x1BCu      /* streamControl (+0x20) + 0x19C packet-callback slot, zeroed by its ctor */
#define OFF_DISPATCHER       0x2Cu       /* streamControl + 0xC */
#define OFF_GHOST_BITS       (0x12CCu + 0x244u)
#define OFF_LEVEL_NAME_B     0x28u
#define OFF_LEVEL_NAME_E     0x2Cu
#define SPAN_CAP             0x4000u     /* = recorder_v3_cave.SPAN_CAP */

#define SITE_CONNECT         0x0065642Fu
#define SITE_CONNECT_RET     0x00656434u
#define TGT_CTOR             0x005585A0u
#define SITE_DESTROY         0x0063D5EBu
#define SITE_DESTROY_RET     0x0063D5F0u
#define TGT_GETTER           0x007FF060u
/* original bytes at the connect / destroy sites: identical in both builds (caller and callee moved together, r301) */
static const BYTE OLD_CONNECT[5] = { 0xe8, 0x6c, 0x21, 0xf0, 0xff };
static const BYTE OLD_DESTROY[5] = { 0xe8, 0x70, 0x1a, 0x1c, 0x00 };

/* exe SHA-256 written into the .bc2raw header (retail by default; the profile's own hash once identified, so a
 * capture says which build recorded it and the builder can translate that build's vtables) */
static BYTE g_exe_sha256[32] = {
    0x39, 0x11, 0xfc, 0xc8, 0x91, 0x4b, 0x01, 0x58, 0xc4, 0x34, 0xce, 0xe3, 0x6f, 0x19, 0x29, 0x53,
    0x16, 0xe7, 0x15, 0xd4, 0xc9, 0xa0, 0xe7, 0x0c, 0x85, 0x7e, 0x8c, 0x39, 0xe3, 0xd5, 0x42, 0x58
};

/* r302: absolute game dwords inside the generated cave code (client_cave.h, cave sha a1b58406), retail values; a
 * profile replaces each one with its cave_lit[i] after checking the retail value is really there (a regenerated cave
 * with other offsets makes the install fail instead of patching blind). 0x7FFE00xx (KUSER) and the ring size are
 * not game addresses. */
static const struct { unsigned short off; DWORD retail; } CAVE_LIT[5] = {
    { 356, 0x0140D8C8u },  /* cmp eax, BitStream wrapper vtable */
    { 676, 0x01571434u },  /* mov eax, [ctx ready] */
    { 693, 0x015713D8u },  /* mov eax, [g_ClientContext] */
    { 699, 0x01431530u },  /* cmp eax, ClientContext vtable */
    { 711, 0x015713E0u },  /* mov eax, [ctx+8 child] */
};

#define CODE_ALLOC           4096u
#define DRAIN_CAP            (256u * 1024u)
#define WRITER_PERIOD_MS     50u
#define T_PERIOD_MS          1000u
#define FLUSH_PERIOD_MS      2000u
#define STAT_PERIOD_MS       10000u

/* Stub targets live in globals so the selftest can point them at fake functions. */
DWORD bc2_tgt_ctor = TGT_CTOR;
DWORD bc2_tgt_connect_ret = SITE_CONNECT_RET;
DWORD bc2_tgt_getter = TGT_GETTER;
DWORD bc2_tgt_destroy_ret = SITE_DESTROY_RET;

static DWORD g_addr_level = 0x01571410u;  /* [g_addr_level] = current ClientLevel */

/* ---- state ---- */
static bc2_ring g_ring;
static unsigned char *g_code;
static volatile LONG g_installed, g_stop, g_corrupt;
static volatile DWORD g_conn;            /* current ClientConnection or 0 */
static int g_observer_on;                /* slot was 0 and now holds &g_cb */
static DWORD g_last_level, g_last_name_b, g_last_name_e, g_last_bits;  /* S re-emit keys */
static unsigned g_qskip;                 /* observer: packets refused by the span/limit checks */
static HANDLE g_thread;
static char g_log_path[MAX_PATH];
static DWORD g_t0;
static volatile LONG g_paused;           /* r255 /rec off: writer discards, file closed */
#define BC2_RW_LAST_C       13   /* cave ring header: last_c(child, t0ptr, n0, t1ptr, n1) = words 13..17 (recorder_v3_cave) */
#define BC2_RW_LAST_C_WORDS 5
static char g_file_path[MAX_PATH];       /* current capture file (writer thread writes, status reads) */
static volatile LONG g_file_open;

/* ---- SEH helpers (game memory) ---- */
static int rd32(DWORD addr, DWORD *out)
{
    __try {
        *out = *(const volatile DWORD *)(UINT_PTR)addr;
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

static int wr32(DWORD addr, DWORD v)
{
    __try {
        *(volatile DWORD *)(UINT_PTR)addr = v;
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

static int cmpmem(DWORD addr, const BYTE *bytes, DWORD len)
{
    __try {
        return memcmp((const void *)(UINT_PTR)addr, bytes, len) == 0;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return -1;
    }
}

/* ---- log ---- */
static int local_appdata(char *out, DWORD size)
{
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", out, size);
    return n > 0 && n < size;
}

static void rec_log(const char *text)
{
    char line[1100];
    DWORD ms = GetTickCount() - g_t0, written;
    HANDLE f;
    wsprintfA(line, "dinput8 proxy: recorder +%lu %s\r\n", ms, text);
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

static void log_path_init(void)
{
    char dir[MAX_PATH], name[48];
    g_t0 = GetTickCount();
    g_log_path[0] = 0;
    if (!local_appdata(dir, MAX_PATH - 64))
        return;
    lstrcatA(dir, "\\BC2Replay");
    CreateDirectoryA(dir, NULL);
    lstrcatA(dir, "\\logs");
    CreateDirectoryA(dir, NULL);
    wsprintfA(name, "\\recorder_%lu.log", GetCurrentProcessId());
    if (lstrlenA(dir) + lstrlenA(name) < (int)sizeof(g_log_path)) {
        lstrcpyA(g_log_path, dir);
        lstrcatA(g_log_path, name);
    }
}

/* ---- gating ---- */
int bc2_client_recorder_enabled(void)
{
    char flag[8], ini[MAX_PATH], value[16];
    DWORD n = GetEnvironmentVariableA("BC2_REPLAY", flag, sizeof(flag));
    if (n == 1 && flag[0] == '1')
        return 0;
    if (!local_appdata(ini, MAX_PATH - 32))
        return 1;
    lstrcatA(ini, "\\BC2Replay\\config.ini");
    GetPrivateProfileStringA("dll", "record", "auto", value, sizeof(value), ini);
    return lstrcmpiA(value, "off") != 0;
}

/* ---- records (ring lock held by the caller) ---- */
static int level_name(DWORD *level, DWORD *b, DWORD *e, char *name, unsigned *len)
{
    DWORD vt = 0;
    *len = 0;
    *b = *e = 0;
    if (!rd32(g_addr_level, level) || !*level || !rd32(*level, &vt) || vt != VT_LEVEL)
        return 0;
    if (!rd32(*level + OFF_LEVEL_NAME_B, b) || !rd32(*level + OFF_LEVEL_NAME_E, e) || !*b || *e <= *b || *e - *b >= 256)
        return 0;
    if (!rdbuf(*b, name, *e - *b))
        return 0;
    *len = *e - *b;
    return 1;
}

static void emit_s(unsigned extra_flags)
{
    bc2_s_fields f;
    unsigned char body[BC2_S_MAX_BODY];
    char name[256];
    DWORD disp = 0, level = 0, b, e, disp_vt = 0, bits = 0;
    unsigned len;
    memset(&f, 0, sizeof(f));
    f.conn = g_conn;
    if (level_name(&level, &b, &e, name, &len))
        f.flags |= BC2_S_LEVEL_VALID;
    if (!rd32(g_conn + OFF_DISPATCHER, &disp) || !disp || !rd32(disp, &disp_vt))
        disp_vt = 0;
    f.dispatcher_vt = disp_vt;
    if (disp_vt == VT_MUX_FRAGMENT)
        f.flags |= BC2_S_FRAGMENT;
    f.flags |= extra_flags;
    if (!rd32(g_conn + OFF_GHOST_BITS, &bits))
        bits = 0;
    f.ghost_bits = bits;
    f.qseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_Q);
    f.pseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_P);
    f.level_ptr = level;
    f.filetime = bc2_filetime_now();
    f.interrupt_time = bc2_interrupt_time();
    f.name = name;
    f.name_len = len;
    bc2_ring_write(&g_ring, BC2_T_S, body, bc2_pack_s(body, &f));
    g_last_level = level;
    g_last_name_b = b;
    g_last_name_e = e;
    g_last_bits = f.ghost_bits;
}

/* Re-emit S when the level object / name range or the ghost id width changed (cheap pointer compares). */
static void maybe_emit_s(void)
{
    DWORD level = 0, vt = 0, b = 0, e = 0, bits = 0;
    if (rd32(g_addr_level, &level) && level && rd32(level, &vt) && vt == VT_LEVEL) {
        if (!rd32(level + OFF_LEVEL_NAME_B, &b) || !rd32(level + OFF_LEVEL_NAME_E, &e))
            b = e = 0;
    } else {
        level = 0;
    }
    if (!rd32(g_conn + OFF_GHOST_BITS, &bits))
        bits = 0;
    if (level != g_last_level || b != g_last_name_b || e != g_last_name_e || bits != g_last_bits)
        emit_s(g_observer_on ? 0 : BC2_S_NO_OBSERVER);
}

static void emit_e(unsigned reason)
{
    bc2_e_fields f;
    unsigned char body[BC2_E_BODY_LEN];
    f.reason = reason;
    f.interrupt_time = bc2_interrupt_time();
    f.qseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_Q);
    f.pseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_P);
    bc2_ring_write(&g_ring, BC2_T_E, body, bc2_pack_e(body, &f));
}

static void emit_t(void)
{
    bc2_t_fields f;
    unsigned char body[BC2_T_BODY_LEN];
    f.interrupt_time = bc2_interrupt_time();
    f.filetime = bc2_filetime_now();
    f.qseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_Q);
    f.pseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_P);
    f.prod = BC2_RING_H(&g_ring, BC2_RW_PROD);
    f.drops = BC2_RING_H(&g_ring, BC2_RW_DROPS);
    f.contention = BC2_RING_H(&g_ring, BC2_RW_CONT);
    f.skip = BC2_RING_H(&g_ring, BC2_RW_SKIP);
    bc2_ring_write(&g_ring, BC2_T_T, body, bc2_pack_t(body, &f));
}

/* ---- packet observer: void __thiscall slot0(cb *this, void *readerState), ret 4, return ignored ---- */
static void __fastcall observer(void *self, void *edx_unused, void *state_)
{
    DWORD state = (DWORD)(UINT_PTR)state_, buffer = 0, limit = 0, end = 0, span, t1, t2, t3, qseq;
    unsigned total;
    unsigned char *rec;
    (void)self; (void)edx_unused;
    if (!g_installed || !g_conn)
        return;
    if (!rd32(state + 4, &buffer) || !rd32(state + 8, &limit) || !rd32(state + 0xC, &end))
        return;
    span = end - buffer;
    if (!buffer || end < buffer || span < 4 || span > SPAN_CAP || limit > 8 * span) {
        g_qskip++;
        return;
    }
    t1 = *(volatile DWORD *)0x7FFE000C;  /* KUSER InterruptTime, same order as the cave */
    t2 = *(volatile DWORD *)0x7FFE0008;
    t3 = *(volatile DWORD *)0x7FFE0010;
    if (!bc2_ring_try_lock(&g_ring))
        return;
    qseq = BC2_RING_H(&g_ring, BC2_RW_SEQ_Q) + 1;
    total = (4 + BC2_Q_HEAD_LEN + span + 3) & ~3u;
    rec = bc2_ring_reserve(&g_ring, total);
    if (rec) {
        if (rdbuf(buffer, rec + 4 + BC2_Q_HEAD_LEN, span)) {
            bc2_q_head_fields q;
            q.t_hi1 = t1; q.t_lo = t2; q.t_hi2 = t3; q.qseq = qseq;
            q.buffer = buffer; q.limit = limit; q.span = span;
            memcpy(&q.first_dword, rec + 4 + BC2_Q_HEAD_LEN, 4);
            bc2_pack_q_head(rec + 4, &q);
            bc2_ring_publish(rec, BC2_T_Q, total);
            BC2_RING_H(&g_ring, BC2_RW_SEQ_Q) = qseq;
        } else {
            bc2_ring_abandon(rec, total);  /* payload vanished under us: PAD, never a head that stays 0 */
        }
    }
    maybe_emit_s();
    bc2_ring_unlock(&g_ring);
}

static const void *const g_cb_vtable[1] = { (const void *)observer };
struct bc2_cb { const void *const *vtable; };
static struct bc2_cb g_cb = { g_cb_vtable };

/* ---- hook callbacks (game thread, inside the connect / destroy routines) ---- */
static void __cdecl on_connect(DWORD conn)
{
    char line[160];
    DWORD vt = 0, slot = 1;
    int installed = 0;
    if (!rd32(conn, &vt) || vt != VT_CONNECTION) {
        wsprintfA(line, "CONN %08lx vt %08lx mismatch, ignored", (unsigned long)conn, (unsigned long)vt);
        rec_log(line);
        return;
    }
    if (rd32(conn + OFF_CB_SLOT, &slot) && slot == 0)
        installed = wr32(conn + OFF_CB_SLOT, (DWORD)(UINT_PTR)&g_cb);
    g_observer_on = installed;
    g_last_level = g_last_name_b = g_last_name_e = g_last_bits = 0;
    g_conn = conn;
    if (bc2_ring_try_lock(&g_ring)) {
        unsigned i;
        /* r291: the cave writes a C (config) record only when the config key changes (ring header words 13..17,
         * last_c); a second connection in the same process reuses the config object, so its capture file got no C
         * record and decoded to nothing. Clear the key: the first G of this connection re-emits C. */
        for (i = BC2_RW_LAST_C; i < BC2_RW_LAST_C + BC2_RW_LAST_C_WORDS; i++)
            BC2_RING_H(&g_ring, i) = 0;
        emit_s(installed ? 0 : BC2_S_NO_OBSERVER);
        bc2_ring_unlock(&g_ring);
    }
    wsprintfA(line, "CONN %08lx observer %s (slot was %08lx)", (unsigned long)conn,
              installed ? "installed" : "NOT installed", (unsigned long)slot);
    rec_log(line);
}

static void __cdecl on_destroy(void)
{
    char line[96];
    DWORD conn = g_conn;
    if (!conn)
        return;
    g_conn = 0;  /* the connection is freed already; never dereference it */
    if (bc2_ring_try_lock(&g_ring)) {
        emit_e(BC2_E_DESTROY);
        bc2_ring_unlock(&g_ring);
    }
    wsprintfA(line, "CONN %08lx gone, E written", (unsigned long)conn);
    rec_log(line);
}

/* Entered by `jmp` from 0x65642F: the stack holds exactly the ctor's 5 args, ecx = this.
 * The ctor pops its args (ret 0x14) and returns conn in eax; we return to 0x656434 with eax intact. */
__declspec(naked) static void stub_connect(void)
{
    __asm {
        mov eax, dword ptr [bc2_tgt_ctor]
        call eax
        test eax, eax
        jz skip
        pushad
        push eax
        call on_connect
        add esp, 4
        popad
    skip:
        push dword ptr [bc2_tgt_connect_ret]
        ret
    }
}

/* Entered by `jmp` from 0x63D5EB: the getter takes no args; its eax is stored at 0x63D5F0, so keep it. */
__declspec(naked) static void stub_destroy(void)
{
    __asm {
        mov eax, dword ptr [bc2_tgt_getter]
        call eax
        pushad
        call on_destroy
        popad
        push dword ptr [bc2_tgt_destroy_ret]
        ret
    }
}

/* ---- cave relocation and patch bytes ---- */
static void apply_fixups(unsigned char *code, DWORD ring, DWORD code_addr)
{
    unsigned i;
    DWORD v;
    for (i = 0; i < BC2_CAVE_RING_FIXUPS; i++) {
        memcpy(&v, code + bc2_cave_ring_fixups[i], 4);
        v += ring - BC2_CAVE_REF_RING;
        memcpy(code + bc2_cave_ring_fixups[i], &v, 4);
    }
    for (i = 0; i < BC2_CAVE_CODE_FIXUPS; i++) {
        memcpy(&v, code + bc2_cave_code_fixups[i], 4);
        v -= code_addr - BC2_CAVE_REF_CODE;
        memcpy(code + bc2_cave_code_fixups[i], &v, 4);
    }
}

/* r302: after apply_fixups, retarget the relocated cave to profile p: the 5 absolute game dwords and the resume
 * jmp rel32 (the one code fixup). Every dword is checked against its retail value first; 0 = cave layout unknown,
 * nothing may be installed. For retail the result is byte-identical to apply_fixups alone. */
static int cave_apply_profile(unsigned char *code, DWORD code_addr, const bc2_rec_profile *p)
{
    unsigned i;
    DWORD v, off;
#if BC2_CAVE_CODE_FIXUPS != 1
#error cave_apply_profile expects exactly one code fixup (the resume jmp rel32)
#endif
    for (i = 0; i < 5; i++) {
        memcpy(&v, code + CAVE_LIT[i].off, 4);
        if (v != CAVE_LIT[i].retail)
            return 0;
    }
    off = bc2_cave_code_fixups[0];
    memcpy(&v, code + off, 4);
    if (v != BC2_CAVE_RESUME - (code_addr + off + 4) || code[off - 1] != 0xe9)
        return 0;
    for (i = 0; i < 5; i++)
        memcpy(code + CAVE_LIT[i].off, &p->cave_lit[i], 4);
    v = p->cave_resume - (code_addr + off + 4);
    memcpy(code + off, &v, 4);
    return 1;
}

/* Game addresses of the C side (stubs, observer, S records, capture header) from profile p. */
static void use_profile(const bc2_rec_profile *p)
{
    g_vt_connection = p->vt_connection;
    g_vt_mux_fragment = p->vt_mux_fragment;
    g_vt_level = p->vt_level;
    g_addr_level = p->g_level;
    bc2_tgt_ctor = p->tgt_ctor;
    bc2_tgt_connect_ret = p->site_connect_ret;
    bc2_tgt_getter = p->tgt_getter;
    bc2_tgt_destroy_ret = p->site_destroy_ret;
    memcpy(g_exe_sha256, p->sha256, 32);
}

static void jmp_bytes(DWORD site, DWORD target, BYTE out[5])
{
    DWORD rel = target - (site + 5);
    out[0] = 0xe9;
    memcpy(out + 1, &rel, 4);
}

/* ---- writer thread ---- */
typedef struct { unsigned char *buf; bc2_capture file; char dir[MAX_PATH]; int dir_ok, open_failed_logged; } writer;

/* Records in [0, len) are complete. Returns the byte length up to and including the first E record (or len);
 * *ends_with_e = 1 when that E exists. */
static unsigned chunk_until_e(const unsigned char *p, unsigned len, int *ends_with_e)
{
    unsigned off = 0, head, total;
    *ends_with_e = 0;
    while (off + 4 <= len) {
        memcpy(&head, p + off, 4);
        total = head >> 8;
        if (total < 4 || off + total > len)
            break;
        off += total;
        if ((head & 0xFF) == BC2_T_E) {
            *ends_with_e = 1;
            return off;
        }
    }
    return len;
}

static void writer_append(writer *w, const unsigned char *p, unsigned len)
{
    char line[MAX_PATH + 96];
    while (len) {
        int ends;
        unsigned n = chunk_until_e(p, len, &ends);
        if (w->file.h == INVALID_HANDLE_VALUE) {
            if (!w->dir_ok || !bc2_capture_open(&w->file, w->dir, g_exe_sha256, BC2_DLL_VERSION, BC2_RING_SIZE)) {
                if (!w->open_failed_logged) {  /* once, not on every 50 ms pass */
                    w->open_failed_logged = 1;
                    rec_log("capture open FAILED, records discarded");
                }
                return;
            }
            w->open_failed_logged = 0;
            wsprintfA(line, "FILE open %s", w->file.path);
            rec_log(line);
            lstrcpynA(g_file_path, w->file.path, MAX_PATH);
            g_file_open = 1;
        }
        if (!bc2_capture_append(&w->file, p, n))
            rec_log("capture append FAILED");
        if (ends) {
            wsprintfA(line, "FILE close %s bytes %lu", w->file.path, (unsigned long)w->file.bytes);
            rec_log(line);
            bc2_capture_close(&w->file);
            w->file.h = INVALID_HANDLE_VALUE;
            g_file_open = 0;
        }
        p += n;
        len -= n;
    }
}

/* One drain pass: everything complete in the ring goes to the file(s). Returns bytes drained. */
static unsigned writer_drain(writer *w)
{
    unsigned total = 0, n;
    int corrupt = 0;
    for (;;) {
        n = bc2_ring_drain(&g_ring, w->buf, DRAIN_CAP, &corrupt);
        if (corrupt) {
            if (!g_corrupt) {
                g_corrupt = 1;
                rec_log("ring CORRUPT, draining stopped");
            }
            break;
        }
        if (!n)
            break;
        if (g_paused) {  /* r255 /rec off: drop what the ring holds, keep no file open */
            if (w->file.h != INVALID_HANDLE_VALUE) {
                char line[MAX_PATH + 64];
                wsprintfA(line, "FILE close %s (paused) bytes %lu", w->file.path, (unsigned long)w->file.bytes);
                rec_log(line);
                bc2_capture_close(&w->file);
                w->file.h = INVALID_HANDLE_VALUE;
                g_file_open = 0;
            }
        } else
            writer_append(w, w->buf, n);
        total += n;
    }
    return total;
}

/* ---- r255 chat commands ---- */
void bc2_client_recorder_status(char *out, int out_size)
{
    char line[MAX_PATH + 160];
    if (!g_installed) {
        lstrcpynA(out, "rec: not installed (off, refused or replay session)", out_size);
        return;
    }
    wsprintfA(line, "rec: %s, conn %08lx, %s%s, drops %lu, Q %lu", g_paused ? "PAUSED" : "recording",
              (unsigned long)g_conn, g_file_open ? "file " : "no file open", g_file_open ? g_file_path : "",
              (unsigned long)BC2_RING_H(&g_ring, BC2_RW_DROPS), (unsigned long)BC2_RING_H(&g_ring, BC2_RW_SEQ_Q));
    lstrcpynA(out, line, out_size);
}

int bc2_client_recorder_pause(int paused)
{
    if (!g_installed)
        return 0;
    if (g_paused && !paused)
        g_last_level = 0;  /* force an S record on the next packet so the new file starts self-describing */
    g_paused = paused ? 1 : 0;
    rec_log(paused ? "/rec off: paused" : "/rec on: resumed");
    return 1;
}

static void stat_line(const writer *w, unsigned long long drained)
{
    char line[320];
    wsprintfA(line, "STAT prod %lu cons %lu drops %lu cont %lu skip %lu qskip %lu qseq %lu pseq %lu drained %lu KB file %lu KB conn %08lx",
              (unsigned long)BC2_RING_H(&g_ring, BC2_RW_PROD), (unsigned long)BC2_RING_H(&g_ring, BC2_RW_CONS),
              (unsigned long)BC2_RING_H(&g_ring, BC2_RW_DROPS), (unsigned long)BC2_RING_H(&g_ring, BC2_RW_CONT),
              (unsigned long)BC2_RING_H(&g_ring, BC2_RW_SKIP), (unsigned long)g_qskip,
              (unsigned long)BC2_RING_H(&g_ring, BC2_RW_SEQ_Q), (unsigned long)BC2_RING_H(&g_ring, BC2_RW_SEQ_P),
              (unsigned long)(drained >> 10), (unsigned long)(w->file.h == INVALID_HANDLE_VALUE ? 0 : w->file.bytes >> 10),
              (unsigned long)g_conn);
    rec_log(line);
}

static int writer_init(writer *w)
{
    memset(w, 0, sizeof(*w));
    w->file.h = INVALID_HANDLE_VALUE;
    w->buf = (unsigned char *)VirtualAlloc(NULL, DRAIN_CAP, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    w->dir_ok = bc2_capture_dir(w->dir, sizeof(w->dir), 1);
    return w->buf != NULL;
}

static DWORD WINAPI writer_thread(LPVOID arg)
{
    writer w;
    DWORD now, last_t, last_flush, last_stat;
    unsigned long long drained = 0;
    (void)arg;
    if (!writer_init(&w)) {
        rec_log("writer: buffer allocation failed, thread exits (ring fills, records drop)");
        return 0;
    }
    rec_log(w.dir_ok ? "writer thread running" : "writer: no capture dir (LOCALAPPDATA), records will be discarded");
    now = last_t = last_flush = last_stat = GetTickCount();
    while (!g_stop && !g_corrupt) {
        drained += writer_drain(&w);
        now = GetTickCount();
        if (now - last_t >= T_PERIOD_MS) {
            last_t = now;
            if (g_conn && bc2_ring_try_lock(&g_ring)) {  /* T only while a connection is live: no file in the menus */
                emit_t();
                bc2_ring_unlock(&g_ring);
            }
        }
        if (w.file.h != INVALID_HANDLE_VALUE && now - last_flush >= FLUSH_PERIOD_MS) {
            last_flush = now;
            bc2_capture_flush(&w.file);
        }
        if (now - last_stat >= STAT_PERIOD_MS) {
            last_stat = now;
            stat_line(&w, drained);
        }
        Sleep(WRITER_PERIOD_MS);
    }
    stat_line(&w, drained);
    bc2_capture_close(&w.file);
    rec_log("writer thread stopped");
    return 0;
}

/* ---- install ---- */
#ifndef BC2_RECORDER_SELFTEST
typedef struct { DWORD va; DWORD len; const BYTE *old_bytes; BYTE new_bytes[6]; const char *name; } site_patch;

static int patch_sites(site_patch *s, int n, char *why)
{
    DWORD old_prot[3], tmp;
    int i, done, rc;
    for (i = 0; i < n; i++) {
        rc = cmpmem(s[i].va, s[i].old_bytes, s[i].len);
        if (rc != 1) {
            wsprintfA(why, "site %08lx %s (%s)", (unsigned long)s[i].va, rc < 0 ? "unreadable" : "bytes differ", s[i].name);
            return 0;
        }
    }
    for (done = 0; done < n; done++)
        if (!VirtualProtect((void *)(UINT_PTR)s[done].va, s[done].len, PAGE_EXECUTE_READWRITE, &old_prot[done]))
            break;
    if (done == n)
        for (i = 0; i < n; i++)
            memcpy((void *)(UINT_PTR)s[i].va, s[i].new_bytes, s[i].len);
    for (i = done - 1; i >= 0; i--) {
        VirtualProtect((void *)(UINT_PTR)s[i].va, s[i].len, old_prot[i], &tmp);
        FlushInstructionCache(GetCurrentProcess(), (void *)(UINT_PTR)s[i].va, s[i].len);
    }
    if (done != n) {
        wsprintfA(why, "VirtualProtect failed at %s (error %lu); nothing written", s[done].name, GetLastError());
        return 0;
    }
    return 1;
}

static site_patch g_sites[3];
static const bc2_rec_profile *g_prof;
static DWORD g_ring_va, g_code_va;

/* Patch the three sites (all-or-nothing), then start recording. Retail: DllMain; Steam: the deferred thread. */
static int install_sites(void)
{
    char why[256], line[400];
    int i;
    if (!patch_sites(g_sites, 3, why)) {
        wsprintfA(line, "install FAILED: %s", why);
        rec_log(line);
        return 0;  /* ring/code stay allocated (nothing points at them) */
    }
    g_installed = 1;
    g_thread = CreateThread(NULL, 0, writer_thread, NULL, 0, NULL);
    wsprintfA(line, "installed (%s): ring %08lx (%lu KB) code %08lx (%u B, %u+%u fixups) stubs %08lx/%08lx cb %08lx writer %s",
              g_prof->name, (unsigned long)g_ring_va, (unsigned long)(BC2_RING_SIZE >> 10), (unsigned long)g_code_va,
              (unsigned)BC2_CAVE_CODE_LEN, (unsigned)BC2_CAVE_RING_FIXUPS, (unsigned)BC2_CAVE_CODE_FIXUPS,
              (unsigned long)(UINT_PTR)stub_connect, (unsigned long)(UINT_PTR)stub_destroy,
              (unsigned long)(UINT_PTR)&g_cb, g_thread ? "started" : "FAILED");
    rec_log(line);
    for (i = 0; i < 3; i++) {
        wsprintfA(line, "site %s %08lx: %02x %02x %02x %02x %02x%s", g_sites[i].name, (unsigned long)g_sites[i].va,
                  g_sites[i].new_bytes[0], g_sites[i].new_bytes[1], g_sites[i].new_bytes[2], g_sites[i].new_bytes[3],
                  g_sites[i].new_bytes[4], g_sites[i].len == 6 ? " 90" : "");
        rec_log(line);
    }
    return 1;
}

/* Steam (r302): SteamStub decrypts .text after every DllMain and before the game's entry point, so the site bytes
 * read as ciphertext here. Wait until all of them hold the originals, patch, then check 2 s later that nothing
 * (a late stub write) undid the patch. Connects happen only after the menus, long after the stub. */
#define DEFERRED_TIMEOUT_MS 120000u
static DWORD WINAPI deferred_install_thread(LPVOID arg)
{
    char why[256], line[400];
    int waited, i, intact = 1;
    (void)arg;
    waited = bc2_client_probe_wait_sites(g_prof, DEFERRED_TIMEOUT_MS, why);
    if (waited < 0) {
        wsprintfA(line, "deferred install FAILED: sites not original after %lu ms (%s); nothing installed",
                  (unsigned long)DEFERRED_TIMEOUT_MS, why);
        rec_log(line);
        return 0;
    }
    wsprintfA(line, ".text decrypted: sites original after %d ms", waited);
    rec_log(line);
    if (!install_sites())
        return 0;
    Sleep(2000);
    for (i = 0; i < 3; i++)
        if (cmpmem(g_sites[i].va, g_sites[i].new_bytes, g_sites[i].len) != 1) {
            intact = 0;
            wsprintfA(line, "WARNING site %s %08lx no longer holds the patch (overwritten after install?)",
                      g_sites[i].name, (unsigned long)g_sites[i].va);
            rec_log(line);
        }
    if (intact)
        rec_log("deferred install: patches intact after 2 s");
    return 0;
}

int bc2_client_recorder_attach(int static_load)
{
    char why[256], line[400];
    void *ring_mem;
    DWORD tmp, code_va, ring_va;
    site_patch *sites = g_sites;
    const bc2_rec_profile *p;
    HANDLE t;

    log_path_init();
    rec_log("version " BC2_VERSION_STRING);  /* r266: first line of every recorder_<pid>.log */
    if (!bc2_client_recorder_enabled()) {
        rec_log("off (BC2_REPLAY=1 or ini record=off); nothing installed");
        return 0;
    }
    if (!static_load) {
        rec_log("dynamic load (lpReserved == NULL): refused, the exe may already run");
        return 0;
    }
    /* bcrypt is a static import of this DLL, so hashing the exe inside DllMain is safe from loader-lock
     * deadlocks; it costs one read of the exe file at process start. */
    p = bc2_client_probe_identify(why);
    if (!p) {
        wsprintfA(line, "GATE fail %s; nothing installed", why);
        rec_log(line);
        return 0;
    }
    if (!p->deferred && !bc2_client_probe_sites_ok(p, why)) {
        wsprintfA(line, "GATE fail %s; nothing installed", why);
        rec_log(line);
        return 0;
    }
    wsprintfA(line, "profile %s", p->name);
    rec_log(line);
    g_prof = p;
    use_profile(p);
    ring_mem = VirtualAlloc(NULL, BC2_RING_HDR + BC2_RING_SIZE, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    g_code = (unsigned char *)VirtualAlloc(NULL, CODE_ALLOC, MEM_COMMIT | MEM_RESERVE, PAGE_EXECUTE_READWRITE);
    if (!ring_mem || !g_code || !bc2_ring_init(&g_ring, ring_mem, BC2_RING_SIZE)) {
        rec_log("ring/code allocation failed; nothing installed");
        return 0;
    }
    ring_va = (DWORD)(UINT_PTR)g_ring.base;
    code_va = (DWORD)(UINT_PTR)g_code;
    g_ring_va = ring_va;
    g_code_va = code_va;
    memcpy(g_code, bc2_cave_code, BC2_CAVE_CODE_LEN);
    apply_fixups(g_code, ring_va, code_va);
    if (!cave_apply_profile(g_code, code_va, p)) {
        rec_log("cave literals differ from the expected layout (regenerated cave?); nothing installed");
        return 0;
    }
    VirtualProtect(g_code, CODE_ALLOC, PAGE_EXECUTE_READ, &tmp);
    FlushInstructionCache(GetCurrentProcess(), g_code, CODE_ALLOC);

    sites[0].va = p->cave_site; sites[0].len = BC2_CAVE_SITE_LEN; sites[0].old_bytes = bc2_cave_original;
    sites[0].name = "ghost cave";
    jmp_bytes(p->cave_site, code_va, sites[0].new_bytes); sites[0].new_bytes[5] = 0x90;
    sites[1].va = p->site_connect; sites[1].len = 5; sites[1].old_bytes = OLD_CONNECT; sites[1].name = "connect";
    jmp_bytes(p->site_connect, (DWORD)(UINT_PTR)stub_connect, sites[1].new_bytes);
    sites[2].va = p->site_destroy; sites[2].len = 5; sites[2].old_bytes = OLD_DESTROY; sites[2].name = "destroy";
    jmp_bytes(p->site_destroy, (DWORD)(UINT_PTR)stub_destroy, sites[2].new_bytes);
    if (!p->deferred)
        return install_sites();
    /* The thread's own start blocks on the loader lock until the process's DllMains are done; then it runs beside
     * the stub. Returning 1 keeps the probe off, as for an installed recorder. */
    t = CreateThread(NULL, 0, deferred_install_thread, NULL, 0, NULL);
    rec_log(t ? "deferred install armed: waiting for the stub to decrypt .text" : "deferred install thread FAILED; nothing installed");
    if (t)
        CloseHandle(t);
    return t != NULL;
}
#else
int bc2_client_recorder_attach(int static_load) { (void)static_load; return 0; }
#endif

void bc2_client_recorder_detach(void)
{
    g_stop = 1;
}

/* ---- selftest hooks (no game): expose the pieces to client_recorder_selftest.c ---- */
#ifdef BC2_RECORDER_SELFTEST
int bc2_recorder_selftest_init(void *ring_mem)
{
    g_log_path[0] = 0;
    g_t0 = GetTickCount();
    if (!bc2_ring_init(&g_ring, ring_mem, BC2_RING_SIZE))
        return 0;
    g_installed = 1;
    g_conn = 0;
    return 1;
}
bc2_ring *bc2_recorder_selftest_ring(void) { return &g_ring; }
void *bc2_recorder_selftest_cb(void) { return &g_cb; }
void *bc2_recorder_selftest_stub_connect(void) { return (void *)stub_connect; }
void *bc2_recorder_selftest_stub_destroy(void) { return (void *)stub_destroy; }
void bc2_recorder_selftest_set_level_addr(DWORD addr) { g_addr_level = addr; }
DWORD bc2_recorder_selftest_conn(void) { return g_conn; }
unsigned bc2_recorder_selftest_qskip(void) { return g_qskip; }
void bc2_recorder_selftest_apply_fixups(unsigned char *code, DWORD ring, DWORD code_addr) { apply_fixups(code, ring, code_addr); }
void bc2_recorder_selftest_jmp_bytes(DWORD site, DWORD target, BYTE out[5]) { jmp_bytes(site, target, out); }
int bc2_recorder_selftest_cave_apply_profile(unsigned char *code, DWORD code_addr, const char *profile)
{
    const bc2_rec_profile *p = bc2_rec_profile_by_name(profile);
    return p && cave_apply_profile(code, code_addr, p);
}
/* switch the C side to a profile and back (selftest restores "retail" before the fake-game checks) */
int bc2_recorder_selftest_use_profile(const char *profile)
{
    const bc2_rec_profile *p = bc2_rec_profile_by_name(profile);
    if (p)
        use_profile(p);
    return p != NULL;
}
DWORD bc2_recorder_selftest_vt_connection(void) { return g_vt_connection; }
/* One writer pass into dir (open, drain, close). Returns bytes drained; path_out = the file (MAX_PATH). */
unsigned bc2_recorder_selftest_drain_once(const char *dir, char *path_out)
{
    writer w;
    unsigned n;
    if (!writer_init(&w))
        return 0;
    lstrcpynA(w.dir, dir, MAX_PATH);
    w.dir_ok = 1;
    n = writer_drain(&w);
    lstrcpynA(path_out, w.file.path, MAX_PATH);  /* the path of the last file opened (closed already at an E) */
    bc2_capture_close(&w.file);
    VirtualFree(w.buf, 0, MEM_RELEASE);
    return n;
}
#endif
