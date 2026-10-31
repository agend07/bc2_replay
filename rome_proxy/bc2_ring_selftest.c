/* Offline check of bc2_ring.c and bc2_capture.c (no game, no network). One line per check, exit 0 = pass.
 * Usage: bc2_ring_selftest.exe [outdir]   (default out_shared; leaves <outdir>\selftest.bc2raw)
 *
 * Body pattern used everywhere below: pat(len)[i] = (i + len) & 0xFF.
 *
 * 1. Scripted ring sequence (replayable with recorder_v3.Ring(4096): ring.write(1, pat(n)), ring.drain()):
 *    size 4096; totals = pad4(4 + len); type 1 (P) for every write.
 *      write 100  -> total  104, prod  104
 *      write 1000 -> total 1004, prod 1108
 *      write 2000 -> total 2004, prod 3112
 *      write 1500 -> total 1504: tail = 4096 - 3112 = 984 < 1504 -> extra 984;
 *                    (3112 - 0) + 984 + 1504 = 5600 > 4096 -> DROP, drops 1, prod stays 3112
 *      drain      -> 3112 bytes (3 records, head + body + zero pad), cons 3112, data area all zero
 *      write 1500 -> extra 984: PAD head 0xFF | 984 << 8 = 0x0003D8FF at data[3112]; record at data[0];
 *                    (3112 - 3112) + 984 + 1504 = 2488 <= 4096 -> prod 5600
 *      write 200  -> total 204: tail = 4096 - 5600 % 4096 = 2592, fits -> prod 5804
 *      drain      -> PAD consumed, 1504 + 204 = 1708 bytes copied, cons 5804, data area all zero
 *    final header: prod 5804, cons 5804, drops 1.
 *    Then (same ring): abandon -> PAD skipped; unpublished head stops the drain; cap smaller than a record
 *    stops before it; a corrupt head sets *corrupt; drops saturate at 0xFFFFFFFF; lock / contention.
 * 2. Ring of BC2_RING_SIZE: first 12 header bytes 00 52 56 33 01 00 00 00 00 00 40 00, rest zero.
 * 3. Capture: S C P G G2 M Q T E written through the real-size ring, drained, appended to a .bc2raw, re-read
 *    and compared. Record values (fixed; only the header GUID / FILETIME / InterruptTime vary):
 *      S  flags 1, conn 0x11223344, dispatcher_vt 0x0140D8C8, ghost_bits 10, qseq 0, pseq 0,
 *         level_ptr 0x0A0B0C00, FILETIME 0x01DB000000000000, InterruptTime 0x100000000, name "levels/mp_005"
 *      C  n0 2, n1 1, child_raw pat(32), tables0 (10, 20), tables1 (30)
 *      P  t (1, 0x1000, 1), seq 1, buffer 0x0BAD0000, limit 800, first_dword = u32(pat(100)[0:4]), span 100, pat(100)
 *      G  t (1, 0x1000, 1), pseq 1, id 7, phase 0, ghost_vt 0x14378F0, section_vt 0x14377F8, entry_cursor 40,
 *         cursor 64, cache 0, dword_off 2, origin (1, 2, 3), create_cur 0
 *      G2 as G but id 8, section_vt 0 (multi-section ghost)
 *      M  n 2, (0x1111, 0x2222)   (belongs to G2)
 *    recorder_v3.decode of the records: 1 packet, 1 ghost, 1 multi (sections 0x1111, 0x2222), 1 config,
 *    0 orphans, 1 session, 1 sync, 1 end, 1 raw.
 *      Q  t (1, 0x1004, 1), qseq 1, buffer 0x0BAD0000, limit 800, first_dword = u32(pat(100)[0:4]), span 100, pat(100)
 *      T  InterruptTime 0x100000005, FILETIME 0x01DB000000000005, qseq 1, pseq 1, prod 0x1234, drops 0,
 *         contention 0, skip 0
 *      E  reason 0, InterruptTime 0x100000006, qseq 1, pseq 1
 *    Header: exe_sha256 = bytes 0..31, dll_version 0x00010002, ring_size BC2_RING_SIZE.
 */
