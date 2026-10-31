/* Smoke-grenade recolor (r255), see client_smoke.h and README section 6.
 *
 * Facts used (retail BFBC2Game.exe 3911fcc8..., reports r252 asset scan / r253 runtime RE; colour part LIVE-proven
 * 2026-10-27, the template part is the follow-up for the grey core):
 *  - PolynomialColorInterpData: size 0x50, vtable 0x0145BE04, +0x14 EvaluatorType == 0xB, +0x20 Coefficients vec4,
 *    +0x30 Color1 vec3 (light end), +0x40 Color0 vec3 (dark end); 4th lanes never written by the ctor (0xCDCDCDCD
 *    on disk) -> masked. The evaluator 0xB268E0 reads +0x20/+0x30/+0x40 on EVERY call, so a write recolors every
 *    particle of that template from the next frame (LIVE: red smoke, 5 objects on Arica).
 *  - Smoke-grenade values are byte-identical on all 109 level archives (r252): vapor 0.792/0.220, trail 0.914/0.400,
 *    projtrail 0.500/0.600 (Color1/Color0 greys). No other asset carries those (Color1, Color0) pairs, so a memory
 *    match on the pair + vtable is unambiguous. Launcher (L) and hand-grenade (HandGun) emitters share the bytes.
 *  - EmitterDocument: vtable 0x0145AE6C, +0x0C Asset.Name (eastl string, begin char* first), +0x18 EmitterTemplateData.
 *    EmitterTemplateData (size 0xF0, field table 0x01C1DF08): +0x40 AlphaAsColorBlendFactor (0.615 on the smoke
 *    templates: dense texels take alpha as grey, the likely cause of the grey core), +0xCC AmbientLightBlendFactor,
 *    +0xD0 VertexPixelLightingBlendFactor, +0xD4 GlobalLocalNormalBlendFactor, +0xB8 Name (char* begin, full asset path
 *    'FX/Weapons/Misc/SmokeGrenade_L/.../Em_SmokeGrenade_VaporExplotion_L_01'). Whether the renderer reads +0x40 from
 *    the data object each draw (patch takes effect) or copied it at template creation is NOT known: live test.
 *  - Templates come per level: ClientLevel_Load creates a new EmitterManager, global [0x01575A50] (r253); the DBX
 *    data objects exist before any spawn (LIVE: found before the first smoke).
 * Scan: committed private RW regions, 4-byte steps for the two vtable words, then the content checks; runs on the chat
 * monitor thread under __try per region (own stack excluded). Writes are small float stores into live RW heap objects,
 * re-validated (vtable / name + expected current values) before every write; /smoke off restores the originals.
 * The addresses above are the retail build's; the running build's come from bc2_exe() (Steam build, r300).
 */
#include <windows.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include <math.h>
#include "client_smoke.h"
#include "client_smoke_hook.h"
#include "client_fx.h"
#include "bc2_exe.h"

#ifndef BC2_CHAT_APPDIR
#define BC2_CHAT_APPDIR      "BC2Replay"      /* see client_chat.c */
#endif
#ifndef BC2_CHAT_LOG_PREFIX
#define BC2_CHAT_LOG_PREFIX  "dinput8 proxy"
#endif
#ifndef BC2_SMOKE_DEFAULT
#define BC2_SMOKE_DEFAULT    ""   /* /smoke args applied on the very first run (no [smoke] color= yet); "" = none */
#endif
#define VT_PCID              (bc2_exe()->vt_pcid)          /* retail 0x0145BE04 */
#define OFF_EVAL_TYPE        0x14u
#define OFF_COLOR1           0x30u
#define OFF_COLOR0           0x40u
#define OBJ_SIZE             0x50u
#define VT_EMITTER_DOC       (bc2_exe()->vt_emitter_doc)   /* retail 0x0145AE6C */
#define OFF_DOC_NAME         0x0Cu
#define OFF_DOC_TPL          0x18u
#define OFF_TPL_NAME         0xB8u
#define TPL_SIZE             0xF0u
#define ADDR_EMITTER_MGR     (bc2_exe()->emitter_mgr_ptr)  /* retail 0x01575A50 */
#define MAX_OBJS             64
#define LEVEL_SCAN_DELAY_MS  4000u
#define LEVEL_SCAN_REPEAT_MS 20000u
#define PERIODIC_SCAN_MS     60000u
#define SCAN_REGION_CAP      (512u << 20)
#define NAME_KEY             "smokegrenade"
#define KIND_TPL             4
#define NTPL                 4
#define MODE_STATIC          0
#define MODE_RAINBOW         1   /* hue cycles, whole cloud together */
#define MODE_PSYCHO          2   /* Color1/Color0 = opposite hues, rotating; each template offset by 120 deg */
#define MODE_PULSE           3   /* one colour, brightness sine */
#define MODE_PER             4   /* r259: every grenade effect its own palette colour (per-instance hook) */
#define MODE_DUO             5   /* two fixed colours: young particles (Color1) -> old particles (Color0), no animation */
#define PI_F                 3.14159265f
#define PER_MAX_PAL          12
#define PER_DEF_PAL          8
#define OFF_BLOCK_DATA       0xE0u   /* eval block: colour node data pointer (PolynomialColorInterpData*) */
#define OFF_INST_ENTITY      0x154u  /* EmitterInstance -> owning ClientEmitterEntity (r256, C) */
#define OFF_ENT_PARENT       0x64u   /* EffectEntity parent (r256, P) */
#define PER_WALK_MAX         8

typedef struct { const char *name; float c1[3], c0[3]; } kind_def;
static const kind_def KINDS[3] = {
    { "vapor",     { 0.792f, 0.792f, 0.792f }, { 0.220f, 0.220f, 0.220f } },
    { "trail",     { 0.914f, 0.914f, 0.914f }, { 0.400f, 0.400f, 0.400f } },
    { "projtrail", { 0.500f, 0.500f, 0.500f }, { 0.600f, 0.600f, 0.600f } },
};
/* exact little-endian words of the asset values (r252 hex), compared as bytes */
static const DWORD KIND_C1[3] = { 0x3f4ac083u, 0x3f69fbe7u, 0x3f000000u };
static const DWORD KIND_C0[3] = { 0x3e6147aeu, 0x3ecccccdu, 0x3f19999au };
/* template float fields (EmitterTemplateData offsets), tunable via /smoke tpl <name> <v|orig> */
static const struct { const char *name, *tag; DWORD off; } TPL_FIELDS[NTPL] = {
    { "alpha", "al", 0x40u }, { "ambient", "am", 0xCCu }, { "vpl", "vp", 0xD0u }, { "normal", "no", 0xD4u }
};

