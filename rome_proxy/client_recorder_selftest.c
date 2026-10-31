/* Offline selftest of client_recorder.c (built with -DBC2_RECORDER_SELFTEST: no gate, no .text writes, game
 * addresses replaced by fake memory). Checks the cave relocation, the two naked hook stubs (stack discipline
 * against a fake ctor / getter), the __thiscall observer (ret 4, Q and S records) and one writer pass.
 * Usage: client_recorder_selftest.exe [outdir]   (default out_shared); exit 0 = all checks passed. */
#include <windows.h>
#include <bcrypt.h>
#include <stdio.h>
#include <string.h>
#include "client_cave.h"
#include "bc2_ring.h"
#include "bc2_capture.h"

/* client_recorder.c selftest exports */
int bc2_recorder_selftest_init(void *ring_mem);
bc2_ring *bc2_recorder_selftest_ring(void);
void *bc2_recorder_selftest_cb(void);
void *bc2_recorder_selftest_stub_connect(void);
void *bc2_recorder_selftest_stub_destroy(void);
void bc2_recorder_selftest_set_level_addr(DWORD addr);
DWORD bc2_recorder_selftest_conn(void);
unsigned bc2_recorder_selftest_qskip(void);
void bc2_recorder_selftest_apply_fixups(unsigned char *code, DWORD ring, DWORD code_addr);
void bc2_recorder_selftest_jmp_bytes(DWORD site, DWORD target, BYTE out[5]);
unsigned bc2_recorder_selftest_drain_once(const char *dir, char *path_out);
int bc2_recorder_selftest_cave_apply_profile(unsigned char *code, DWORD code_addr, const char *profile);
int bc2_recorder_selftest_use_profile(const char *profile);
DWORD bc2_recorder_selftest_vt_connection(void);
extern DWORD bc2_tgt_ctor, bc2_tgt_connect_ret, bc2_tgt_getter, bc2_tgt_destroy_ret;

static int fails;
static void check(int ok, const char *what)
{
    printf("%s %s\n", ok ? "ok  " : "FAIL", what);
    if (!ok) fails++;
}

/* ---- fake game objects ---- */
#define FAKE_CONN_SIZE 0x1600
static unsigned char fake_conn[FAKE_CONN_SIZE];
static DWORD fake_dispatcher[4] = { 0x0147D218u, 0, 0, 0 };
static DWORD fake_level[16];
static DWORD fake_level_ptr;             /* [g_addr_level] */
static const char LEVEL_NAME[] = "Levels/MP_006CQ";

static void fake_conn_init(void)
{
    memset(fake_conn, 0, sizeof(fake_conn));
    *(DWORD *)(fake_conn + 0) = 0x014182A8u;
    *(DWORD *)(fake_conn + 0x2C) = (DWORD)(UINT_PTR)fake_dispatcher;
    *(DWORD *)(fake_conn + 0x12CC + 0x244) = 13;
    memset(fake_level, 0, sizeof(fake_level));
    fake_level[0] = 0x01432090u;
    fake_level[0x28 / 4] = (DWORD)(UINT_PTR)LEVEL_NAME;
    fake_level[0x2C / 4] = (DWORD)(UINT_PTR)(LEVEL_NAME + sizeof(LEVEL_NAME) - 1);
    fake_level_ptr = (DWORD)(UINT_PTR)fake_level;
    bc2_recorder_selftest_set_level_addr((DWORD)(UINT_PTR)&fake_level_ptr);
}

/* ---- fake ctor (__thiscall, 5 stack args, ret 0x14) and getter ---- */
static DWORD seen_ecx, seen_args[5], ctor_result, drv_eax, drv_esp_before, drv_esp_after;
static void *p_stub, *p_cont;

__declspec(naked) static void fake_ctor(void)
{
    __asm {
        mov dword ptr [seen_ecx], ecx
        mov eax, dword ptr [esp + 4]
        mov dword ptr [seen_args], eax
        mov eax, dword ptr [esp + 8]
        mov dword ptr [seen_args + 4], eax
        mov eax, dword ptr [esp + 12]
        mov dword ptr [seen_args + 8], eax
        mov eax, dword ptr [esp + 16]
        mov dword ptr [seen_args + 12], eax
        mov eax, dword ptr [esp + 20]
        mov dword ptr [seen_args + 16], eax
        mov eax, dword ptr [ctor_result]
        ret 0x14
    }
}

__declspec(naked) static void fake_getter(void)
{
    __asm {
        mov eax, 0x0BEEF123
        ret
    }
}