#include <windows.h>
#include <stdio.h>
#include <string.h>
#include "bc2_ring.h"
#include "bc2_capture.h"

static int fails;
#define CHECK(c, what) do { if (c) printf("ok   %s\n", what); else { printf("FAIL %s\n", what); fails++; } } while (0)

static unsigned char g_buf[1 << 16];
static unsigned char g_exp[1 << 16];

static void pat(unsigned char *p, unsigned len)
{
    unsigned i;
    for (i = 0; i < len; i++)
        p[i] = (unsigned char)(i + len);
}

static void put32(unsigned char *p, unsigned v)
{
    memcpy(p, &v, 4);
}

static unsigned get32(const unsigned char *p)
{
    unsigned v;
    memcpy(&v, p, 4);
    return v;
}

/* expected record bytes for type, pat(len) body: head + body + zero pad -> returns total */
static unsigned exp_rec(unsigned char *dst, unsigned type, unsigned len)
{
    unsigned total = (4 + len + 3) & ~3u;
    memset(dst, 0, total);
    put32(dst, type | total << 8);
    pat(dst + 4, len);
    return total;
}

static int write_pat(bc2_ring *r, unsigned len)
{
    static unsigned char body[4096];
    pat(body, len);
    return bc2_ring_write(r, BC2_T_P, body, len);
}

static int data_zero(bc2_ring *r)
{
    unsigned i;
    for (i = 0; i < r->size; i++)
        if (BC2_RING_DATA(r)[i])
            return 0;
    return 1;
}