typedef struct {
    DWORD addr; int kind; int patched;
    float cur1[3], cur0[3];          /* colour objects: current values (as found, then as written) */
    float torig[NTPL], tcur[NTPL];   /* templates: original and current field values */
} obj_entry;

/* g_cs: scan state + object table, held by the monitor thread (tick) for the whole scan; the command path never
 * blocks on it (status = TryEnter). g_cmd_cs: the wish mailbox, held for a few instructions only. */
static CRITICAL_SECTION g_cs, g_cmd_cs;
static int g_cs_ready;
static obj_entry g_objs[MAX_OBJS];
static int g_nobjs;
static int g_active;                 /* mailbox: a wish color is set */
static float g_rgb[6];               /* mailbox: [0..2] the colour, [3..5] the second (old-particle) colour of MODE_DUO */
static char g_wish[32], g_wish2[32]; /* mailbox: text as given (name or #rrggbb); wish2 = the DUO second colour */
static float g_tpl[NTPL];            /* mailbox: wished template field values, NaN = leave original */
static int g_mode;                   /* mailbox: MODE_* */
static float g_period;               /* mailbox: animation period in seconds */
static int g_pending;                /* mailbox: wish changed since the last tick */
static int g_npal = PER_DEF_PAL;     /* mailbox: MODE_PER palette size 2..12 */
static int s_active, s_mode, s_npal; /* tick's snapshot of the mailbox (used by apply_all under g_cs) */
static float s_rgb[6], s_tpl[NTPL], s_period;
/* hook install state (written by the monitor thread in tick, read by status under g_cmd_cs): 0 not tried, 1 ok, -1 refused */
static int g_hook_state;
static char g_hook_why[64];

/* r259 per-instance table. g_per_cs guards everything below and is held for a few instructions only, by the monitor
 * thread (publish) and by render workers (bc2_smoke_per_apply); never g_cs on the render side. g_per_enabled /
 * g_per_nknown / g_per_known are also read lock-free as a prefilter (aligned DWORD reads; confirmed under the lock). */
typedef struct { DWORD addr; int kind; } per_known;
static CRITICAL_SECTION g_per_cs;
static per_known g_per_known[MAX_OBJS];
static volatile LONG g_per_nknown, g_per_enabled, g_per_npal, g_per_gen;
static __declspec(align(16)) unsigned char g_per_clones[MAX_OBJS][PER_MAX_PAL][OBJ_SIZE];
static unsigned char g_per_ready[MAX_OBJS][PER_MAX_PAL];
static volatile LONG g_per_calls, g_per_repl, g_per_faults;
static DWORD g_anim_t0;
static DWORD g_last_mgr, g_mgr_change_tick, g_last_scan_tick, g_scan_count, g_last_scan_ms;
static int g_level_scans_left;

static void slog(const char *text)
{
    char line[300];
    wsprintfA(line, BC2_CHAT_LOG_PREFIX ": smoke %s\r\n", text);
    OutputDebugStringA(line);
}

static float nanf_(void) { float f; DWORD w = 0x7fc00000u; memcpy(&f, &w, 4); return f; }
static int isnan_(float f) { return f != f; }

/* ---- pure helpers ---- */
typedef struct { const char *name; unsigned rgb; } named_color;
static const named_color NAMES[] = {
    { "red", 0xff2020 }, { "green", 0x20ff20 }, { "blue", 0x2040ff }, { "yellow", 0xffff20 }, { "cyan", 0x20ffff },
    { "magenta", 0xff20ff }, { "orange", 0xff8000 }, { "purple", 0xa020ff }, { "pink", 0xff60c0 }, { "white", 0xffffff },
    { "black", 0x101010 }, { "lime", 0x80ff00 }, { "teal", 0x00a0a0 }, { "gold", 0xffc000 }, { "grey", 0xa0a0a0 },
    { "gray", 0xa0a0a0 },
    /* Halloween presets (2026-10-31); "/smoke halloween" = duo pumpkin blood (both live-picked 2026-10-31). pumpkin is a red-leaning orange (live-picked): the
     * alpha-as-colour blend greys the cloud, so a yellow-leaning orange (0xff7518) came out tan in the live test */
    { "pumpkin", 0xff4000 }, { "ghost", 0xf4f4ff }, { "blood", 0xff1000 },
};