/* The stubs return by `push ret; ret` to a fixed address: the continuation is a separate naked function that
 * records eax/esp and restores what the driver saved. */
__declspec(naked) static void drive_cont(void)
{
    __asm {
        mov dword ptr [drv_eax], eax
        mov dword ptr [drv_esp_after], esp
        pop ebp
        pop edi
        pop esi
        pop ebx
        ret
    }
}

/* push 5 args (1..5, so [esp+4] = 1 inside the ctor), ecx = 0x1234, jmp to the connect stub */
__declspec(naked) static void drive_connect(void)
{
    __asm {
        push ebx
        push esi
        push edi
        push ebp
        mov dword ptr [drv_esp_before], esp
        push 5
        push 4
        push 3
        push 2
        push 1
        mov ecx, 0x1234
        mov eax, dword ptr [p_stub]
        jmp eax
    }
}

/* no args; jmp to the destroy stub */
__declspec(naked) static void drive_destroy(void)
{
    __asm {
        push ebx
        push esi
        push edi
        push ebp
        mov dword ptr [drv_esp_before], esp
        mov eax, dword ptr [p_stub]
        jmp eax
    }
}

/* __thiscall-shaped observer call: ecx = cb, one stack arg, callee ret 4 */
static void *obs_cb, *obs_state;
__declspec(naked) static void drive_observer(void)
{
    __asm {
        push ebx
        push esi
        push edi
        push ebp
        mov dword ptr [drv_esp_before], esp
        mov ecx, dword ptr [obs_cb]
        push dword ptr [obs_state]
        mov eax, dword ptr [ecx]
        call dword ptr [eax]
        mov dword ptr [drv_esp_after], esp
        pop ebp
        pop edi
        pop esi
        pop ebx
        ret
    }
}

/* ---- ring inspection: drain into a buffer, walk records ---- */
static unsigned char drained[1 << 16];
static unsigned drained_len;

static unsigned drain_all(void)
{
    int corrupt = 0;
    drained_len = bc2_ring_drain(bc2_recorder_selftest_ring(), drained, sizeof(drained), &corrupt);
    check(!corrupt, "drain: ring not corrupt");
    return drained_len;
}

/* n-th record (0-based) of the drained buffer -> type, body pointer, body length; 0 if absent */
static int record(unsigned n, unsigned *type, const unsigned char **body, unsigned *len)
{
    unsigned off = 0, head, total, i = 0;
    while (off + 4 <= drained_len) {
        memcpy(&head, drained + off, 4);
        total = head >> 8;
        if (total < 4 || off + total > drained_len) return 0;
        if (i == n) {
            *type = head & 0xFF; *body = drained + off + 4; *len = total - 4;
            return 1;
        }
        off += total; i++;
    }
    return 0;
}

static unsigned record_count(void)
{
    unsigned off = 0, head, total, i = 0;
    while (off + 4 <= drained_len) {
        memcpy(&head, drained + off, 4);
        total = head >> 8;
        if (total < 4 || off + total > drained_len) break;
        off += total; i++;
    }
    return i;
}

static DWORD u32at(const unsigned char *p, unsigned off) { DWORD v; memcpy(&v, p + off, 4); return v; }

static int sha256(const void *data, DWORD len, BYTE out[32])
{
    BCRYPT_ALG_HANDLE alg = NULL;
    int ok = 0;
    if (BCryptOpenAlgorithmProvider(&alg, BCRYPT_SHA256_ALGORITHM, NULL, 0) >= 0) {
        ok = BCryptHash(alg, NULL, 0, (PUCHAR)data, len, out, 32) >= 0;
        BCryptCloseAlgorithmProvider(alg, 0);
    }
    return ok;
}