static void test_scripted(void)
{
    static unsigned char mem[BC2_RING_HDR + 4096];
    bc2_ring r;
    unsigned n, e, total, saved;
    int corrupt;
    unsigned char *rec;
    char line[128];

    CHECK(bc2_ring_init(&r, mem, 4096) == 1, "ring 4096: init");
    CHECK(bc2_ring_init(&r, mem, 4092) == 0 && bc2_ring_init(&r, mem, 256) == 0, "ring: init rejects non power of two / <= 256");
    bc2_ring_init(&r, mem, 4096);

    CHECK(write_pat(&r, 100) && BC2_RING_H(&r, BC2_RW_PROD) == 104, "w100 -> prod 104");
    CHECK(write_pat(&r, 1000) && BC2_RING_H(&r, BC2_RW_PROD) == 1108, "w1000 -> prod 1108");
    CHECK(write_pat(&r, 2000) && BC2_RING_H(&r, BC2_RW_PROD) == 3112, "w2000 -> prod 3112");
    CHECK(write_pat(&r, 1500) == 0 && BC2_RING_H(&r, BC2_RW_PROD) == 3112 && BC2_RING_H(&r, BC2_RW_DROPS) == 1,
          "w1500 dropped: prod 3112, drops 1");
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    e = exp_rec(g_exp, 1, 100);
    e += exp_rec(g_exp + e, 1, 1000);
    e += exp_rec(g_exp + e, 1, 2000);
    wsprintfA(line, "drain 1: %u bytes (expect 3112), cons %u", n, BC2_RING_H(&r, BC2_RW_CONS));
    CHECK(n == 3112 && e == 3112 && !corrupt && BC2_RING_H(&r, BC2_RW_CONS) == 3112, line);
    CHECK(memcmp(g_buf, g_exp, e) == 0, "drain 1: bytes = heads + pat bodies");
    CHECK(data_zero(&r), "drain 1: data area zeroed");

    CHECK(write_pat(&r, 1500) && BC2_RING_H(&r, BC2_RW_PROD) == 5600, "w1500 -> PAD 984 + record at 0, prod 5600");
    CHECK(get32(BC2_RING_DATA(&r) + 3112) == 0x0003D8FFu, "PAD head 0x0003D8FF at data[3112]");
    CHECK(get32(BC2_RING_DATA(&r)) == (1u | 1504u << 8), "record head at data[0]");
    CHECK(write_pat(&r, 200) && BC2_RING_H(&r, BC2_RW_PROD) == 5804, "w200 -> prod 5804");
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    e = exp_rec(g_exp, 1, 1500);
    e += exp_rec(g_exp + e, 1, 200);
    wsprintfA(line, "drain 2: %u bytes (expect 1708), PAD stripped", n);
    CHECK(n == 1708 && e == 1708 && !corrupt && memcmp(g_buf, g_exp, e) == 0, line);
    CHECK(BC2_RING_H(&r, BC2_RW_PROD) == 5804 && BC2_RING_H(&r, BC2_RW_CONS) == 5804 && BC2_RING_H(&r, BC2_RW_DROPS) == 1,
          "after drain 2: prod 5804, cons 5804, drops 1");
    CHECK(data_zero(&r), "drain 2: data area zeroed");

    /* abandon -> PAD skipped */
    rec = bc2_ring_reserve(&r, 64);
    memset(rec + 4, 0xCC, 60);
    bc2_ring_abandon(rec, 64);
    write_pat(&r, 8);
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    e = exp_rec(g_exp, 1, 8);
    CHECK(n == 12 && memcmp(g_buf, g_exp, 12) == 0 && BC2_RING_H(&r, BC2_RW_CONS) == 5804 + 64 + 12 && data_zero(&r),
          "abandon: PAD skipped, next record drained, bytes zeroed");

    /* unpublished head stops the drain */
    saved = BC2_RING_H(&r, BC2_RW_CONS);
    rec = bc2_ring_reserve(&r, 16);
    write_pat(&r, 4);
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    CHECK(n == 0 && !corrupt && BC2_RING_H(&r, BC2_RW_CONS) == saved, "unpublished head: drain stops, cons unchanged");
    pat(rec + 4, 12);
    bc2_ring_publish(rec, BC2_T_P, 16);
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    e = exp_rec(g_exp, 1, 12);
    e += exp_rec(g_exp + e, 1, 4);
    CHECK(n == 24 && e == 24 && memcmp(g_buf, g_exp, e) == 0 && BC2_RING_H(&r, BC2_RW_CONS) == saved + 24,
          "after publish: both records drained in order");

    /* cap smaller than a record: PAD consumed, stop before the record that does not fit */
    write_pat(&r, 20);                        /* total 24 */
    rec = bc2_ring_reserve(&r, 8);
    bc2_ring_abandon(rec, 8);                 /* PAD 8 */
    write_pat(&r, 100);                       /* total 104 */
    saved = BC2_RING_H(&r, BC2_RW_CONS);
    n = bc2_ring_drain(&r, g_buf, 50, &corrupt);
    CHECK(n == 24 && !corrupt && BC2_RING_H(&r, BC2_RW_CONS) == saved + 24 + 8,
          "cap 50: first record copied, PAD consumed, cons at the 104-byte record");
    n = bc2_ring_drain(&r, g_buf, 103, &corrupt);
    CHECK(n == 0 && BC2_RING_H(&r, BC2_RW_CONS) == saved + 32, "cap 103 < 104: nothing copied, cons unchanged");
    n = bc2_ring_drain(&r, g_buf, 104, &corrupt);
    CHECK(n == 104 && BC2_RING_H(&r, BC2_RW_CONS) == BC2_RING_H(&r, BC2_RW_PROD) && data_zero(&r), "cap 104: record drained");

    /* corrupt heads */
    saved = BC2_RING_H(&r, BC2_RW_CONS);
    rec = bc2_ring_reserve(&r, 16);
    put32(rec, 1u | 6u << 8);                 /* total 6: not 4-aligned */
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    CHECK(n == 0 && corrupt == 1 && BC2_RING_H(&r, BC2_RW_CONS) == saved, "corrupt head (total 6): *corrupt = 1");
    put32(rec, 1u | 64u << 8);                /* total 64 > prod - cons = 16 */
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    CHECK(n == 0 && corrupt == 1, "corrupt head (total > prod - cons): *corrupt = 1");
    bc2_ring_publish(rec, BC2_T_P, 16);
    memset(rec + 4, 0, 12);
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    CHECK(n == 16 && !corrupt, "repaired head drains");
    BC2_RING_H(&r, BC2_RW_PROD) = BC2_RING_H(&r, BC2_RW_CONS) + 4100;
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    CHECK(n == 0 && corrupt == 1, "prod - cons > size: *corrupt = 1");
    BC2_RING_H(&r, BC2_RW_PROD) = BC2_RING_H(&r, BC2_RW_CONS);

    /* drops saturate; bad totals are drops */
    BC2_RING_H(&r, BC2_RW_DROPS) = 0xFFFFFFFEu;
    CHECK(bc2_ring_reserve(&r, 6) == NULL && BC2_RING_H(&r, BC2_RW_DROPS) == 0xFFFFFFFFu, "reserve(6) rejected as drop");
    CHECK(bc2_ring_reserve(&r, 8192) == NULL && BC2_RING_H(&r, BC2_RW_DROPS) == 0xFFFFFFFFu, "drops saturate at 0xFFFFFFFF");

    /* u32 counter wrap (size is a power of two) */
    bc2_ring_init(&r, mem, 4096);
    BC2_RING_H(&r, BC2_RW_PROD) = BC2_RING_H(&r, BC2_RW_CONS) = 0xFFFFFF00u;   /* data offset 3840 */
    total = 0;
    CHECK(write_pat(&r, 400) && BC2_RING_H(&r, BC2_RW_PROD) == 0x00000194u, "wrap: PAD 256 + 404 -> prod 0x194");
    n = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    total = exp_rec(g_exp, 1, 400);
    CHECK(n == total && memcmp(g_buf, g_exp, n) == 0 && BC2_RING_H(&r, BC2_RW_CONS) == 0x194 && data_zero(&r), "wrap: drained");

    /* lock */
    CHECK(bc2_ring_try_lock(&r) == 1, "lock: first try_lock = 1");
    CHECK(bc2_ring_try_lock(&r) == 0 && BC2_RING_H(&r, BC2_RW_CONT) == 1, "lock: second = 0, contention 1");
    bc2_ring_unlock(&r);
    CHECK(BC2_RING_H(&r, BC2_RW_LOCK) == 0 && bc2_ring_try_lock(&r) == 1, "lock: unlock, try_lock = 1");
    bc2_ring_unlock(&r);
}