static int hexval(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

int bc2_smoke_parse_color(const char *text, float rgb[3])
{
    size_t i;
    unsigned v = 0;
    if (!text)
        return 0;
    while (*text == ' ' || *text == '\t')
        text++;
    if (text[0] == '#') {
        int d;
        if (strlen(text) != 7)
            return 0;
        for (i = 1; i < 7; i++) {
            d = hexval(text[i]);
            if (d < 0)
                return 0;
            v = (v << 4) | (unsigned)d;
        }
    } else {
        for (i = 0; i < sizeof(NAMES) / sizeof(NAMES[0]); i++)
            if (_stricmp(text, NAMES[i].name) == 0)
                break;
        if (i == sizeof(NAMES) / sizeof(NAMES[0]))
            return 0;
        v = NAMES[i].rgb;
    }
    rgb[0] = (float)((v >> 16) & 0xff) / 255.0f;
    rgb[1] = (float)((v >> 8) & 0xff) / 255.0f;
    rgb[2] = (float)(v & 0xff) / 255.0f;
    return 1;
}

/* Keep each emitter's own brightness ramp: the vapor cloud's light end (0.792) becomes the wish color itself, the
 * other greys scale by the same factor (trail a bit brighter, projectile trail dimmer), clamped to 1. */
void bc2_smoke_map_colors(const float rgb[3], const float orig1[3], const float orig0[3], float new1[3], float new0[3])
{
    int i;
    for (i = 0; i < 3; i++) {
        float a = rgb[i] * orig1[i] / 0.792f, b = rgb[i] * orig0[i] / 0.792f;
        new1[i] = a > 1.0f ? 1.0f : a;
        new0[i] = b > 1.0f ? 1.0f : b;
    }
}

/* hue in turns (0..1 wraps), saturation 1, value v */
void bc2_smoke_hsv(float hue, float v, float rgb[3])
{
    float h, f, x;
    int i;
    hue -= (float)floor(hue);
    h = hue * 6.0f;
    i = (int)h;
    f = h - (float)i;
    x = v * (1.0f - f);
    f = v * f;
    switch (i % 6) {
    case 0: rgb[0] = v; rgb[1] = f; rgb[2] = 0; break;
    case 1: rgb[0] = x; rgb[1] = v; rgb[2] = 0; break;
    case 2: rgb[0] = 0; rgb[1] = v; rgb[2] = f; break;
    case 3: rgb[0] = 0; rgb[1] = x; rgb[2] = v; break;
    case 4: rgb[0] = f; rgb[1] = 0; rgb[2] = v; break;
    default: rgb[0] = v; rgb[1] = 0; rgb[2] = x; break;
    }
}

/* the two colour ends an emitter of kind k (1..3) gets at animation time t (seconds) in mode;
 * rgb = the wish colour (MODE_DUO reads SIX floats: young colour, then old colour) */
void bc2_smoke_anim_colors(int mode, float period, float t, const float rgb[3], int kind, float c1[3], float c0[3])
{
    const float *o1 = KINDS[kind - 1].c1, *o0 = KINDS[kind - 1].c0;
    float base[3], tmp[3], ph;
    int i;
    if (period < 0.2f) period = 0.2f;
    ph = t / period;
    switch (mode) {
    case MODE_RAINBOW:
        bc2_smoke_hsv(ph, 1.0f, base);
        bc2_smoke_map_colors(base, o1, o0, c1, c0);
        break;
    case MODE_PSYCHO:
        bc2_smoke_hsv(ph + (float)(kind - 1) / 3.0f, 1.0f, c1);
        bc2_smoke_hsv(ph + (float)(kind - 1) / 3.0f + 0.5f, 0.7f, c0);   /* old particles: the opposite hue, a bit darker */
        break;
    case MODE_PULSE:
        for (i = 0; i < 3; i++)
            tmp[i] = rgb[i] * (0.55f + 0.45f * (float)sin(2.0f * PI_F * ph));
        bc2_smoke_map_colors(tmp, o1, o0, c1, c0);
        break;
    case MODE_DUO:   /* like psycho's two ends, but fixed: young = first colour as is, old = second colour a bit darker */
        for (i = 0; i < 3; i++) {
            c1[i] = rgb[i];
            c0[i] = rgb[3 + i] * 0.7f;
        }
        break;
    default:
        bc2_smoke_map_colors(rgb, o1, o0, c1, c0);
        break;
    }
}

int bc2_smoke_match(const unsigned char *obj)
{
    DWORD vt, et, w;
    int k, i;
    memcpy(&vt, obj, 4);
    if (vt != VT_PCID)
        return 0;
    memcpy(&et, obj + OFF_EVAL_TYPE, 4);
    if (et != 0xB)
        return 0;
    for (k = 0; k < 3; k++) {
        int ok = 1;
        for (i = 0; i < 3 && ok; i++) {
            memcpy(&w, obj + OFF_COLOR1 + 4 * i, 4);
            if (w != KIND_C1[k]) ok = 0;
            memcpy(&w, obj + OFF_COLOR0 + 4 * i, 4);
            if (w != KIND_C0[k]) ok = 0;
        }
        if (ok)
            return k + 1;
    }
    return 0;
}

static int contains_ci(const char *hay, const char *needle)
{
    size_t n = strlen(needle);
    const char *p;
    for (p = hay; *p; p++)
        if (_strnicmp(p, needle, n) == 0)
            return 1;
    return 0;
}

/* ---- game memory (monitor thread; rd32 also runs on render workers via bc2_smoke_per_apply, r259) ---- */
static int rd32(DWORD addr, DWORD *out)
{
    __try {
        *out = *(volatile DWORD *)(UINT_PTR)addr;
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

/* copy a NUL-terminated string the game owns (char* at [addr]) into out; 0 on any fault / no key match */
static int rd_name(DWORD ptr_addr, char *out, int size)
{
    DWORD p = 0;
    int i;
    out[0] = 0;
    if (!rd32(ptr_addr, &p) || p < 0x10000)
        return 0;
    __try {
        for (i = 0; i < size - 1; i++) {
            out[i] = *(volatile char *)(UINT_PTR)(p + i);
            if (!out[i])
                break;
        }
        out[size - 1] = 0;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        out[0] = 0;
        return 0;
    }
    return contains_ci(out, NAME_KEY);
}

static int tpl_read(DWORD tpl, float vals[NTPL])
{
    int i;
    char name[200];
    if (!rd_name(tpl + OFF_TPL_NAME, name, sizeof(name)))
        return 0;
    for (i = 0; i < NTPL; i++)
        if (!rd32(tpl + TPL_FIELDS[i].off, (DWORD *)&vals[i]))
            return 0;
    return 1;
}

static int obj_alive(const obj_entry *o)
{
    unsigned char buf[OBJ_SIZE];
    DWORD vt, et;
    if (o->kind == KIND_TPL) {
        float v[NTPL];
        return tpl_read(o->addr, v) && memcmp(v, o->tcur, sizeof(v)) == 0;
    }
    __try {
        memcpy(buf, (const void *)(UINT_PTR)o->addr, OBJ_SIZE);
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
    memcpy(&vt, buf, 4);
    memcpy(&et, buf + OFF_EVAL_TYPE, 4);
    return vt == VT_PCID && et == 0xB && memcmp(buf + OFF_COLOR1, o->cur1, 12) == 0 && memcmp(buf + OFF_COLOR0, o->cur0, 12) == 0;
}

static int obj_write(obj_entry *o, const float c1[3], const float c0[3])
{
    if (!obj_alive(o))
        return 0;
    __try {
        memcpy((void *)(UINT_PTR)(o->addr + OFF_COLOR1), c1, 12);
        memcpy((void *)(UINT_PTR)(o->addr + OFF_COLOR0), c0, 12);
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
    memcpy(o->cur1, c1, 12);
    memcpy(o->cur0, c0, 12);
    return 1;
}

static int tpl_write(obj_entry *o, const float vals[NTPL])
{
    int i;
    if (!obj_alive(o))
        return 0;
    __try {
        for (i = 0; i < NTPL; i++)
            *(volatile float *)(UINT_PTR)(o->addr + TPL_FIELDS[i].off) = vals[i];
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
    memcpy(o->tcur, vals, sizeof(o->tcur));
    return 1;
}

static int known(DWORD addr)
{
    int i;
    for (i = 0; i < g_nobjs; i++)
        if (g_objs[i].addr == addr)
            return 1;
    return 0;
}

static void drop_dead(void)
{
    int i, j = 0;
    for (i = 0; i < g_nobjs; i++)
        if (obj_alive(&g_objs[i]))
            g_objs[j++] = g_objs[i];
    g_nobjs = j;
}

/* an EmitterDocument candidate at addr: name contains the key -> register its template (once) */
static void try_document(DWORD addr, int *found)
{
    char name[200], line[260];
    DWORD tpl = 0;
    float vals[NTPL];
    obj_entry *o;
    if (!rd_name(addr + OFF_DOC_NAME, name, sizeof(name)))
        return;
    if (!rd32(addr + OFF_DOC_TPL, &tpl) || tpl < 0x10000 || known(tpl) || !tpl_read(tpl, vals))
        return;
    o = &g_objs[g_nobjs++];
    memset(o, 0, sizeof(*o));
    o->addr = tpl;
    o->kind = KIND_TPL;
    memcpy(o->torig, vals, sizeof(vals));
    memcpy(o->tcur, vals, sizeof(vals));
    (*found)++;
    wsprintfA(line, "template %08lx '%.120s'", (unsigned long)tpl, name);
    slog(line);
}

static void scan_region(const unsigned char *base, SIZE_T size, int *found)
{
    SIZE_T off;
    const DWORD vt_doc = VT_EMITTER_DOC, vt_pcid = VT_PCID;   /* hot loop: read the profile once */
    __try {
        for (off = 0; off + OBJ_SIZE <= size; off += 4) {   /* no early stop on a full smoke table: /fx needs every document */
            DWORD w;
            int k;
            memcpy(&w, base + off, 4);
            if (w == vt_doc) {
                bc2_fx_scan_doc((DWORD)(UINT_PTR)(base + off));
                if (g_nobjs < MAX_OBJS)
                    try_document((DWORD)(UINT_PTR)(base + off), found);
                continue;
            }
            if (w != vt_pcid || g_nobjs >= MAX_OBJS)
                continue;
            k = bc2_smoke_match(base + off);
            if (!k || known((DWORD)(UINT_PTR)(base + off)))
                continue;
            {
                obj_entry *o = &g_objs[g_nobjs++];
                memset(o, 0, sizeof(*o));
                o->addr = (DWORD)(UINT_PTR)(base + off);
                o->kind = k;
                memcpy(o->cur1, base + off + OFF_COLOR1, 12);   /* exact bytes as found */
                memcpy(o->cur0, base + off + OFF_COLOR0, 12);
                (*found)++;
            }
        }
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        /* region vanished mid-scan */
    }
}

static int scan_all(void)
{
    MEMORY_BASIC_INFORMATION mbi;
    const unsigned char *p = NULL;
    int found = 0;
    DWORD t0 = GetTickCount();
    const NT_TIB *tib = (const NT_TIB *)NtCurrentTeb();   /* this thread's stack: obj_alive() leaves object copies in dead frames */
    const unsigned char *stk_lo = (const unsigned char *)tib->StackLimit, *stk_hi = (const unsigned char *)tib->StackBase;
    while (VirtualQuery(p, &mbi, sizeof(mbi)) == sizeof(mbi)) {
        const unsigned char *next = (const unsigned char *)mbi.BaseAddress + mbi.RegionSize;
        int own_stack = (const unsigned char *)mbi.BaseAddress < stk_hi && next > stk_lo;
        if (!own_stack && mbi.State == MEM_COMMIT && mbi.Type == MEM_PRIVATE && !(mbi.Protect & (PAGE_GUARD | PAGE_NOACCESS)) &&
            (mbi.Protect == PAGE_READWRITE || mbi.Protect == PAGE_EXECUTE_READWRITE) && mbi.RegionSize <= SCAN_REGION_CAP)
            scan_region((const unsigned char *)mbi.BaseAddress, mbi.RegionSize, &found);
        if (next <= p)
            break;
        p = next;
    }
    g_scan_count++;
    g_last_scan_tick = GetTickCount();
    g_last_scan_ms = g_last_scan_tick - t0;
    return found;
}

static void apply_all(void)
{
    int i, n = 0, fail = 0;
    char line[160];
    for (i = 0; i < g_nobjs; i++) {
        obj_entry *o = &g_objs[i];
        if (o->kind == KIND_TPL) {
            float want[NTPL];
            int j;
            for (j = 0; j < NTPL; j++)
                want[j] = (s_active && !isnan_(s_tpl[j])) ? s_tpl[j] : o->torig[j];
            if (memcmp(want, o->tcur, sizeof(want)) == 0)
                continue;
            if (tpl_write(o, want)) { o->patched = memcmp(want, o->torig, sizeof(want)) != 0; n++; } else fail++;
            continue;
        }
        if (s_active && s_mode != MODE_PER) {   /* per mode: the shared objects keep the greys, the hook swaps in clones */
            float n1[3], n0[3];
            bc2_smoke_anim_colors(s_mode, s_period, (float)(GetTickCount() - g_anim_t0) / 1000.0f, s_rgb, o->kind, n1, n0);
            if (memcmp(o->cur1, n1, 12) == 0 && memcmp(o->cur0, n0, 12) == 0)
                continue;
            if (obj_write(o, n1, n0)) { o->patched = 1; n++; } else fail++;
        } else if (o->patched) {
            if (obj_write(o, KINDS[o->kind - 1].c1, KINDS[o->kind - 1].c0)) { o->patched = 0; n++; } else fail++;
        }
    }
    if ((n && (s_mode == MODE_STATIC || s_mode == MODE_DUO || !s_active)) || fail) {
        wsprintfA(line, "%s %d object(s), %d failed, %d known", s_active && s_mode != MODE_PER ? "patched" : "restored", n, fail, g_nobjs);
        slog(line);
    }
}

/* ---- r259 per-instance colours (bc2_smoke_per_apply runs on render workers) ---- */
unsigned bc2_smoke_per_index(DWORD key, unsigned npal)
{
    DWORD h = (key >> 4) * 0x9E3779B1u;   /* heap pointers: the low 4 bits carry no information */
    h ^= h >> 16;
    h *= 0x85EBCA6Bu;
    h ^= h >> 13;
    if (!npal)
        return 0;
    return (unsigned)(((unsigned __int64)h * npal) >> 32);
}

/* palette colour idx of npal: evenly spaced hues, full saturation, idx 0 = red */
static void per_palette(unsigned idx, unsigned npal, float rgb[3])
{
    bc2_smoke_hsv((float)idx / (float)npal, 1.0f, rgb);
}

static int rd_obj(DWORD addr, unsigned char *out)
{
    __try {
        memcpy(out, (const void *)(UINT_PTR)addr, OBJ_SIZE);
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

/* replace the known set (resets the clones when the set or the palette size changes); enabled needs npal >= 2 and a
 * non-empty set. Monitor thread (tick, under g_cs) or the selftest publisher. */
static void per_set(const per_known *k, int n, int want, int npal)
{
    EnterCriticalSection(&g_per_cs);
    if (n != g_per_nknown || npal != g_per_npal || (n && memcmp(k, g_per_known, (size_t)n * sizeof(*k)) != 0)) {
        g_per_enabled = 0;   /* prefilter readers see the old set disabled while it changes */
        if (n)
            memcpy(g_per_known, k, (size_t)n * sizeof(*k));
        g_per_nknown = n;
        g_per_npal = npal;
        memset(g_per_ready, 0, sizeof(g_per_ready));
        g_per_gen++;
    }
    g_per_enabled = want && npal >= 2 && npal <= PER_MAX_PAL && n > 0;
    LeaveCriticalSection(&g_per_cs);
}

static void per_publish(void)
{
    per_known k[MAX_OBJS];
    int i, n = 0;
    for (i = 0; i < g_nobjs; i++)
        if (g_objs[i].kind >= 1 && g_objs[i].kind <= 3) {
            k[n].addr = g_objs[i].addr;
            k[n].kind = g_objs[i].kind;
            n++;
        }
    per_set(k, n, s_active && s_mode == MODE_PER, s_npal);
}

#ifdef BC2_CHAT_SELFTEST
void bc2_smoke_per_test_set_known(const DWORD *addrs, const int *kinds, int n, int npal)
{
    per_known k[MAX_OBJS];
    int i;
    if (!g_cs_ready)
        bc2_smoke_init();
    if (n > MAX_OBJS)
        n = MAX_OBJS;
    for (i = 0; i < n; i++) {
        k[i].addr = addrs[i];
        k[i].kind = kinds[i];
    }
    per_set(k, n, 1, npal);
}
#endif

/* grenade identity: the effect root above the instance's emitter entity (all emitters of one detonation share it).
 * 0 = the instance itself is unreadable (do nothing). entity 0 -> key inst; a fault in the parent walk -> key entity. */
static int per_key(DWORD inst, DWORD *key)
{
    DWORD ent, cur, p;
    int i;
    if (!rd32(inst + OFF_INST_ENTITY, &ent))
        return 0;
    if (!ent) {
        *key = inst;
        return 1;
    }
    cur = ent;
    for (i = 0; i < PER_WALK_MAX; i++) {
        if (!rd32(cur + OFF_ENT_PARENT, &p)) {
            cur = ent;
            break;
        }
        if (!p)
            break;
        cur = p;
    }
    *key = cur;
    return 1;
}

void bc2_smoke_per_apply(DWORD block, DWORD inst)
{
    DWORD data, key, vt, et;
    LONG gen;
    int i, n, slot, kind = 0, npal = 0;
    unsigned idx = 0;
    unsigned char *clone = NULL, buf[OBJ_SIZE];
    float rgb[3], c1[3], c0[3];
    if (!g_per_enabled)
        return;
    InterlockedIncrement(&g_per_calls);
    if (!rd32(block + OFF_BLOCK_DATA, &data)) {
        InterlockedIncrement(&g_per_faults);
        return;
    }
    n = g_per_nknown;   /* lock-free prefilter: most emitters are not smoke */
    if (n > MAX_OBJS)
        n = MAX_OBJS;
    for (i = 0; i < n; i++)
        if (g_per_known[i].addr == data)
            break;
    if (i == n)
        return;
    if (!per_key(inst, &key)) {
        InterlockedIncrement(&g_per_faults);
        return;
    }
    EnterCriticalSection(&g_per_cs);
    slot = -1;
    if (g_per_enabled)
        for (i = 0; i < g_per_nknown; i++)
            if (g_per_known[i].addr == data) { slot = i; break; }
    if (slot >= 0) {
        kind = g_per_known[slot].kind;
        npal = g_per_npal;
        idx = bc2_smoke_per_index(key, (unsigned)npal);
        if (g_per_ready[slot][idx])
            clone = g_per_clones[slot][idx];
    }
    gen = g_per_gen;
    LeaveCriticalSection(&g_per_cs);
    if (slot < 0)
        return;
    if (!clone) {
        /* first use of (object, colour): copy the live object outside the lock, then fill the clone */
        if (!rd_obj(data, buf)) {
            InterlockedIncrement(&g_per_faults);
            return;
        }
        memcpy(&vt, buf, 4);
        memcpy(&et, buf + OFF_EVAL_TYPE, 4);
        if (vt != VT_PCID || et != 0xB || kind < 1 || kind > 3)
            return;
        per_palette(idx, (unsigned)npal, rgb);
        bc2_smoke_map_colors(rgb, KINDS[kind - 1].c1, KINDS[kind - 1].c0, c1, c0);
        memcpy(buf + OFF_COLOR1, c1, 12);   /* 4th lanes stay as copied */
        memcpy(buf + OFF_COLOR0, c0, 12);
        EnterCriticalSection(&g_per_cs);
        if (g_per_gen == gen) {   /* the set did not change while unlocked: slot/idx still mean the same */
            if (!g_per_ready[slot][idx]) {
                memcpy(g_per_clones[slot][idx], buf, OBJ_SIZE);
                g_per_ready[slot][idx] = 1;
            }
            clone = g_per_clones[slot][idx];
        }
        LeaveCriticalSection(&g_per_cs);
        if (!clone)
            return;
    }
    if (!wr32(block + OFF_BLOCK_DATA, (DWORD)(UINT_PTR)clone)) {
        InterlockedIncrement(&g_per_faults);
        return;
    }
    InterlockedIncrement(&g_per_repl);
}

/* ---- public ---- */
static void ini_path(char *out, DWORD size)
{
#ifdef BC2_CHAT_SELFTEST
    (void)size;
    out[0] = 0;   /* the selftest must never read or write the real config.ini */
#else
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", out, size);
    if (n == 0 || n + 32 >= size) { out[0] = 0; return; }
    lstrcatA(out, "\\" BC2_CHAT_APPDIR "\\config.ini");
#endif
}

static int parse_float(const char *s, float *out)
{
    char *end;
    double d;
    if (!s || !*s)
        return 0;
    d = strtod(s, &end);
    while (*end == ' ' || *end == '\t')
        end++;
    if (*end || d < -10.0 || d > 10.0)
        return 0;
    *out = (float)d;
    return 1;
}

static void ini_write_tpl(const char *ini)
{
    char v[32];
    int i;
    for (i = 0; i < NTPL; i++) {
        if (isnan_(g_tpl[i]))
            lstrcpyA(v, "orig");
        else
            sprintf_s(v, sizeof(v), "%.3f", g_tpl[i]);
        WritePrivateProfileStringA("smoke", TPL_FIELDS[i].name, v, ini);
    }
}

void bc2_smoke_init(void)
{
    char ini[MAX_PATH], value[32];
    int i;
    float f;
    if (!g_cs_ready) {
        InitializeCriticalSection(&g_cs);
        InitializeCriticalSection(&g_cmd_cs);
        InitializeCriticalSection(&g_per_cs);
        g_cs_ready = 1;
        for (i = 0; i < NTPL; i++)
            g_tpl[i] = nanf_();
        /* r255d: all template fields default to 'orig' - the grey core was the grenade's ground-impact dust effect
         * (a separate, material-dependent asset), not AlphaAsColorBlendFactor (user, 2026-10-27); /smoke alpha 0 remains
         * available as a look tweak */
    }
    ini_path(ini, MAX_PATH);
    if (!ini[0])
        return;
    for (i = 0; i < NTPL; i++) {
        GetPrivateProfileStringA("smoke", TPL_FIELDS[i].name, "", value, sizeof(value), ini);
        if (_stricmp(value, "orig") == 0)
            g_tpl[i] = nanf_();
        else if (parse_float(value, &f))
            g_tpl[i] = f;
    }
    GetPrivateProfileStringA("smoke", "period", "", value, sizeof(value), ini);
    g_period = parse_float(value, &f) && f > 0.0f ? f : 6.0f;
    GetPrivateProfileStringA("smoke", "mode", "", value, sizeof(value), ini);
    g_mode = _stricmp(value, "rainbow") == 0 ? MODE_RAINBOW : _stricmp(value, "psycho") == 0 ? MODE_PSYCHO :
             _stricmp(value, "pulse") == 0 ? MODE_PULSE : _stricmp(value, "per") == 0 ? MODE_PER :
             _stricmp(value, "duo") == 0 ? MODE_DUO : MODE_STATIC;
    GetPrivateProfileStringA("smoke", "color2", "", value, sizeof(value), ini);
    if (!bc2_smoke_parse_color(value, g_rgb + 3)) {   /* duo second colour; default blood */
        lstrcpyA(value, "blood");
        bc2_smoke_parse_color(value, g_rgb + 3);
    }
    lstrcpynA(g_wish2, value, sizeof(g_wish2));
    i = (int)GetPrivateProfileIntA("smoke", "per", PER_DEF_PAL, ini);
    g_npal = i >= 2 && i <= PER_MAX_PAL ? i : PER_DEF_PAL;
    GetPrivateProfileStringA("smoke", "color", "", value, sizeof(value), ini);
    if (!value[0] && BC2_SMOKE_DEFAULT[0]) {
        /* first run of a build with a default preset: no color= key at all (after /smoke off it is "off", so the
         * user's choice sticks). Goes through the command path, which persists it like a typed command. */
        char reply[200];
        g_anim_t0 = GetTickCount();
        bc2_smoke_command(BC2_SMOKE_DEFAULT, reply, sizeof(reply));
        slog("first run: default preset " BC2_SMOKE_DEFAULT);
        return;
    }
    /* the hook for mode=per is installed by the first tick (monitor thread), see bc2_smoke_tick */
    if (value[0] && _stricmp(value, "off") != 0 &&
        (bc2_smoke_parse_color(value, g_rgb) || g_mode == MODE_RAINBOW || g_mode == MODE_PSYCHO || g_mode == MODE_PER)) {
        lstrcpynA(g_wish, value, sizeof(g_wish));
        g_active = 1;
        g_pending = 1;
        slog("ini color loaded");
    }
    g_anim_t0 = GetTickCount();
}

void bc2_smoke_tick(void)
{
    DWORD mgr = 0, now = GetTickCount();
    int do_scan = 0, found, want_hook = 0;
    char line[200], why[64];
    if (!g_cs_ready)
        return;
    if (!rd32(ADDR_EMITTER_MGR, &mgr))
        mgr = 0;
    EnterCriticalSection(&g_cs);
    if (mgr != g_last_mgr) {
        g_last_mgr = mgr;
        g_nobjs = 0;                          /* old level's objects are gone */
        g_mgr_change_tick = now;
        g_level_scans_left = mgr ? 2 : 0;     /* 4 s and 20 s after the new manager appears */
        bc2_fx_level(mgr);
        wsprintfA(line, "emitter manager -> %08lx (%s)", (unsigned long)mgr, mgr ? "rescan scheduled" : "no level");
        slog(line);
    }
    EnterCriticalSection(&g_cmd_cs);
    if (g_pending) {
        g_pending = 0;
        s_active = g_active;
        s_mode = g_mode;
        s_period = g_period;
        memcpy(s_rgb, g_rgb, sizeof(s_rgb));
        memcpy(s_tpl, g_tpl, sizeof(s_tpl));
        s_npal = g_npal;
        do_scan = s_active;  /* off = restore only */
        want_hook = s_active && s_mode == MODE_PER && g_hook_state != 1;   /* retried on every new per wish */
    }
    LeaveCriticalSection(&g_cmd_cs);
    if (want_hook) {
        /* install here, never from the command (that runs on the game's chat thread); idempotent */
        int ok;
        why[0] = 0;
        ok = bc2_smoke_hook_install(why, sizeof(why));
        EnterCriticalSection(&g_cmd_cs);
        g_hook_state = ok ? 1 : -1;
        lstrcpynA(g_hook_why, ok ? "" : why, sizeof(g_hook_why));
        LeaveCriticalSection(&g_cmd_cs);
        wsprintfA(line, "per hook %s%s%.60s", ok ? "installed" : "refused", why[0] ? ": " : "", why);
        slog(line);
    }
    if (g_level_scans_left == 2 && now - g_mgr_change_tick >= LEVEL_SCAN_DELAY_MS) { g_level_scans_left = 1; do_scan = 1; }
    else if (g_level_scans_left == 1 && now - g_mgr_change_tick >= LEVEL_SCAN_REPEAT_MS) { g_level_scans_left = 0; do_scan = 1; }
    if (s_active && mgr && g_nobjs == 0 && now - g_last_scan_tick >= PERIODIC_SCAN_MS)
        do_scan = 1;  /* nothing found yet (lazy allocation?) -> keep looking, slowly; never sweep once objects are known */
    if (mgr && bc2_fx_scan_wanted(now))
        do_scan = 1;  /* /fx: a rule or a list with nothing known yet */
    if (do_scan && mgr) {
        drop_dead();
        bc2_fx_scan_begin();
        found = scan_all();
        bc2_fx_scan_end();
        wsprintfA(line, "scan #%lu: %d new, %d known, %lu ms", (unsigned long)g_scan_count, found, g_nobjs, (unsigned long)g_last_scan_ms);
        slog(line);
    }
    if (mgr) {
        apply_all();
        bc2_fx_apply();
    }
    per_publish();   /* after scan / drop_dead / level change; no-op unless the set, palette or mode changed */
    LeaveCriticalSection(&g_cs);
}

static void status_line(char *out, int size)
{
    int i, k[5] = { 0, 0, 0, 0, 0 }, patched = 0, have = 0;
    char wish[32], tpl[96], one[24], per[80], mgrs[16];
    int active, norig = 0, per_mode;
    EnterCriticalSection(&g_cmd_cs);
    active = g_active;
    per_mode = g_active && g_mode == MODE_PER;
    lstrcpynA(wish, g_wish, sizeof(wish));
    if (g_mode == MODE_PER)
        sprintf_s(wish, sizeof(wish), "per %d", g_npal);
    else if (g_mode == MODE_DUO)
        _snprintf_s(wish, sizeof(wish), _TRUNCATE, "duo %s %s", g_wish, g_wish2);
    else if (g_mode != MODE_STATIC)
        sprintf_s(wish, sizeof(wish), "%s %.1fs%s%s", g_mode == MODE_RAINBOW ? "rainbow" : g_mode == MODE_PSYCHO ? "psycho" : "pulse",
                  g_period, g_mode == MODE_PULSE ? " " : "", g_mode == MODE_PULSE ? g_wish : "");
    tpl[0] = 0;
    for (i = 0; i < NTPL; i++) {
        if (isnan_(g_tpl[i])) { sprintf_s(one, sizeof(one), " %s=orig", TPL_FIELDS[i].tag); norig++; }
        else sprintf_s(one, sizeof(one), " %s=%.2g", TPL_FIELDS[i].tag, g_tpl[i]);
        strcat_s(tpl, sizeof(tpl), one);
    }
    if (norig == NTPL)
        lstrcpyA(tpl, " tpl orig");   /* chat line budget */
    per[0] = 0;
    if (per_mode) {
        /* c/r/f = hook calls while enabled / blocks given a clone / faults */
        if (g_hook_state == 1)
            _snprintf_s(per, sizeof(per), _TRUNCATE, " hook ok c%lu r%lu", (unsigned long)g_per_calls, (unsigned long)g_per_repl);
        else if (g_hook_state < 0)
            _snprintf_s(per, sizeof(per), _TRUNCATE, " hook refused: %.30s", g_hook_why);
        else
            lstrcpyA(per, " hook pending");
        if (g_hook_state == 1 && g_per_faults) {
            sprintf_s(one, sizeof(one), " f%lu", (unsigned long)g_per_faults);
            strcat_s(per, sizeof(per), one);
        }
    }
    LeaveCriticalSection(&g_cmd_cs);
    if (TryEnterCriticalSection(&g_cs)) {   /* never wait on a running scan from a game thread */
        for (i = 0; i < g_nobjs; i++) { k[g_objs[i].kind]++; patched += g_objs[i].patched; }
        have = g_nobjs;
        LeaveCriticalSection(&g_cs);
        /* one chat line is ~110 visible chars: keep it short (v/t/p = vapor/trail/projtrail objects, tpl = templates,
         * al/am/vp/no = alpha/ambient/vpl/normal template fields; per mode drops the manager pointer for the hook info) */
        if (per_mode) mgrs[0] = 0;
        else sprintf_s(mgrs, sizeof(mgrs), " mgr %08lx", (unsigned long)g_last_mgr);
        _snprintf_s(out, size, _TRUNCATE, "smoke: %s; %d obj (v%d t%d p%d tpl%d) patched %d;%s; scan %lu/%lums%s%s",
                    active ? wish : "off", have, k[1], k[2], k[3], k[4], patched, tpl,
                    (unsigned long)g_scan_count, (unsigned long)g_last_scan_ms, mgrs, per);
    } else
        _snprintf_s(out, size, _TRUNCATE, "smoke: %s; scan in progress, ask again in a few seconds%s", active ? wish : "off", per);
}

int bc2_smoke_command(const char *args, char *out, int out_size)
{
    char a[48], ini[MAX_PATH], *sp, *val;
    float rgb[3], f = 0.0f;
    int i;
    if (!g_cs_ready)
        bc2_smoke_init();
    strncpy_s(a, sizeof(a), args ? args : "", _TRUNCATE);
    while (a[0] && (a[strlen(a) - 1] == ' ' || a[strlen(a) - 1] == '\t'))
        a[strlen(a) - 1] = 0;
    ini_path(ini, MAX_PATH);
    if (!a[0] || _stricmp(a, "status") == 0) {
        status_line(out, out_size);
        return 1;
    }
    if (_stricmp(a, "halloween") == 0)   /* preset: young particles pumpkin orange, old ones blood red (persisted like any duo) */
        lstrcpyA(a, "duo pumpkin blood");
    if (_stricmp(a, "off") == 0) {
        EnterCriticalSection(&g_cmd_cs);
        g_active = 0;
        g_mode = MODE_STATIC;
        g_wish[0] = 0;
        g_pending = 1;
        LeaveCriticalSection(&g_cmd_cs);
        EnterCriticalSection(&g_per_cs);   /* per mode: stop handing out clones now; the hook stays installed */
        g_per_enabled = 0;
        LeaveCriticalSection(&g_per_cs);
        if (ini[0]) { WritePrivateProfileStringA("smoke", "color", "off", ini); WritePrivateProfileStringA("smoke", "mode", "static", ini); }
        lstrcpynA(out, "smoke: off, original greys + template fields restored on the next tick", out_size);
        return 1;
    }
    /* animation modes: "/smoke rainbow [s]", "/smoke psycho [s]", "/smoke pulse <color> [s]" */
    {
        char *w1 = a, *w2 = NULL, *w3 = NULL, sv[16];
        int mode = -1;
        w2 = strchr(w1, ' ');
        if (w2) { *w2++ = 0; while (*w2 == ' ') w2++; w3 = strchr(w2, ' '); if (w3) { *w3++ = 0; while (*w3 == ' ') w3++; } }
        if (_stricmp(w1, "rainbow") == 0) mode = MODE_RAINBOW;
        else if (_stricmp(w1, "psycho") == 0) mode = MODE_PSYCHO;
        else if (_stricmp(w1, "pulse") == 0) mode = MODE_PULSE;
        if (_stricmp(w1, "duo") == 0) {
            /* "/smoke duo <young colour> <old colour>": two fixed colours, no animation */
            float rgb2[3];
            if (!(w2 && bc2_smoke_parse_color(w2, rgb) && w3 && bc2_smoke_parse_color(w3, rgb2))) {
                lstrcpynA(out, "Usage: /smoke duo <young color> <old color>  (e.g. duo pumpkin blood)", out_size);
                return 1;
            }
            EnterCriticalSection(&g_cmd_cs);
            memcpy(g_rgb, rgb, 12);
            memcpy(g_rgb + 3, rgb2, 12);
            lstrcpynA(g_wish, w2, sizeof(g_wish));
            lstrcpynA(g_wish2, w3, sizeof(g_wish2));
            g_mode = MODE_DUO;
            g_active = 1;
            g_pending = 1;
            LeaveCriticalSection(&g_cmd_cs);
            if (ini[0]) {
                WritePrivateProfileStringA("smoke", "color", w2, ini);
                WritePrivateProfileStringA("smoke", "color2", w3, ini);
                WritePrivateProfileStringA("smoke", "mode", "duo", ini);
            }
            _snprintf_s(out, out_size, _TRUNCATE, "smoke: duo %s -> %s (young -> old particles; scanning on the next tick)", w2, w3);
            return 1;
        }
        if (_stricmp(w1, "per") == 0) {
            /* "/smoke per [N]": every grenade effect its own colour of an N-hue palette (r259 per-instance hook) */
            long np = PER_DEF_PAL;
            char *end = NULL;
            if (w2 && *w2) np = strtol(w2, &end, 10);
            if ((w2 && *w2 && (!end || *end)) || w3 || np < 2 || np > PER_MAX_PAL) {
                lstrcpynA(out, "Usage: /smoke per [colours 2..12, default 8]", out_size);
                return 1;
            }
            EnterCriticalSection(&g_cmd_cs);
            lstrcpynA(g_wish, "per", sizeof(g_wish));
            g_npal = (int)np;
            g_mode = MODE_PER;
            g_active = 1;
            g_pending = 1;
            LeaveCriticalSection(&g_cmd_cs);
            if (ini[0]) {
                WritePrivateProfileStringA("smoke", "color", "per", ini);
                WritePrivateProfileStringA("smoke", "mode", "per", ini);
                sprintf_s(sv, sizeof(sv), "%ld", np);
                WritePrivateProfileStringA("smoke", "per", sv, ini);
            }
            _snprintf_s(out, out_size, _TRUNCATE, "smoke: per grenade, %ld colours (hook + scan on the next tick; /smoke status)", np);
            return 1;
        }
        if (mode >= 0) {
            float period = mode == MODE_PULSE ? 2.0f : mode == MODE_RAINBOW ? 6.0f : 8.0f;
            const char *per = mode == MODE_PULSE ? w3 : w2;
            if (mode == MODE_PULSE && !(w2 && bc2_smoke_parse_color(w2, rgb))) {
                lstrcpynA(out, "Usage: /smoke pulse <color> [seconds]", out_size);
                return 1;
            }
            if (per && *per && !(parse_float(per, &f) && f >= 0.2f && f <= 600.0f)) {
                lstrcpynA(out, "Usage: /smoke rainbow|psycho [seconds 0.2..600], /smoke pulse <color> [seconds]", out_size);
                return 1;
            }
            if (per && *per) period = f;
            EnterCriticalSection(&g_cmd_cs);
            if (mode == MODE_PULSE) { memcpy(g_rgb, rgb, sizeof(rgb)); lstrcpynA(g_wish, w2, sizeof(g_wish)); }
            else lstrcpynA(g_wish, w1, sizeof(g_wish));
            g_mode = mode;
            g_period = period;
            g_active = 1;
            g_pending = 1;
            LeaveCriticalSection(&g_cmd_cs);
            if (ini[0]) {
                WritePrivateProfileStringA("smoke", "color", g_wish, ini);
                WritePrivateProfileStringA("smoke", "mode", w1, ini);
                sprintf_s(sv, sizeof(sv), "%.2f", period);
                WritePrivateProfileStringA("smoke", "period", sv, ini);
            }
            _snprintf_s(out, out_size, _TRUNCATE, "smoke: %s, period %.1f s (animating 20x/s on the known objects)", w1, period);
            return 1;
        }
        if (w2) { w2[-1] = ' '; if (w3) w3[-1] = ' '; }   /* not a mode: restore the line for the parsers below */
    }
    /* "/smoke alpha 0.3", "/smoke tpl ambient orig" */
    sp = strchr(a, ' ');
    if (sp) {
        char *field = a;
        *sp = 0;
        val = sp + 1;
        while (*val == ' ') val++;
        if (_stricmp(field, "tpl") == 0) {
            field = val;
            sp = strchr(field, ' ');
            if (!sp) { lstrcpynA(out, "Usage: /smoke tpl alpha|ambient|vpl|normal <0..1|orig>", out_size); return 1; }
            *sp = 0; val = sp + 1;
            while (*val == ' ') val++;
        }
        for (i = 0; i < NTPL; i++)
            if (_stricmp(field, TPL_FIELDS[i].name) == 0)
                break;
        if (i == NTPL || !(_stricmp(val, "orig") == 0 || parse_float(val, &f))) {
            lstrcpynA(out, "Usage: /smoke <color> | off | status | alpha|ambient|vpl|normal <0..1|orig>", out_size);
            return 1;
        }
        EnterCriticalSection(&g_cmd_cs);
        g_tpl[i] = _stricmp(val, "orig") == 0 ? nanf_() : f;
        g_pending = 1;
        LeaveCriticalSection(&g_cmd_cs);
        if (ini[0]) ini_write_tpl(ini);
        _snprintf_s(out, out_size, _TRUNCATE, "smoke: template %s = %s (applied with the colour on the next tick)", TPL_FIELDS[i].name, val);
        return 1;
    }
    if (bc2_smoke_parse_color(a, rgb)) {
        EnterCriticalSection(&g_cmd_cs);
        memcpy(g_rgb, rgb, sizeof(rgb));
        lstrcpynA(g_wish, a, sizeof(g_wish));
        g_mode = MODE_STATIC;
        g_active = 1;
        g_pending = 1;
        LeaveCriticalSection(&g_cmd_cs);
        if (ini[0]) { WritePrivateProfileStringA("smoke", "color", a, ini); WritePrivateProfileStringA("smoke", "mode", "static", ini); }
        _snprintf_s(out, out_size, _TRUNCATE, "smoke: %s (scanning on the next tick; /smoke status for counts)", a);
        return 1;
    }
    lstrcpynA(out, "Usage: /smoke <color>|#rrggbb | halloween | duo <c1> <c2> | per [N] | rainbow [s] | psycho [s] | pulse <color> [s] | off | status | alpha <0..1|orig>", out_size);
    return 1;
}