int main(int argc, char **argv)
{
    const char *outdir = argc > 1 ? argv[1] : "out_shared";
    unsigned char code[BC2_CAVE_CODE_LEN];
    BYTE hash[32], jb[5];
    void *ring_mem;
    unsigned type, len, n;
    const unsigned char *body;
    DWORD state[4], rel;
    unsigned char payload[100], big_payload[0x4001];
    char path[MAX_PATH];
    unsigned i;

    setvbuf(stdout, NULL, _IONBF, 0);
    /* 1. cave relocation */
    memcpy(code, bc2_cave_code, BC2_CAVE_CODE_LEN);
    bc2_recorder_selftest_apply_fixups(code, BC2_CAVE_TEST_RING, BC2_CAVE_TEST_CODE);
    check(sha256(code, BC2_CAVE_CODE_LEN, hash) && memcmp(hash, bc2_cave_test_sha256, 32) == 0,
          "fixups: relocated cave SHA-256 == bc2_cave_test_sha256");
    memcpy(code, bc2_cave_code, BC2_CAVE_CODE_LEN);
    bc2_recorder_selftest_apply_fixups(code, BC2_CAVE_REF_RING, BC2_CAVE_REF_CODE);
    check(memcmp(code, bc2_cave_code, BC2_CAVE_CODE_LEN) == 0, "fixups: identity at the reference pair");
    bc2_recorder_selftest_jmp_bytes(0x0065642Fu, 0x10001000u, jb);
    rel = 0x10001000u - 0x00656434u;
    check(jb[0] == 0xe9 && memcmp(jb + 1, &rel, 4) == 0, "jmp bytes: e9 + (target - (site + 5))");

    /* 1b. r302 per-build profiles: retail = byte-identical, Steam = 5 literals + resume rel32 retargeted */
    memcpy(code, bc2_cave_code, BC2_CAVE_CODE_LEN);
    bc2_recorder_selftest_apply_fixups(code, BC2_CAVE_TEST_RING, BC2_CAVE_TEST_CODE);
    check(bc2_recorder_selftest_cave_apply_profile(code, BC2_CAVE_TEST_CODE, "retail")
              && sha256(code, BC2_CAVE_CODE_LEN, hash) && memcmp(hash, bc2_cave_test_sha256, 32) == 0,
          "profile retail: relocated cave unchanged (SHA == bc2_cave_test_sha256)");
    memcpy(code, bc2_cave_code, BC2_CAVE_CODE_LEN);
    bc2_recorder_selftest_apply_fixups(code, BC2_CAVE_TEST_RING, BC2_CAVE_TEST_CODE);
    {
        static const struct { unsigned off; DWORD v; } STEAM_LIT[5] = {
            { 356, 0x0140E8D8u }, { 676, 0x015724B4u }, { 693, 0x01572458u }, { 699, 0x01434098u }, { 711, 0x01572460u } };
        unsigned char ref[BC2_CAVE_CODE_LEN];
        DWORD v, off = bc2_cave_code_fixups[0];
        int lit_ok = 1, rest_ok = 1;
        unsigned k;
        memcpy(ref, code, BC2_CAVE_CODE_LEN);
        check(bc2_recorder_selftest_cave_apply_profile(code, BC2_CAVE_TEST_CODE, "steam"), "profile steam: cave layout accepted");
        for (k = 0; k < 5; k++) {
            memcpy(&v, code + STEAM_LIT[k].off, 4);
            lit_ok &= v == STEAM_LIT[k].v;
            memcpy(ref + STEAM_LIT[k].off, &STEAM_LIT[k].v, 4);
        }
        check(lit_ok, "profile steam: the 5 cave literals hold the r301 Steam values");
        memcpy(&v, code + off, 4);
        check(code[off - 1] == 0xe9 && BC2_CAVE_TEST_CODE + off + 4 + v == 0x009AD38Du, "profile steam: cave jmp resumes at 0x9AD38D");
        memcpy(ref + off, &v, 4);
        rest_ok = memcmp(ref, code, BC2_CAVE_CODE_LEN) == 0;
        check(rest_ok, "profile steam: no other cave byte changed");
        check(!bc2_recorder_selftest_cave_apply_profile(code, BC2_CAVE_TEST_CODE, "steam"),
              "profile steam: applying twice is refused (literal check)");
    }
    check(bc2_recorder_selftest_use_profile("steam") && bc2_recorder_selftest_vt_connection() == 0x0141AE18u
              && bc2_tgt_ctor == 0x00560130u && bc2_tgt_connect_ret == 0x0065DFC4u && bc2_tgt_getter == 0x00806BF0u
              && bc2_tgt_destroy_ret == 0x00645180u, "profile steam: C-side targets + vtable switched");
    check(bc2_recorder_selftest_use_profile("retail") && bc2_recorder_selftest_vt_connection() == 0x014182A8u
              && bc2_tgt_ctor == 0x005585A0u && bc2_tgt_connect_ret == 0x00656434u, "profile retail restored");

    /* ring */
    ring_mem = VirtualAlloc(NULL, BC2_RING_HDR + BC2_RING_SIZE, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    check(ring_mem && bc2_recorder_selftest_init(ring_mem), "ring: 4 MB ring initialised");
    fake_conn_init();

    /* 2a. connect stub: ctor returns 0 -> no S, slot untouched */
    bc2_tgt_ctor = (DWORD)(UINT_PTR)fake_ctor;
    bc2_tgt_connect_ret = (DWORD)(UINT_PTR)drive_cont;
    p_stub = bc2_recorder_selftest_stub_connect();
    ctor_result = 0;
    drive_connect();
    check(drv_eax == 0 && drv_esp_before == drv_esp_after, "connect stub: ctor -> 0, eax 0, esp balanced");
    check(seen_ecx == 0x1234 && seen_args[0] == 1 && seen_args[1] == 2 && seen_args[2] == 3 && seen_args[3] == 4
          && seen_args[4] == 5, "connect stub: ctor saw ecx and the 5 args in order");
    check(bc2_recorder_selftest_conn() == 0 && drain_all() == 0, "connect stub: no connection, no record");

    /* 2b. connect stub: ctor returns the fake connection */
    ctor_result = (DWORD)(UINT_PTR)fake_conn;
    memset(seen_args, 0, sizeof(seen_args));
    BC2_RING_H(bc2_recorder_selftest_ring(), 13) = 0xdead;  /* r291: a stale config key from an earlier connection */
    BC2_RING_H(bc2_recorder_selftest_ring(), 17) = 0xbeef;
    drive_connect();
    check(BC2_RING_H(bc2_recorder_selftest_ring(), 13) == 0 && BC2_RING_H(bc2_recorder_selftest_ring(), 17) == 0,
          "connect: ring header last_c (words 13..17) cleared, so the cave re-emits the C record");
    check(drv_eax == ctor_result && drv_esp_before == drv_esp_after, "connect stub: eax == conn after return, esp balanced");
    check(seen_args[0] == 1 && seen_args[4] == 5, "connect stub: args intact on the second call");
    check(*(DWORD *)(fake_conn + 0x1BC) == (DWORD)(UINT_PTR)bc2_recorder_selftest_cb(), "connect: [conn+0x1BC] == &g_cb");
    check(bc2_recorder_selftest_conn() == ctor_result, "connect: current connection set");
    drain_all();
    check(record_count() == 1 && record(0, &type, &body, &len) && type == BC2_T_S, "connect: exactly one S record");
    if (record(0, &type, &body, &len) && type == BC2_T_S) {
        unsigned name_len = u32at(body, 32);
        check(u32at(body, 0) == 1 && (u32at(body, 4) & BC2_S_LEVEL_VALID) && !(u32at(body, 4) & BC2_S_NO_OBSERVER),
              "S: ver 1, level valid, observer installed");
        check(u32at(body, 8) == ctor_result && u32at(body, 12) == 0x0147D218u && u32at(body, 16) == 13,
              "S: conn, dispatcher vt direct, ghost_bits 13");
        check(u32at(body, 28) == fake_level_ptr && name_len == sizeof(LEVEL_NAME) - 1
              && memcmp(body + BC2_S_HEAD_LEN, LEVEL_NAME, name_len) == 0 && len == BC2_S_HEAD_LEN + ((name_len + 3) & ~3u),
              "S: level ptr + name, packed length");
    }

    /* 2c. connect again with a non-zero slot -> observer NOT installed flag */
    *(DWORD *)(fake_conn + 0x1BC) = 0xDEADBEEF;
    drive_connect();
    drain_all();
    check(*(DWORD *)(fake_conn + 0x1BC) == 0xDEADBEEF && record(0, &type, &body, &len) && type == BC2_T_S
          && (u32at(body, 4) & BC2_S_NO_OBSERVER), "connect: foreign slot kept, S flags bit2 set");
    *(DWORD *)(fake_conn + 0x1BC) = 0;
    drive_connect();
    drain_all();

    /* 3. observer */
    for (i = 0; i < sizeof(payload); i++) payload[i] = (unsigned char)(i * 7 + 3);
    state[0] = 0; state[1] = (DWORD)(UINT_PTR)payload; state[2] = 777; state[3] = (DWORD)(UINT_PTR)(payload + sizeof(payload));
    obs_cb = bc2_recorder_selftest_cb(); obs_state = state;
    drive_observer();
    check(drv_esp_before == drv_esp_after, "observer: ret 4 (esp balanced through the __thiscall call)");
    drain_all();
    check(record_count() == 1 && record(0, &type, &body, &len) && type == BC2_T_Q, "observer: exactly one Q record");
    if (record(0, &type, &body, &len) && type == BC2_T_Q) {
        check(u32at(body, 12) == 1 && u32at(body, 16) == state[1] && u32at(body, 20) == 777 && u32at(body, 28) == 100
              && u32at(body, 24) == u32at(payload, 0), "Q head: qseq 1, buffer, limit, span 100, first dword");
        check(len == 32 + 100 && memcmp(body + 32, payload, 100) == 0, "Q body: payload copied, length 32 + 100");
        check(u32at(body, 0) == u32at(body, 8) || u32at(body, 0) + 1 == u32at(body, 8), "Q time: hi1/hi2 consistent");
    }
    check(BC2_RING_H(bc2_recorder_selftest_ring(), BC2_RW_SEQ_Q) == 1, "observer: header word 22 (seq_q) == 1");
    /* refused shapes */
    state[3] = (DWORD)(UINT_PTR)(big_payload + sizeof(big_payload)); state[1] = (DWORD)(UINT_PTR)big_payload;
    drive_observer();
    state[1] = (DWORD)(UINT_PTR)payload; state[3] = (DWORD)(UINT_PTR)(payload + 100); state[2] = 801;
    drive_observer();
    drain_all();
    check(record_count() == 0 && bc2_recorder_selftest_qskip() == 2, "observer: span > 0x4000 and limit > 8*span refused");
    /* ghost_bits change -> Q then S */
    state[2] = 777;
    *(DWORD *)(fake_conn + 0x12CC + 0x244) = 10;
    drive_observer();
    drain_all();
    check(record_count() == 2 && record(1, &type, &body, &len) && type == BC2_T_S && u32at(body, 16) == 10
          && u32at(body, 20) == 2, "observer: ghost_bits change -> extra S (bits 10, qseq 2)");
    /* unreadable readerState -> nothing, no crash */
    obs_state = (void *)(UINT_PTR)0x10;
    drive_observer();
    drain_all();
    check(record_count() == 0 && drv_esp_before == drv_esp_after, "observer: unreadable state ignored");
    obs_state = state;

    /* 4. destroy stub */
    bc2_tgt_getter = (DWORD)(UINT_PTR)fake_getter;
    bc2_tgt_destroy_ret = (DWORD)(UINT_PTR)drive_cont;
    p_stub = bc2_recorder_selftest_stub_destroy();
    drive_destroy();
    check(drv_eax == 0x0BEEF123 && drv_esp_before == drv_esp_after, "destroy stub: getter's eax preserved, esp balanced");
    check(bc2_recorder_selftest_conn() == 0, "destroy: current connection cleared");
    drain_all();
    check(record_count() == 1 && record(0, &type, &body, &len) && type == BC2_T_E && u32at(body, 0) == BC2_E_DESTROY
          && len == BC2_E_BODY_LEN && u32at(body, 12) == 2, "destroy: one E record (reason 0, qseq 2)");
    drive_destroy();
    drain_all();
    check(record_count() == 0 && drv_eax == 0x0BEEF123, "destroy: second call without a connection writes nothing");
    /* observer after destroy: no connection -> ignored */
    drive_observer();
    drain_all();
    check(record_count() == 0, "observer: ignored without a connection");

    /* 5. writer pass: connect + 2 packets + destroy -> one file with S Q Q E */
    p_stub = bc2_recorder_selftest_stub_connect();
    drive_connect();
    drive_observer();
    drive_observer();
    p_stub = bc2_recorder_selftest_stub_destroy();
    drive_destroy();
    n = bc2_recorder_selftest_drain_once(outdir, path);
    check(n > 0 && path[0], "writer: drained into a capture file");
    if (path[0]) {
        HANDLE f = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, 0, NULL);
        unsigned char hdr[128];
        DWORD got = 0;
        check(f != INVALID_HANDLE_VALUE && ReadFile(f, hdr, 128, &got, NULL) && got == 128
              && memcmp(hdr, "BC2RAW\0\0", 8) == 0 && hdr[8] == 1 && u32at(hdr, 80) == BC2_RING_SIZE,
              "writer: file header magic/format/ring_size");
        if (f != INVALID_HANDLE_VALUE) {
            drained_len = 0;
            ReadFile(f, drained, sizeof(drained), &got, NULL);
            drained_len = got;
            check(got == n && record_count() == 4 && record(0, &type, &body, &len) && type == BC2_T_S
                  && record(3, &type, &body, &len) && type == BC2_T_E, "writer: file body = S Q Q E, closed at E");
            CloseHandle(f);
        }
        DeleteFileA(path);
    }

    printf(fails ? "%d check(s) FAILED\n" : "all checks passed\n", fails);
    return fails ? 1 : 0;
}