static void add_rec(bc2_ring *r, unsigned type, const unsigned char *body, unsigned len, const char *what)
{
    char line[96];
    wsprintfA(line, "ring write %s (%u body bytes)", what, len);
    CHECK(bc2_ring_write(r, type, body, len) == 1, line);
}

static unsigned read_all(const char *path, unsigned char *buf, unsigned cap)
{
    HANDLE f = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING, 0, NULL);
    DWORD got = 0;
    if (f == INVALID_HANDLE_VALUE)
        return 0;
    ReadFile(f, buf, cap, &got, NULL);
    CloseHandle(f);
    return got;
}

static void test_capture(const char *outdir)
{
    static const unsigned char init[12] = { 0x00, 0x52, 0x56, 0x33, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40, 0x00 };
    static const char types[9] = { BC2_T_S, BC2_T_C, BC2_T_P, BC2_T_G, BC2_T_G, BC2_T_M, BC2_T_Q, BC2_T_T, BC2_T_E };
    unsigned char *mem, body[512], sha[32], *p;
    unsigned char sbuf[BC2_S_MAX_BODY];
    bc2_ring r;
    bc2_capture f, f2;
    bc2_s_fields s;
    bc2_t_fields t;
    bc2_e_fields e;
    bc2_q_head_fields q;
    unsigned i, n, len, drained, flen, first;
    int corrupt, ok;
    unsigned long long before, it;
    char tmp[MAX_PATH], root[MAX_PATH], dir[MAX_PATH], final_path[MAX_PATH], line[MAX_PATH + 64];
    const char *s1, *s2;

    /* 2. real-size ring */
    mem = (unsigned char *)VirtualAlloc(NULL, BC2_RING_HDR + BC2_RING_SIZE, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    CHECK(mem != NULL && bc2_ring_init(&r, mem, BC2_RING_SIZE) == 1, "ring BC2_RING_SIZE: init");
    ok = memcmp(mem, init, 12) == 0;
    for (i = 12; i < BC2_RING_HDR + BC2_RING_SIZE; i++)
        if (mem[i])
            ok = 0;
    CHECK(ok, "ring BC2_RING_SIZE: header 00 52 56 33 01 00 00 00 00 00 40 00 + zeros");

    /* packers: lengths */
    memset(&s, 0, sizeof(s));
    s.flags = 1; s.conn = 0x11223344; s.dispatcher_vt = 0x0140D8C8; s.ghost_bits = 10; s.level_ptr = 0x0A0B0C00;
    s.filetime = 0x01DB000000000000ull; s.interrupt_time = 0x100000000ull; s.name = "levels/mp_005"; s.name_len = 13;
    CHECK(bc2_pack_s(sbuf, &s) == 52 + 16, "pack_s: 52 + pad4(13) = 68");
    s.name_len = 12;
    CHECK(bc2_pack_s(sbuf, &s) == 52 + 12, "pack_s: 52 + pad4(12) = 64");
    s.name_len = 0;
    CHECK(bc2_pack_s(sbuf, &s) == 52, "pack_s: empty name = 52");
    s.name_len = 13;
    len = bc2_pack_s(sbuf, &s);
    CHECK(get32(sbuf) == 1 && get32(sbuf + 32) == 13 && get32(sbuf + 36) == 0 && get32(sbuf + 40) == 0x01DB0000
          && get32(sbuf + 44) == 0 && get32(sbuf + 48) == 1 && memcmp(sbuf + 52, "levels/mp_005\0\0\0", 16) == 0,
          "pack_s: '<9IQQ' offsets (name_len 32, FILETIME 36, InterruptTime 44, name 52)");
    memset(&t, 0, sizeof(t));
    t.interrupt_time = 0x100000005ull; t.filetime = 0x01DB000000000005ull; t.qseq = 1; t.pseq = 1; t.prod = 0x1234;
    CHECK(bc2_pack_t(body, &t) == 40 && get32(body) == 5 && get32(body + 4) == 1 && get32(body + 8) == 5
          && get32(body + 24) == 0x1234, "pack_t: 40 bytes, '<QQ6I' offsets");
    memset(&e, 0, sizeof(e));
    e.interrupt_time = 0x100000006ull; e.qseq = 1; e.pseq = 1;
    CHECK(bc2_pack_e(body, &e) == 20 && get32(body) == 0 && get32(body + 4) == 6 && get32(body + 8) == 1
          && get32(body + 12) == 1 && get32(body + 16) == 1, "pack_e: 20 bytes, '<IQII' (u64 at offset 4)");

    /* 3. records through the ring */
    add_rec(&r, BC2_T_S, sbuf, len, "S");
    p = body;
    put32(p, 2); put32(p + 4, 1); pat(p + 8, 32); put32(p + 40, 10); put32(p + 44, 20); put32(p + 48, 30);
    add_rec(&r, BC2_T_C, body, 52, "C");
    memset(body, 0, sizeof(body));                       /* P: P_HEAD + pat(100) */
    put32(p, 1); put32(p + 4, 0x1000); put32(p + 8, 1); put32(p + 12, 1); put32(p + 16, 0x0BAD0000);
    put32(p + 20, 800); pat(p + 32, 100); put32(p + 24, get32(p + 32)); put32(p + 28, 100);
    add_rec(&r, BC2_T_P, body, 132, "P");
    memset(body, 0, sizeof(body));                       /* G: '<IIIIHHIIIIII3II' = 60 bytes */
    put32(p, 1); put32(p + 4, 0x1000); put32(p + 8, 1); put32(p + 12, 1);
    p[16] = 7; p[17] = 0; p[18] = 0; p[19] = 0;          /* id u16 7, phase u16 0 */
    put32(p + 20, 0x14378F0); put32(p + 24, 0x14377F8); put32(p + 28, 40); put32(p + 32, 64); put32(p + 36, 0);
    put32(p + 40, 2); put32(p + 44, 1); put32(p + 48, 2); put32(p + 52, 3); put32(p + 56, 0);
    add_rec(&r, BC2_T_G, body, 60, "G");
    p[16] = 8; put32(p + 24, 0);                         /* G2: id 8, section_vt 0 -> M follows */
    add_rec(&r, BC2_T_G, body, 60, "G2");
    put32(p, 2); put32(p + 4, 0x1111); put32(p + 8, 0x2222);
    add_rec(&r, BC2_T_M, body, 12, "M");
    q.t_hi1 = 1; q.t_lo = 0x1004; q.t_hi2 = 1; q.qseq = 1; q.buffer = 0x0BAD0000; q.limit = 800; q.span = 100;
    pat(body + 32, 100);
    q.first_dword = get32(body + 32);
    CHECK(bc2_pack_q_head(body, &q) == 32 && get32(body + 4) == 0x1004 && get32(body + 28) == 100, "pack_q_head: 32 bytes");
    add_rec(&r, BC2_T_Q, body, 132, "Q");
    add_rec(&r, BC2_T_T, body, bc2_pack_t(body, &t), "T");
    add_rec(&r, BC2_T_E, body, bc2_pack_e(body, &e), "E");
    drained = bc2_ring_drain(&r, g_buf, sizeof(g_buf), &corrupt);
    wsprintfA(line, "drain: %u bytes, prod %u, cons %u", drained, BC2_RING_H(&r, BC2_RW_PROD), BC2_RING_H(&r, BC2_RW_CONS));
    CHECK(drained == BC2_RING_H(&r, BC2_RW_PROD) && !corrupt && BC2_RING_H(&r, BC2_RW_CONS) == drained, line);

    /* capture dir under a private LOCALAPPDATA */
    GetTempPathA(sizeof(tmp), tmp);
    wsprintfA(root, "%sbc2_ring_selftest_%lu", tmp, GetCurrentProcessId());
    CreateDirectoryA(root, NULL);
    SetEnvironmentVariableA("LOCALAPPDATA", root);
    CHECK(bc2_capture_dir(dir, sizeof(dir), 1) == 1 && GetFileAttributesA(dir) != INVALID_FILE_ATTRIBUTES
          && lstrlenA(dir) > 19 && lstrcmpA(dir + lstrlenA(dir) - 19, "\\BC2Replay\\captures") == 0,
          "capture_dir: <LOCALAPPDATA>\\BC2Replay\\captures created");

    /* name collision -> _2 */
    for (i = 0; i < 32; i++)
        sha[i] = (unsigned char)i;
    CHECK(bc2_capture_open(&f, dir, sha, 0x00010002, BC2_RING_SIZE) == 1, "capture_open #1");
    CHECK(bc2_capture_open(&f2, dir, sha, 0x00010002, BC2_RING_SIZE) == 1, "capture_open #2");
    s1 = f.path + lstrlenA(dir) + 1;
    s2 = f2.path + lstrlenA(dir) + 1;
    wsprintfA(line, "names: %s, %s", s1, s2);
    if (memcmp(s1, s2, 15) == 0)
        CHECK(lstrlenA(s2) == lstrlenA(s1) + 2 && lstrcmpA(s2 + lstrlenA(s2) - 9, "_2.bc2raw") == 0, line);
    else
        printf("skip %s (second boundary)\n", line);
    bc2_capture_close(&f2);
    DeleteFileA(f2.path);

    before = bc2_interrupt_time();
    CHECK(bc2_capture_append(&f, g_buf, drained) == 1 && f.bytes == BC2_CAPTURE_HDR + drained, "capture_append");
    bc2_capture_flush(&f);
    bc2_capture_close(&f);
    bc2_capture_close(&f);
    wsprintfA(final_path, "%s\\selftest.bc2raw", outdir);
    CHECK(MoveFileExA(f.path, final_path, MOVEFILE_REPLACE_EXISTING | MOVEFILE_COPY_ALLOWED) != 0, "moved to <outdir>\\selftest.bc2raw");
    RemoveDirectoryA(dir);
    lstrcpyA(dir + lstrlenA(dir) - 9, "");
    RemoveDirectoryA(dir);
    RemoveDirectoryA(root);

    /* re-read */
    flen = read_all(final_path, g_exp, sizeof(g_exp));
    CHECK(flen == BC2_CAPTURE_HDR + drained, "re-read: size = 128 + drained");
    CHECK(memcmp(g_exp, "BC2RAW\0\0", 8) == 0 && g_exp[8] == 1 && g_exp[9] == 0 && g_exp[10] == 0 && g_exp[11] == 0,
          "header: magic, format 1, flags 0");
    CHECK(memcmp(g_exp + 12, sha, 32) == 0 && get32(g_exp + 44) == 0x00010002, "header: exe_sha256, dll_version");
    CHECK((g_exp[55] & 0xF0) == 0x40 && (g_exp[56] & 0xC0) == 0x80, "header: session guid v4");
    it = (unsigned long long)get32(g_exp + 76) << 32 | get32(g_exp + 72);
    CHECK(it != 0 && it <= before && get32(g_exp + 68) >= 0x01D00000, "header: InterruptTime <= now, FILETIME plausible");
    CHECK(get32(g_exp + 80) == BC2_RING_SIZE, "header: ring_size");
    ok = 1;
    for (i = 84; i < BC2_CAPTURE_HDR; i++)
        if (g_exp[i])
            ok = 0;
    CHECK(ok, "header: 84..127 zero");
    CHECK(memcmp(g_exp + BC2_CAPTURE_HDR, g_buf, drained) == 0, "records: verbatim drained bytes");
    p = g_exp + BC2_CAPTURE_HDR;
    ok = 1;
    for (i = 0, n = 0; n < drained && i < 9; i++) {
        unsigned head = get32(p + n);
        if ((head & 0xFF) != (unsigned)types[i] || (head >> 8) < 4 || ((head >> 8) & 3))
            ok = 0;
        n += head >> 8;
    }
    CHECK(ok && i == 9 && n == drained, "records: S C P G G2 M Q T E parse back");
    CHECK(get32(p) == (BC2_T_S | 72u << 8) && memcmp(p + 4 + 52, "levels/mp_005", 13) == 0, "records: S head 0x4806, name");

    /* split point */
    first = bc2_capture_split_point(g_buf, drained);
    CHECK(first == drained, "split_point: E last -> len");
    n = 0;
    n += (unsigned)(4 + 68);   memcpy(g_exp, g_buf, n);                               /* S */
    put32(g_exp + n, BC2_T_E | 24u << 8); bc2_pack_e(g_exp + n + 4, &e); n += 24;      /* E */
    first = n;
    put32(g_exp + n, BC2_T_T | 44u << 8); bc2_pack_t(g_exp + n + 4, &t); n += 44;      /* T */
    put32(g_exp + n, BC2_T_E | 24u << 8); bc2_pack_e(g_exp + n + 4, &e); n += 24;      /* E */
    wsprintfA(line, "split_point: S E | T E -> %u (expect %u)", bc2_capture_split_point(g_exp, n), first);
    CHECK(bc2_capture_split_point(g_exp, n) == first, line);
    CHECK(bc2_capture_split_point(g_exp, 72) == 72 && bc2_capture_split_point(g_exp, 3) == 3, "split_point: no E -> len");
    VirtualFree(mem, 0, MEM_RELEASE);
}

int main(int argc, char **argv)
{
    const char *outdir = argc > 1 ? argv[1] : "out_shared";

    CreateDirectoryA(outdir, NULL);
    test_scripted();
    test_capture(outdir);
    printf(fails ? "FAILED: %d check(s)\n" : "all checks passed\n", fails);
    return fails ? 1 : 0;
}
