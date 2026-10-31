/* Offline selftest of the chat command layer (client_chat.c built with -DBC2_CHAT_SELFTEST: no hooks, no game).
 * Checks the slash-command parser, the mute list round trip through a temp file, /rec without a recorder and the
 * /smoke stub. Usage: client_chat_selftest.exe [outdir] (default: the exe's folder); exit 0 = all checks passed. */
#include <windows.h>
#include <stdio.h>
#include <string.h>
#include "client_chat.h"
#ifndef BC2_CHAT_STANDALONE
#include "client_recorder.h"
#endif
#include "client_smoke.h"
#include "client_smoke_hook.h"
#include "client_fx.h"
#include "bc2_exe.h"

void bc2_chat_selftest_init(void);

#ifndef BC2_CHAT_STANDALONE
/* recorder stand-ins (the real ones live in client_recorder.c, not linked here) */
void bc2_client_recorder_status(char *out, int n) { lstrcpynA(out, "rec: not installed (selftest)", n); }
int bc2_client_recorder_pause(int p) { (void)p; return 0; }
#endif

static int fails;
static void check(int ok, const char *what)
{
    printf("%s %s\n", ok ? "ok  " : "FAIL", what);
    if (!ok) fails++;
}

int main(int argc, char **argv)
{
    char path[MAX_PATH], out[384], exedir[MAX_PATH];
    const char *dir = argc > 1 ? argv[1] : exedir;
    float rgb_h[3];
    FILE *f;
    DWORD attr, len = GetModuleFileNameA(NULL, exedir, MAX_PATH);
    /* default = the exe's own folder, not a cwd-relative "out_shared": from any other cwd the temp mute/hash
     * files could not be created and the 11 list checks failed */
    while (len && exedir[len - 1] != '\\') len--;
    exedir[len ? len - 1 : 0] = 0;
    if (!exedir[0]) lstrcpyA(exedir, ".");
    attr = GetFileAttributesA(dir);
    if (attr == INVALID_FILE_ATTRIBUTES || !(attr & FILE_ATTRIBUTE_DIRECTORY)) {
        printf("FAIL temp dir '%s' does not exist\n", dir);
        return 2;
    }
    bc2_chat_selftest_init();
    wsprintfA(path, "%s\\selftest_muted.txt", dir);
    DeleteFileA(path);
    bc2_chat_set_mute_file(path);

    check(bc2_chat_command("hello", out, sizeof(out)) == 0, "plain text is not a command");
    check(bc2_chat_command("/vote yes", out, sizeof(out)) == 0, "unknown slash text passes through");
    check(bc2_chat_command("/muted2", out, sizeof(out)) == 0, "prefix without separator is not a command");
    check(bc2_chat_command("  /help", out, sizeof(out)) == 1 && strstr(out, "/smoke"), "/help lists commands");

    check(bc2_chat_command("/mute", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/mute without arg = usage");
    check(bc2_chat_command("/mute  Bob ", out, sizeof(out)) == 1 && strcmp(out, "Muted: Bob") == 0, "/mute Bob");
    check(bc2_chat_command("/MUTE bob", out, sizeof(out)) == 1 && strstr(out, "Already"), "/mute is case-insensitive");
    check(bc2_chat_command("/mute spam", out, sizeof(out)) == 1, "/mute spam");
    check(bc2_chat_is_muted("BobbyTables", NULL), "sender substring match");
    check(bc2_chat_is_muted("Alice", "SPAM!"), "first-word substring match");
    check(!bc2_chat_is_muted("Alice", "hi"), "unmuted sender passes");
    check(bc2_chat_command("/mutelist", out, sizeof(out)) == 1 && strstr(out, "Muted (2): Bob, spam"), "/mutelist");
    check(bc2_chat_command("/mute Bob", out, sizeof(out)) == 1 && strcmp(out, "Already Muted: Bob") == 0, "/mute duplicate wording");
    if (fopen_s(&f, path, "r") != 0) f = NULL;
    check(f != NULL, "mute file written");
    if (f) {
        int lines = 0, seen = 0;
        char line[256];
        while (fgets(line, sizeof(line), f)) { lines++; if (strncmp(line, "Bob", 3) == 0) seen = 1; }
        fclose(f);
        check(lines == 4 && seen, "mute file = 2 header lines + 2 entries");
    }
    check(bc2_chat_command("/unmute bob", out, sizeof(out)) == 1 && strstr(out, "Unmuted: bob (1)"), "/unmute bob");
    check(!bc2_chat_is_muted("Bob", NULL), "Bob unmuted");
    check(bc2_chat_command("/unmute nobody", out, sizeof(out)) == 1 && strstr(out, "Not in"), "/unmute unknown");

    /* hot reload: edit the file by hand */
    if (fopen_s(&f, path, "w") == 0 && f) { fprintf(f, "# x\nCarol\n"); fclose(f); }
    bc2_chat_set_mute_file(path);  /* resets the reload clock + mtime (the real thread waits 2 s) */
    check(bc2_chat_is_muted("carol99", NULL) && !bc2_chat_is_muted("spam", NULL), "file edit reloaded");

    /* /hash list + transform */
    wsprintfA(path, "%s\\selftest_hashed.txt", dir);
    DeleteFileA(path);
    bc2_chat_set_hash_file(path);
    check(bc2_chat_command("/hash Dave", out, sizeof(out)) == 1 && strcmp(out, "Hashed: Dave") == 0, "/hash Dave");
    check(bc2_chat_is_hashed("xDAVEx") && !bc2_chat_is_hashed("Eve"), "hashed membership");
    check(bc2_chat_command("/hashlist", out, sizeof(out)) == 1 && strstr(out, "Hashed (1): Dave"), "/hashlist");
    check(!bc2_chat_is_muted("Dave", NULL), "hash list is separate from the mute list");
    {
        char t[] = "Hi Bob, go 2 B! \xc5\x82\xc3\xb3" "d\xc5\xba ok";   /* UTF-8 'lodz' with Polish letters */
        char copy[64];
        size_t n = strlen(t), i;
        int same_len, punct_kept, utf8_kept = 1, alnum_gone = 1;
        strcpy_s(copy, sizeof(copy), t);
        bc2_chat_hash_text(t, (unsigned)n);
        same_len = strlen(t) == n;
        punct_kept = t[2] == ' ' && t[6] == ',' && t[14] == '!' && t[15] == ' ';
        for (i = 0; i < n; i++) {
            unsigned char c = (unsigned char)copy[i];
            if (c >= 0x80 && t[i] != copy[i]) utf8_kept = 0;
            if (((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) &&
                !strchr("#$%&@!*?", t[i])) alnum_gone = 0;
        }
        check(same_len && punct_kept && utf8_kept && alnum_gone, "hash transform: same length, punctuation + UTF-8 kept, alnum -> symbols");
        strcpy_s(copy, sizeof(copy), "abc");
        bc2_chat_hash_text(copy, 3);
        strcpy_s(t, sizeof(t), "abc");
        bc2_chat_hash_text(t, 3);
        check(strcmp(copy, t) == 0 && strcmp(t, "abc") != 0, "hash transform is deterministic");
    }
    check(bc2_chat_command("/unhash dave", out, sizeof(out)) == 1 && strstr(out, "Unhashed: dave (1)"), "/unhash");
    DeleteFileA(path);

#ifndef BC2_CHAT_STANDALONE
    check(bc2_chat_command("/rec", out, sizeof(out)) == 1 && strstr(out, "not installed"), "/rec = status");
    check(bc2_chat_command("/rec off", out, sizeof(out)) == 1 && strstr(out, "not installed"), "/rec off without recorder");
    check(bc2_chat_command("/rec bogus", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/rec bogus = usage");
#else
    check(bc2_chat_command("/rec", out, sizeof(out)) == 0, "/rec is not a command in the standalone build");
#endif
    /* smoke: pure helpers + command state (no game memory: tick is never called here) */
    {
        float rgb[3], n1[3], n0[3];
        unsigned char obj[0x50];
        const DWORD vt = 0x0145BE04u, c1 = 0x3f4ac083u, c0 = 0x3e6147aeu, et = 0xB;
        check(bc2_smoke_parse_color("red", rgb) && rgb[0] > 0.99f && rgb[1] < 0.2f, "parse 'red'");
        check(bc2_smoke_parse_color(" #00ff80", rgb) && rgb[0] == 0.0f && rgb[1] == 1.0f && rgb[2] > 0.5f && rgb[2] < 0.51f, "parse #rrggbb");
        check(!bc2_smoke_parse_color("#12345", rgb) && !bc2_smoke_parse_color("plaid", rgb), "reject bad colors");
        bc2_smoke_map_colors(rgb, (const float[]){ 0.792f, 0.792f, 0.792f }, (const float[]){ 0.22f, 0.22f, 0.22f }, n1, n0);
        check(n1[1] == 1.0f && n1[0] == 0.0f && n0[1] > 0.27f && n0[1] < 0.28f, "vapor mapping keeps the ramp");
        bc2_smoke_map_colors((const float[]){ 1, 1, 1 }, (const float[]){ 0.914f, 0.914f, 0.914f }, (const float[]){ 0.4f, 0.4f, 0.4f }, n1, n0);
        check(n1[0] == 1.0f && n0[0] > 0.5f && n0[0] < 0.51f, "trail mapping clamps at 1");
        memset(obj, 0xcd, sizeof(obj));
        memcpy(obj, &vt, 4); memcpy(obj + 0x14, &et, 4);
        memcpy(obj + 0x30, &c1, 4); memcpy(obj + 0x34, &c1, 4); memcpy(obj + 0x38, &c1, 4);
        memcpy(obj + 0x40, &c0, 4); memcpy(obj + 0x44, &c0, 4); memcpy(obj + 0x48, &c0, 4);
        check(bc2_smoke_match(obj) == 1, "match vapor object (4th lanes 0xcd masked)");
        obj[0x3C] = 0; obj[0x4C] = 0;
        check(bc2_smoke_match(obj) == 1, "match vapor object (4th lanes 0)");
        obj[0x44] ^= 1;
        check(bc2_smoke_match(obj) == 0, "one color byte off = no match");
        obj[0x44] ^= 1; obj[0x14] = 0xA;
        check(bc2_smoke_match(obj) == 0, "wrong evaluator type = no match");
        obj[0x14] = 0xB;
        /* r300 exe profiles: default = retail; the Steam profile switches the vtable the matcher expects */
        check(bc2_exe() && lstrcmpA(bc2_exe()->name, "retail") == 0 && bc2_exe()->vt_pcid == vt && !bc2_exe_known(),
              "exe profile: retail by default, not yet detected");
        check(bc2_exe_test_select("steam") && bc2_exe()->vt_pcid == 0x014B895Cu && bc2_smoke_match(obj) == 0,
              "exe profile steam: the retail PCID vtable no longer matches");
        {
            const DWORD svt = 0x014B895Cu;
            memcpy(obj, &svt, 4);
            check(bc2_smoke_match(obj) == 1, "exe profile steam: match vapor object with the Steam vtable");
            memcpy(obj, &vt, 4);
        }
        check(bc2_exe()->hook_site_call == 0x0119C402u && ((bc2_exe()->hook_site_call - 2) & 7u) == 0 &&
              bc2_exe()->emitter_mgr_ptr == 0x01BE13E0u, "exe profile steam: hook site 8-byte window aligned, emitter manager");
        check(bc2_exe_test_select("retail") && !bc2_exe_test_select("gog") && bc2_smoke_match(obj) == 1 &&
              ((bc2_exe()->hook_site_call - 2) & 7u) == 0, "exe profile: back to retail, unknown name refused");
    }
    check(bc2_chat_command("/smoke plaid", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke bad color = usage");
    check(bc2_chat_command("/smoke red", out, sizeof(out)) == 1 && strstr(out, "smoke: red"), "/smoke red accepted");
    check(bc2_chat_command("/smoke", out, sizeof(out)) == 1 && strstr(out, "smoke: red;"), "/smoke status shows the wish");
    {
        float c1[3], c0[3], d1[3], d0[3], h[3];
        const float white[3] = { 1, 1, 1 };
        bc2_smoke_hsv(0.0f, 1.0f, h); check(h[0] == 1.0f && h[1] == 0.0f && h[2] == 0.0f, "hsv 0 = red");
        bc2_smoke_hsv(1.0f / 3.0f, 1.0f, h); check(h[1] == 1.0f && h[0] < 0.01f, "hsv 1/3 = green");
        bc2_smoke_hsv(2.0f / 3.0f, 1.0f, h); check(h[2] == 1.0f && h[0] < 0.01f, "hsv 2/3 = blue");
        bc2_smoke_anim_colors(1, 6.0f, 0.0f, NULL, 1, c1, c0);
        bc2_smoke_anim_colors(1, 6.0f, 3.0f, NULL, 1, d1, d0);
        check(c1[0] == 1.0f && d1[2] > 0.9f && c0[0] < 0.3f, "rainbow: t=0 red, t=half blue, dark end keeps the ramp");
        bc2_smoke_anim_colors(2, 8.0f, 0.0f, NULL, 1, c1, c0);
        check(c1[0] == 1.0f && c0[2] > 0.6f && c0[0] < 0.01f, "psycho: young red, old opposite hue (cyan-ish, darker)");
        bc2_smoke_anim_colors(3, 2.0f, 0.5f, white, 1, c1, c0);
        bc2_smoke_anim_colors(3, 2.0f, 1.5f, white, 1, d1, d0);
        check(c1[0] > 0.99f && d1[0] < 0.11f && d1[0] > 0.09f, "pulse: peak 1.0 at quarter period, trough 0.1 at three quarters");
    }
    check(bc2_chat_command("/smoke rainbow", out, sizeof(out)) == 1 && strstr(out, "rainbow, period 6.0"), "/smoke rainbow default period");
    check(bc2_chat_command("/smoke psycho 3", out, sizeof(out)) == 1 && strstr(out, "psycho, period 3.0"), "/smoke psycho 3");
    check(bc2_chat_command("/smoke pulse blue 1.5", out, sizeof(out)) == 1 && strstr(out, "pulse, period 1.5"), "/smoke pulse blue 1.5");
    check(bc2_chat_command("/smoke pulse", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke pulse without colour = usage");
    check(bc2_chat_command("/smoke rainbow fast", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke rainbow bad period = usage");
    check(bc2_chat_command("/smoke status", out, sizeof(out)) == 1 && strstr(out, "smoke: pulse 1.5s blue"), "status shows the mode");
    check(bc2_chat_command("/smoke green", out, sizeof(out)) == 1 && strstr(out, "smoke: green"), "static colour after a mode");
    check(bc2_smoke_parse_color("pumpkin", rgb_h) && rgb_h[0] == 1.0f && rgb_h[1] > 0.25f && rgb_h[1] < 0.26f && rgb_h[2] == 0.0f, "parse 'pumpkin' (Halloween preset)");
    check(bc2_chat_command("/smoke duo red", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke duo with one colour = usage");
    check(bc2_chat_command("/smoke duo red plaid", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke duo bad second colour = usage");
    check(bc2_chat_command("/smoke halloween", out, sizeof(out)) == 1 && strstr(out, "duo pumpkin -> blood"), "/smoke halloween = duo pumpkin blood");
    check(bc2_chat_command("/smoke status", out, sizeof(out)) == 1 && strstr(out, "smoke: duo pumpkin blood"), "status shows the halloween preset");
    {
        const float two[6] = { 1.0f, 0.5f, 0.0f, 0.0f, 0.0f, 1.0f };
        float d1[3], d0[3];
        bc2_smoke_anim_colors(5, 1.0f, 123.0f, two, 2, d1, d0);
        check(d1[0] == 1.0f && d1[1] == 0.5f && d1[2] == 0.0f && d0[0] == 0.0f && d0[2] > 0.69f && d0[2] < 0.71f,
              "duo: young = first colour, old = second colour at 0.7, independent of time");
    }
    check(bc2_chat_command("/smoke alpha 0.25", out, sizeof(out)) == 1 && strstr(out, "alpha = 0.25"), "/smoke alpha 0.25");
    check(bc2_chat_command("/smoke tpl ambient orig", out, sizeof(out)) == 1 && strstr(out, "ambient = orig"), "/smoke tpl ambient orig");
    check(bc2_chat_command("/smoke alpha plaid", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke alpha bad = usage");
    check(bc2_chat_command("/smoke status", out, sizeof(out)) == 1 && strstr(out, "al=0.25") && strstr(out, "am=orig"), "status shows template fields");
    /* r259 per-grenade colours: parser, index helper, and the hook callback against fake game objects */
    check(bc2_chat_command("/smoke per", out, sizeof(out)) == 1 && strstr(out, "per grenade, 8 colours"), "/smoke per (default 8)");
    check(bc2_chat_command("/smoke per 5", out, sizeof(out)) == 1 && strstr(out, "per grenade, 5 colours"), "/smoke per 5");
    check(bc2_chat_command("/smoke per 99", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke per 99 = usage");
    check(bc2_chat_command("/smoke per x", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/smoke per x = usage");
    check(bc2_chat_command("/smoke status", out, sizeof(out)) == 1 && strstr(out, "smoke: per 5;") && strstr(out, "hook pending") &&
          strlen(out) <= 110, "status shows per mode + hook state within one chat line");
    {
        unsigned seen = 0, i, inrange = 1, idx0, idx1;
        for (i = 0; i < 64; i++) {
            unsigned ix = bc2_smoke_per_index(0x02000000u + i * 0x1F0u, 8);
            if (ix >= 8) inrange = 0; else seen |= 1u << ix;
        }
        check(inrange && seen == 0xFFu, "per index: in range, 64 heap-like keys hit all 8 colours");
        idx0 = bc2_smoke_per_index(0x0A1B2C30u, 8);
        idx1 = bc2_smoke_per_index(0x0A1B2C30u, 8);
        check(idx0 == idx1 && bc2_smoke_per_index(0x0A1B2C30u, 0) == 0, "per index: deterministic, npal 0 -> 0");
        check(bc2_smoke_per_index(0x0A1B2C30u, 8) != bc2_smoke_per_index(0x0A1B2D30u, 8) ||
              bc2_smoke_per_index(0x0A1B2C30u, 8) != bc2_smoke_per_index(0x0A1B2E30u, 8), "per index: neighbouring keys differ");
    }
    {
        static __declspec(align(16)) unsigned char pobj[0x50], block[0x200], inst[0x160], ent[0xA0], roots[16][0xA0];
        const DWORD vt = 0x0145BE04u, c1 = 0x3f4ac083u, c0 = 0x3e6147aeu, et = 0xB;
        DWORD addr = (DWORD)(UINT_PTR)pobj, got, first, p;
        const int kind = 1;
        float pal[3], e1[3], e0[3], g1[3];
        unsigned want, j, other = 16;
        memset(pobj, 0xcd, sizeof(pobj));
        memcpy(pobj, &vt, 4); memcpy(pobj + 0x14, &et, 4);
        memcpy(pobj + 0x30, &c1, 4); memcpy(pobj + 0x34, &c1, 4); memcpy(pobj + 0x38, &c1, 4);
        memcpy(pobj + 0x40, &c0, 4); memcpy(pobj + 0x44, &c0, 4); memcpy(pobj + 0x48, &c0, 4);
        memset(roots, 0, sizeof(roots));
        p = (DWORD)(UINT_PTR)roots[0]; memcpy(ent + 0x64, &p, 4);           /* entity -> root 0 (root +0x64 = 0) */
        p = (DWORD)(UINT_PTR)ent;      memcpy(inst + 0x154, &p, 4);         /* inst -> entity */
#define SET_BLOCK() memcpy(block + 0xE0, &addr, 4)
#define BLOCK_PTR() (memcpy(&got, block + 0xE0, 4), got)
        bc2_smoke_per_test_set_known(&addr, &kind, 1, 0);
        SET_BLOCK();
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        check(BLOCK_PTR() == addr, "per apply: disabled (npal 0) changes nothing");
        bc2_smoke_per_test_set_known(&addr, &kind, 1, 8);
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        first = BLOCK_PTR();
        want = bc2_smoke_per_index((DWORD)(UINT_PTR)roots[0], 8);
        bc2_smoke_hsv((float)want / 8.0f, 1.0f, pal);
        bc2_smoke_map_colors(pal, (const float[]){ 0.792f, 0.792f, 0.792f }, (const float[]){ 0.22f, 0.22f, 0.22f }, e1, e0);
        if (first != addr && first) memcpy(g1, (const void *)(UINT_PTR)(first + 0x30), 12); else memset(g1, 0, sizeof(g1));
        check(first != addr && (first & 15) == 0 && memcmp((const void *)(UINT_PTR)first, pobj, 0x30) == 0,
              "per apply: block gets an aligned clone, vtable/evaluator/coefficients preserved");
        check(first != addr && memcmp(g1, pobj + 0x30, 12) != 0 && memcmp(g1, e1, 12) == 0 &&
              memcmp((const void *)(UINT_PTR)(first + 0x40), e0, 12) == 0 && memcmp(pobj + 0x30, &c1, 4) == 0,
              "per apply: clone Color1/Color0 = palette mapping of the root's index, shared object untouched");
        SET_BLOCK();
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        check(BLOCK_PTR() == first, "per apply: same root -> same clone");
        for (j = 1; j < 16 && other == 16; j++)
            if (bc2_smoke_per_index((DWORD)(UINT_PTR)roots[j], 8) != want) other = j;
        if (other < 16) { p = (DWORD)(UINT_PTR)roots[other]; memcpy(ent + 0x64, &p, 4); }
        SET_BLOCK();
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        check(other < 16 && BLOCK_PTR() != first && BLOCK_PTR() != addr, "per apply: another root -> another colour clone");
        p = (DWORD)(UINT_PTR)block;   /* not a known colour object */
        memcpy(block + 0xE0, &p, 4);
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        check(BLOCK_PTR() == p, "per apply: unknown data pointer changes nothing");
        SET_BLOCK();
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, 0x10u);
        check(BLOCK_PTR() == addr, "per apply: unreadable inst (0x10) changes nothing (SEH)");
        p = 0; memcpy(inst + 0x154, &p, 4);   /* entity 0 -> key = inst itself */
        bc2_smoke_per_apply((DWORD)(UINT_PTR)block, (DWORD)(UINT_PTR)inst);
        check(BLOCK_PTR() != addr, "per apply: no entity -> keyed by the instance, still recoloured");
#undef SET_BLOCK
#undef BLOCK_PTR
        bc2_smoke_per_test_set_known(NULL, NULL, 0, 0);
    }
    check(bc2_chat_command("/smoke off", out, sizeof(out)) == 1 && strstr(out, "off"), "/smoke off");

    /* /fx: pure helpers, then a fake EmitterDocument -> template -> processor chain -> colour object in our own memory */
    {
        char sn[48];
        float t[3];
        static __declspec(align(16)) unsigned char pcid[0x50], pcid_s[0x50], pa[0x40], pb[0x40], pc[0x40], data[0xF0],
            data_s[0xF0], doc[0x40], doc_s[0x40];
        static const char *path_fx = "FX/Weapons/Misc/Tracer/Em_Tracer_Rifle_01";
        static const char *path_smoke = "FX/Weapons/Misc/SmokeGrenade_L/Em_SmokeGrenade_Vapor_L_01";
        const float c1[3] = { 0.5f, 0.4f, 0.2f }, c0[3] = { 0.25f, 0.25f, 0.25f };
        bc2_fx_node nodes[8];
        DWORD w;
        int n;
#define PUT(buf, off, v) do { w = (DWORD)(UINT_PTR)(v); memcpy((buf) + (off), &w, 4); } while (0)
        bc2_fx_short_name("FX/Weapons/Misc/Tracer/Em_Tracer_Rifle_01", sn, sizeof(sn));
        check(strcmp(sn, "tracer_rifle_01") == 0, "fx short name: last component, Em_ dropped, lower-case");
        bc2_fx_short_name("Plain", sn, sizeof(sn));
        check(strcmp(sn, "plain") == 0, "fx short name: no path");
        bc2_fx_tint((const float[]){ 1.0f, 0.0f, 0.0f }, c1, t);
        check(t[0] == 0.5f && t[1] == 0.0f && t[2] == 0.0f, "fx tint: wish hue, original brightness");
        bc2_fx_tint((const float[]){ 1.0f, 0.5f, 0.0f }, (const float[]){ 3.0f, 0.0f, 0.0f }, t);
        check(t[0] == 3.0f && t[1] == 1.5f && t[2] == 0.0f, "fx tint: HDR original keeps its brightness (no clamp)");
        check(bc2_fx_key_ok("tracer") && bc2_fx_key_ok("weapons/") && bc2_fx_key_ok("Muzzle_AK"), "fx key: valid names");
        check(!bc2_fx_key_ok("list") && !bc2_fx_key_ok("all") && !bc2_fx_key_ok("off") && !bc2_fx_key_ok("") && !bc2_fx_key_ok("a=b") &&
              !bc2_fx_key_ok("a;b") && !bc2_fx_key_ok("0123456789012345678901234567890123"), "fx key: reserved / bad chars / too long");

        bc2_fx_init();   /* selftest: no ini, only the locks */
        PUT(pcid, 0, bc2_exe()->vt_pcid); PUT(pcid, 0x14, 0xB);
        memcpy(pcid + 0x30, c1, 12); memcpy(pcid + 0x40, c0, 12);
        memcpy(pcid_s, pcid, sizeof(pcid));
        PUT(pa, 0x28, 0x17); PUT(pa, 0x0C, pcid); PUT(pa, 0x20, pb);   /* UpdateColor -> our colour object */
        PUT(pb, 0x28, 0x19); PUT(pb, 0x0C, 0); PUT(pb, 0x20, 0);       /* UpdateTransparency, no evaluator, end */
        n = bc2_fx_walk((DWORD)(UINT_PTR)pa, nodes, 8);
        check(n == 2 && nodes[0].pcid && nodes[0].ptype == 0x17 && !nodes[1].ev, "fx walk: colour node + plain node");
        PUT(pb, 0x20, pa);
        check(bc2_fx_walk((DWORD)(UINT_PTR)pa, nodes, 8) == -1, "fx walk: cycle rejected");
        PUT(pb, 0x20, 0); PUT(pb, 0x28, 0x1234);
        check(bc2_fx_walk((DWORD)(UINT_PTR)pa, nodes, 8) == -1, "fx walk: implausible processor type rejected");
        PUT(pb, 0x28, 0x19);
        check(bc2_fx_walk(0x10u, nodes, 8) == -1, "fx walk: unreadable root rejected (SEH)");

        check(bc2_chat_command("/fx list", out, sizeof(out)) == 1 && strstr(out, "scanning"), "/fx list before any scan");
        PUT(data, 0xB4, pa);
        PUT(doc, 0, bc2_exe()->vt_emitter_doc); PUT(doc, 0x0C, path_fx); PUT(doc, 0x18, data); PUT(doc, 0x30, pa);
        PUT(pc, 0x28, 0x17); PUT(pc, 0x0C, pcid_s); PUT(pc, 0x20, 0);
        PUT(data_s, 0xB4, pc);
        PUT(doc_s, 0, bc2_exe()->vt_emitter_doc); PUT(doc_s, 0x0C, path_smoke); PUT(doc_s, 0x18, data_s);
        bc2_fx_scan_begin();
        bc2_fx_scan_doc((DWORD)(UINT_PTR)doc);
        bc2_fx_scan_doc((DWORD)(UINT_PTR)doc);   /* the same document twice: kept once */
        bc2_fx_scan_doc((DWORD)(UINT_PTR)doc_s);
        bc2_fx_scan_doc(0x10u);                  /* unreadable candidate */
        bc2_fx_scan_end();
        check(bc2_chat_command("/fx list", out, sizeof(out)) == 1 && strstr(out, "fx: 1 emitters") && strstr(out, "\ntracer 1, ") &&
              !strstr(out, "smoke"), "/fx list: group counts on the second line, the smoke grenade not counted");
        check(bc2_chat_command("/fx list all", out, sizeof(out)) == 1 && strstr(out, "page 1/1\ntracer_rifle_01"), "/fx list all: names");
        check(bc2_chat_command("/fx list zzz", out, sizeof(out)) == 1 && strstr(out, "no colour emitters matching zzz"), "/fx list filter");
        check(bc2_chat_command("/fx list 2", out, sizeof(out)) == 1 && strstr(out, "only 1 page"), "/fx list page past the end");
        check(bc2_chat_command("/fx tracer purple-ish", out, sizeof(out)) == 1 && strstr(out, "unknown colour"), "/fx bad colour");
        check(bc2_chat_command("/fx list red", out, sizeof(out)) == 1 && strstr(out, "no colour"), "/fx list is a sub-command");
        check(bc2_chat_command("/fx Tracer red", out, sizeof(out)) == 1 && strstr(out, "'tracer' -> red"), "/fx tracer red");
        check(bc2_chat_command("/fx misc #00ff00", out, sizeof(out)) == 1, "/fx misc green (matches both)");
        bc2_fx_apply();
        memcpy(t, pcid + 0x30, 12);
        check(t[0] == 0.0f && t[1] == 0.5f && t[2] == 0.0f, "fx apply: the last matching rule wins (misc -> green)");
        check(memcmp(pcid_s + 0x30, c1, 12) == 0, "fx apply: smoke grenade template left to /smoke");
        check(bc2_chat_command("/fx misc off", out, sizeof(out)) == 1 && strstr(out, "restored"), "/fx misc off");
        bc2_fx_apply();
        memcpy(t, pcid + 0x30, 12);
        check(t[0] == 0.5f && t[1] > 0.06f && t[1] < 0.07f && t[2] == t[1], "fx apply: remaining rule tracer -> red (ff2020)");
        check(bc2_chat_command("/fx", out, sizeof(out)) == 1 && strstr(out, "1 rule(s): tracer=red") && strstr(out, "1 patched"),
              "/fx status");
        memcpy(pcid + 0x34, &c0[0], 4);          /* the game changed the object under us: no write */
        check(bc2_chat_command("/fx tracer blue", out, sizeof(out)) == 1, "/fx tracer blue");
        bc2_fx_apply();
        check(memcmp(pcid + 0x34, &c0[0], 4) == 0 && *(float *)(pcid + 0x30) == 0.5f, "fx apply: changed object left alone");
        check(bc2_chat_command("/fx off", out, sizeof(out)) == 1 && strstr(out, "off"), "/fx off");
        bc2_fx_apply();
        check(bc2_chat_command("/fx status", out, sizeof(out)) == 1 && strstr(out, "0 rule(s)"), "/fx status after off");
        check(bc2_chat_command("/fx bogus", out, sizeof(out)) == 1 && strstr(out, "Usage"), "/fx usage");
        {   /* paging: 40 documents with distinct colour objects; every page = header + at most 3 lines of <= 90 chars */
            static __declspec(align(16)) unsigned char mp[40][0x50], mproc[40][0x40], mdata[40][0xF0], mdoc[40][0x40];
            static char mpath[40][64];
            int k, pages = 0, ok = 1, seen = 0;
            for (k = 0; k < 40; k++) {
                sprintf_s(mpath[k], sizeof(mpath[k]), "FX/Paging/Em_paging_emitter_number_%02d", k);
                memcpy(mp[k], pcid_s, sizeof(pcid_s));
                PUT(mproc[k], 0x28, 0x17); PUT(mproc[k], 0x0C, mp[k]); PUT(mproc[k], 0x20, 0);
                PUT(mdata[k], 0xB4, mproc[k]);
                PUT(mdoc[k], 0, bc2_exe()->vt_emitter_doc); PUT(mdoc[k], 0x0C, mpath[k]); PUT(mdoc[k], 0x18, mdata[k]);
            }
            bc2_fx_scan_begin();
            for (k = 0; k < 40; k++)
                bc2_fx_scan_doc((DWORD)(UINT_PTR)mdoc[k]);
            bc2_fx_scan_end();
            for (k = 1; k <= 10; k++) {
                char cmd[40], *line, *nl;
                int nlines = 0;
                sprintf_s(cmd, sizeof(cmd), "/fx list paging %d", k);
                bc2_chat_command(cmd, out, sizeof(out));
                if (strstr(out, "only")) break;
                pages++;
                for (line = out; line; line = nl ? nl + 1 : NULL) {
                    nl = strchr(line, '\n');
                    if (nl) *nl = 0;   /* count this line only */
                    if ((nl ? (int)(nl - line) : (int)strlen(line)) > 90) ok = 0;
                    if (nlines++ && strstr(line, "paging_emitter")) {
                        const char *q = line;
                        while ((q = strstr(q, "paging_emitter")) != NULL) { seen++; q++; }
                    }
                }
                if (nlines > 4) ok = 0;
                if (k == 1 && !strstr(out, "fx: 40 ~paging, page 1/")) ok = 0;
            }
            check(ok && pages >= 2 && seen == 40, "/fx list paging: every name once, <= 4 lines of <= 90 chars per page");
            check(bc2_chat_command("/fx list", out, sizeof(out)) == 1 && strstr(out, "fx: 41 emitters"), "/fx list counts all");
        }
        bc2_fx_level(0);
#undef PUT
    }

    DeleteFileA(path);
    printf("%d FAIL\n", fails);
    return fails ? 1 : 0;
}
