/* Generic emitter recolour (/fx), see client_fx.h.
 *
 * Facts used (retail BFBC2Game.exe 3911fcc8..., r253 runtime RE; the Steam build's addresses come from bc2_exe(), no
 * new address is needed: the EmitterDocument and PolynomialColorInterpData vtables are already in both profiles):
 *  - EmitterDocument (vtable 0x0145AE6C): +0x0C Asset.Name (eastl string, begin char* first, the asset path, e.g.
 *    'FX/Weapons/Misc/SmokeGrenade_L/.../Em_SmokeGrenade_VaporExplotion_L_01'), +0x18 EmitterTemplateData*,
 *    +0x30 RootProcessor. Documents exist from level load on (templates are created lazily on the first spawn, so the
 *    document is the better handle for a list).
 *  - EmitterTemplateData (0xF0): +0xB4 RootProcessor. ProcessorData: +0x0C Pre (EvaluatorData*), +0x20 NextProcessor,
 *    +0x28 ProcessorType (PtUpdateColor 0x17, PtUpdateColorLeaf 0x18, PtUpdateTransparency 0x19, ...).
 *  - EvaluatorData: +0x14 EvaluatorType. PolynomialColorInterpData (vtable 0x0145BE04, type 0xB): +0x30 Color1 vec3
 *    (young end), +0x40 Color0 vec3 (old end), 4th lanes never written.
 * Every game read/write is under __try; writes re-validate the object (vtable, type, current bytes) first.
 */
#include <windows.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include "client_fx.h"
#include "client_chat.h"
#include "client_smoke.h"
#include "bc2_exe.h"

#ifndef BC2_CHAT_APPDIR
#define BC2_CHAT_APPDIR      "BC2Replay"      /* see client_chat.c */
#endif
#define VT_PCID              (bc2_exe()->vt_pcid)
#define OFF_DOC_NAME         0x0Cu
#define OFF_DOC_TPL          0x18u
#define OFF_DOC_ROOT         0x30u
#define OFF_TPL_ROOT         0xB4u
#define OFF_PROC_PRE         0x0Cu
#define OFF_PROC_NEXT        0x20u
#define OFF_PROC_TYPE        0x28u
#define OFF_EV_TYPE          0x14u
#define OFF_COLOR1           0x30u
#define OFF_COLOR0           0x40u
#define PT_UPDATE_COLOR      0x17u
#define PT_UPDATE_COLOR_LEAF 0x18u
#define PT_MAX_SANE          0x80u    /* processor types are a small enum; anything above = not a processor */
#define EV_PCID              0xBu
#define WALK_MAX             48
#define FX_MAX_TPL           1536
#define FX_MAX_OBJ           2048
#define FX_MAX_RULES         16
#define FX_PATH              160
#define FX_SHORT             48
#define FX_KEY               32
#define FX_LINE              90       /* visible chars of one chat line (the game cuts at ~98, live 2026-10-31) */
#define FX_RESCAN_MS         60000u
#define SMOKE_KEY            "smokegrenade"   /* left to /smoke, see client_fx.h */

typedef struct { DWORD doc; char path[FX_PATH]; char shortn[FX_SHORT]; int nobj, smoke; } fx_tpl;
typedef struct {
    DWORD addr; int tpl; DWORD ptype; int rule, dead;
    float orig1[3], orig0[3], cur1[3], cur0[3];
} fx_obj;
typedef struct { char key[FX_KEY]; char wish[24]; float rgb[3]; } fx_rule;

/* g_fx_cs: tables, held by the monitor thread for a whole scan and for an apply pass that writes; the command path
 * only TryEnters it. g_fx_cmd_cs: the rule mailbox, held for a few instructions. */
static CRITICAL_SECTION g_fx_cs, g_fx_cmd_cs;
static int g_fx_ready;
static fx_tpl g_tpl[FX_MAX_TPL];
static int g_ntpl, g_ndocs_seen, g_nsorted;
static short g_sorted[FX_MAX_TPL];   /* templates with colour objects, by short name (built in scan_end) */
static fx_obj g_obj[FX_MAX_OBJ];
static int g_nobj;
static int g_scan_new_from, g_logged_roots;
static DWORD g_fx_scans, g_fx_last_scan, g_fx_scan_t0;
static int g_ev_hist[16], g_ev_other;         /* evaluator types under colour processors (diagnostic, per level) */
static volatile LONG g_dirty;                 /* tables changed: apply must run */
/* mailbox */
static fx_rule g_rules[FX_MAX_RULES];
static int g_nrules, g_pending, g_force_scan;
/* tick snapshot */
static fx_rule s_rules[FX_MAX_RULES];
static int s_nrules;

static void flog(const char *text)
{
    char line[600];
    _snprintf_s(line, sizeof(line), _TRUNCATE, "fx %s", text);
    bc2_chat_log(line);
}

static void fx_ready(void)
{
    if (!g_fx_ready) {
        InitializeCriticalSection(&g_fx_cs);
        InitializeCriticalSection(&g_fx_cmd_cs);
        g_fx_ready = 1;
    }
}

/* ---- pure helpers ---- */
static int contains_lc(const char *hay_lc, const char *needle_lc)
{
    return strstr(hay_lc, needle_lc) != NULL;
}

static void lower(char *s)
{
    for (; *s; s++)
        if (*s >= 'A' && *s <= 'Z')
            *s = (char)(*s - 'A' + 'a');
}

void bc2_fx_short_name(const char *path, char *out, int size)
{
    const char *p, *last = path;
    if (size <= 0)
        return;
    for (p = path; *p; p++)
        if (*p == '/' || *p == '\\')
            last = p + 1;
    if (_strnicmp(last, "em_", 3) == 0 && last[3])
        last += 3;
    lstrcpynA(out, last, size);
    lower(out);
}

void bc2_fx_tint(const float wish[3], const float orig[3], float out[3])
{
    float v = orig[0];
    int i;
    if (orig[1] > v) v = orig[1];
    if (orig[2] > v) v = orig[2];
    if (v < 0.0f) v = 0.0f;
    for (i = 0; i < 3; i++) {
        float c = wish[i] * v;
        out[i] = c < 0.0f ? 0.0f : c;   /* no upper clamp: fire / flares are HDR (originals up to 20, live log) */
    }
}

int bc2_fx_key_ok(const char *key)
{
    static const char *const RESERVED[] = { "list", "status", "off", "help", "all" };
    size_t n = key ? strlen(key) : 0, i;
    if (n < 1 || n >= FX_KEY)
        return 0;
    for (i = 0; i < n; i++) {
        char c = key[i];
        if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' ||
              c == '.' || c == '/'))
            return 0;
    }
    for (i = 0; i < sizeof(RESERVED) / sizeof(RESERVED[0]); i++)
        if (_stricmp(key, RESERVED[i]) == 0)
            return 0;
    return 1;
}

/* ---- game memory ---- */
static int rd32(DWORD addr, DWORD *out)
{
    if (addr < 0x10000u)
        return 0;
    __try {
        *out = *(volatile DWORD *)(UINT_PTR)addr;
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

static int rd_bytes(DWORD addr, void *out, size_t n)
{
    if (addr < 0x10000u)
        return 0;
    __try {
        memcpy(out, (const void *)(UINT_PTR)addr, n);
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return 0;
    }
}

int bc2_fx_walk(DWORD root, bc2_fx_node *out, int max)
{
    DWORD p = root, seen[WALK_MAX];
    int n = 0, i;
    while (p && n < max && n < WALK_MAX) {
        bc2_fx_node *nd = &out[n];
        DWORD vt = 0;
        for (i = 0; i < n; i++)
            if (seen[i] == p)
                return -1;   /* cycle */
        seen[n] = p;
        memset(nd, 0, sizeof(*nd));
        nd->proc = p;
        if (!rd32(p + OFF_PROC_TYPE, &nd->ptype) || nd->ptype > PT_MAX_SANE || !rd32(p + OFF_PROC_PRE, &nd->ev))
            return -1;
        if (nd->ev) {
            if (!rd32(nd->ev, &vt) || !rd32(nd->ev + OFF_EV_TYPE, &nd->evtype))
                nd->ev = 0;   /* unreadable evaluator: keep the node, drop the evaluator */
            else
                nd->pcid = vt == VT_PCID && nd->evtype == EV_PCID;
        }
        n++;
        if (!rd32(p + OFF_PROC_NEXT, &p))
            return -1;
    }
    return n;
}

/* the asset path: printable ASCII, 3..FX_PATH-1 chars, NUL-terminated (the smoke scan's name check, generalised) */
static int rd_path(DWORD str_addr, char *out)
{
    DWORD p = 0;
    int i;
    out[0] = 0;
    if (!rd32(str_addr, &p) || p < 0x10000u)
        return 0;
    __try {
        for (i = 0; i < FX_PATH; i++) {
            char c = *(volatile char *)(UINT_PTR)(p + i);
            if (!c)
                break;
            if (c < 0x20 || c > 0x7e)
                return 0;
            out[i] = c;
        }
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        out[0] = 0;
        return 0;
    }
    if (i < 3 || i >= FX_PATH) {
        out[0] = 0;
        return 0;
    }
    out[i] = 0;
    return 1;
}

static int obj_alive(const fx_obj *o)
{
    unsigned char buf[0x50];
    DWORD vt, et;
    if (o->dead || !rd_bytes(o->addr, buf, sizeof(buf)))
        return 0;
    memcpy(&vt, buf, 4);
    memcpy(&et, buf + OFF_EV_TYPE, 4);
    return vt == VT_PCID && et == EV_PCID && memcmp(buf + OFF_COLOR1, o->cur1, 12) == 0 &&
           memcmp(buf + OFF_COLOR0, o->cur0, 12) == 0;
}

static int obj_write(fx_obj *o, const float c1[3], const float c0[3])
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

/* ---- scan (monitor thread, called from client_smoke.c) ---- */
void bc2_fx_level(DWORD mgr)
{
    if (!g_fx_ready)
        return;
    EnterCriticalSection(&g_fx_cs);
    g_ntpl = g_nobj = g_nsorted = g_ndocs_seen = 0;
    g_logged_roots = 0;
    memset(g_ev_hist, 0, sizeof(g_ev_hist));
    g_ev_other = 0;
    LeaveCriticalSection(&g_fx_cs);
    (void)mgr;
}

int bc2_fx_scan_wanted(DWORD now)
{
    int want = 0;
    if (!g_fx_ready)
        return 0;
    EnterCriticalSection(&g_fx_cmd_cs);
    if (g_force_scan) {
        g_force_scan = 0;
        want = 1;
    } else if (g_nrules && g_nsorted == 0 && now - g_fx_last_scan >= FX_RESCAN_MS)
        want = 1;   /* rules but nothing recolourable known yet: keep looking, slowly */
    LeaveCriticalSection(&g_fx_cmd_cs);
    return want;
}

void bc2_fx_scan_begin(void)
{
    int i, j = 0;
    if (!g_fx_ready)
        return;
    EnterCriticalSection(&g_fx_cs);   /* held until scan_end */
    g_fx_scan_t0 = GetTickCount();
    for (i = 0; i < g_nobj; i++)      /* forget objects that changed under us (freed / rewritten) */
        if (obj_alive(&g_obj[i]))
            g_obj[j++] = g_obj[i];
    g_nobj = j;
    g_scan_new_from = g_ntpl;
    g_ndocs_seen = 0;
    memset(g_ev_hist, 0, sizeof(g_ev_hist));   /* per scan: documents without colour objects are re-walked */
    g_ev_other = 0;
}

static int known_doc(DWORD doc)
{
    int i;
    for (i = 0; i < g_ntpl; i++)
        if (g_tpl[i].doc == doc)
            return 1;
    return 0;
}

static int known_obj(DWORD addr)
{
    int i;
    for (i = 0; i < g_nobj; i++)
        if (g_obj[i].addr == addr)
            return 1;
    return 0;
}

void bc2_fx_scan_doc(DWORD doc)
{
    char path[FX_PATH], lc[FX_PATH], line[200];
    bc2_fx_node nodes[WALK_MAX];
    DWORD data = 0, root = 0, droot = 0;
    int n, i, idx, ncol = 0;
    fx_tpl *t;
    if (!g_fx_ready)
        return;
    if (known_doc(doc)) {
        g_ndocs_seen++;
        return;
    }
    if (g_ntpl >= FX_MAX_TPL || !rd_path(doc + OFF_DOC_NAME, path) || !rd32(doc + OFF_DOC_TPL, &data) ||
        data < 0x10000u || !rd32(data + OFF_TPL_ROOT, &root))
        return;
    n = bc2_fx_walk(root, nodes, WALK_MAX);
    if (n < 0)
        return;
    g_ndocs_seen++;   /* a plausible document (re-walked on every scan when it has no colour object: cheap) */
    if (g_logged_roots < 3 && rd32(doc + OFF_DOC_ROOT, &droot)) {   /* does the document's RootProcessor agree? */
        g_logged_roots++;
        _snprintf_s(line, sizeof(line), _TRUNCATE, "doc %08lx root doc+30=%08lx tpl+b4=%08lx %s", (unsigned long)doc,
                    (unsigned long)droot, (unsigned long)root, droot == root ? "same" : "DIFFER");
        flog(line);
    }
    idx = g_ntpl;
    t = &g_tpl[g_ntpl];   /* committed below only if it has colour objects */
    memset(t, 0, sizeof(*t));
    t->doc = doc;
    lstrcpynA(lc, path, sizeof(lc));
    lower(lc);
    lstrcpynA(t->path, lc, sizeof(t->path));
    bc2_fx_short_name(path, t->shortn, sizeof(t->shortn));
    t->smoke = contains_lc(lc, SMOKE_KEY);
    for (i = 0; i < n; i++) {
        bc2_fx_node *nd = &nodes[i];
        fx_obj *o;
        float c[8];
        if (nd->ptype != PT_UPDATE_COLOR && nd->ptype != PT_UPDATE_COLOR_LEAF)
            continue;
        ncol++;
        if (nd->ev) {
            if (nd->evtype < 16) g_ev_hist[nd->evtype]++;
            else g_ev_other++;
        }
        if (!nd->pcid || g_nobj >= FX_MAX_OBJ || known_obj(nd->ev))
            continue;
        if (!rd_bytes(nd->ev + OFF_COLOR1, c, 32))
            continue;
        o = &g_obj[g_nobj++];
        memset(o, 0, sizeof(*o));
        o->addr = nd->ev;
        o->tpl = idx;
        o->ptype = nd->ptype;
        o->rule = -1;
        memcpy(o->orig1, c, 12);       /* +0x30 .. +0x3B */
        memcpy(o->orig0, c + 4, 12);   /* +0x40 .. +0x4B */
        memcpy(o->cur1, c, 12);
        memcpy(o->cur0, c + 4, 12);
        t->nobj++;
    }
    if (t->nobj)
        g_ntpl++;   /* only templates with a recolourable object are kept (the table stays small) */
    (void)ncol;
}

static int cmp_short(const void *a, const void *b)
{
    int r = strcmp(g_tpl[*(const short *)a].shortn, g_tpl[*(const short *)b].shortn);
    return r ? r : (int)*(const short *)a - (int)*(const short *)b;
}

void bc2_fx_scan_end(void)
{
    char line[400];
    int i, nnew = g_ntpl - g_scan_new_from;
    if (!g_fx_ready)
        return;
    g_nsorted = 0;
    for (i = 0; i < g_ntpl; i++)
        if (g_tpl[i].nobj && !g_tpl[i].smoke)
            g_sorted[g_nsorted++] = (short)i;
    qsort(g_sorted, (size_t)g_nsorted, sizeof(g_sorted[0]), cmp_short);
    for (i = g_scan_new_from; i < g_ntpl; i++) {   /* full list of the new templates to the log file */
        const fx_tpl *t = &g_tpl[i];
        int j, k = 0;
        char objs[200];
        objs[0] = 0;
        for (j = 0; j < g_nobj && k < 3; j++)
            if (g_obj[j].tpl == i) {
                char one[64];
                _snprintf_s(one, sizeof(one), _TRUNCATE, " [%lx %.2f,%.2f,%.2f/%.2f,%.2f,%.2f]", (unsigned long)g_obj[j].ptype,
                            g_obj[j].orig1[0], g_obj[j].orig1[1], g_obj[j].orig1[2], g_obj[j].orig0[0], g_obj[j].orig0[1],
                            g_obj[j].orig0[2]);
                strcat_s(objs, sizeof(objs), one);
                k++;
            }
        _snprintf_s(line, sizeof(line), _TRUNCATE, "tpl %s (%d obj%s)%s  %s", t->shortn, t->nobj, t->smoke ? ", smoke" : "",
                    objs, t->path);
        flog(line);
    }
    g_fx_scans++;
    g_fx_last_scan = GetTickCount();
    {
        char hist[160] = "";
        for (i = 0; i < 16; i++)
            if (g_ev_hist[i]) {
                char one[24];
                _snprintf_s(one, sizeof(one), _TRUNCATE, " %x:%d", i, g_ev_hist[i]);
                strcat_s(hist, sizeof(hist), one);
            }
        _snprintf_s(line, sizeof(line), _TRUNCATE,
                    "scan #%lu (%lu ms, whole RW scan incl. smoke): %d docs, %d new colour templates (%d kept), %d colour obj, %d listed; colour evaluator types:%s%s",
                    (unsigned long)g_fx_scans, (unsigned long)(g_fx_last_scan - g_fx_scan_t0), g_ndocs_seen, nnew, g_ntpl, g_nobj, g_nsorted, hist, g_ev_other ? " +other" : "");
        flog(line);
    }
    InterlockedExchange(&g_dirty, 1);
    LeaveCriticalSection(&g_fx_cs);   /* entered in scan_begin */
}

/* the rule for template t: the last one whose key is in its path; -1 = none */
static int rule_for(const fx_tpl *t, const fx_rule *rules, int n)
{
    int i;
    if (t->smoke)
        return -1;
    for (i = n - 1; i >= 0; i--)
        if (contains_lc(t->path, rules[i].key))
            return i;
    return -1;
}

void bc2_fx_apply(void)
{
    int i, n = 0, fail = 0, restored = 0;
    char line[160];
    if (!g_fx_ready)
        return;
    EnterCriticalSection(&g_fx_cmd_cs);
    if (g_pending) {
        g_pending = 0;
        memcpy(s_rules, g_rules, sizeof(s_rules));
        s_nrules = g_nrules;
        InterlockedExchange(&g_dirty, 1);
    }
    LeaveCriticalSection(&g_fx_cmd_cs);
    if (!InterlockedExchange(&g_dirty, 0))
        return;
    EnterCriticalSection(&g_fx_cs);
    for (i = 0; i < g_nobj; i++) {
        fx_obj *o = &g_obj[i];
        int r;
        if (o->dead)
            continue;
        r = rule_for(&g_tpl[o->tpl], s_rules, s_nrules);
        if (r >= 0) {
            float n1[3], n0[3];
            bc2_fx_tint(s_rules[r].rgb, o->orig1, n1);
            bc2_fx_tint(s_rules[r].rgb, o->orig0, n0);
            if (memcmp(n1, o->cur1, 12) == 0 && memcmp(n0, o->cur0, 12) == 0)
                continue;
            if (obj_write(o, n1, n0)) n++; else { o->dead = 1; fail++; }
        } else if (memcmp(o->orig1, o->cur1, 12) != 0 || memcmp(o->orig0, o->cur0, 12) != 0) {
            if (obj_write(o, o->orig1, o->orig0)) restored++; else { o->dead = 1; fail++; }
        }
    }
    LeaveCriticalSection(&g_fx_cs);
    if (n || restored || fail) {
        _snprintf_s(line, sizeof(line), _TRUNCATE, "apply: %d recoloured, %d restored, %d failed (%d rules, %d obj)", n,
                    restored, fail, s_nrules, g_nobj);
        flog(line);
    }
}

/* ---- ini ---- */
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

/* rewrite [fx] from a snapshot of the rules (taken under g_fx_cmd_cs; the file I/O runs outside it) */
static void ini_save(const fx_rule *rules, int nrules)
{
    char ini[MAX_PATH], buf[FX_MAX_RULES * (FX_KEY + 26) + 2];
    int i, pos = 0;
    ini_path(ini, MAX_PATH);
    if (!ini[0])
        return;
    if (!nrules) {
        WritePrivateProfileStringA("fx", NULL, NULL, ini);
        return;
    }
    for (i = 0; i < nrules; i++)
        pos += _snprintf_s(buf + pos, sizeof(buf) - pos, _TRUNCATE, "%s=%s", rules[i].key, rules[i].wish) + 1;
    buf[pos] = 0;
    WritePrivateProfileSectionA("fx", buf, ini);
}

void bc2_fx_init(void)
{
    char ini[MAX_PATH], buf[2048], *p;
    fx_ready();
    ini_path(ini, MAX_PATH);
    if (!ini[0])
        return;
    if (!GetPrivateProfileSectionA("fx", buf, sizeof(buf), ini))
        return;
    EnterCriticalSection(&g_fx_cmd_cs);
    for (p = buf; *p && g_nrules < FX_MAX_RULES; p += strlen(p) + 1) {
        char *eq = strchr(p, '=');
        fx_rule r;
        if (!eq)
            continue;
        *eq = 0;
        if (bc2_fx_key_ok(p) && bc2_smoke_parse_color(eq + 1, r.rgb)) {
            lstrcpynA(r.key, p, sizeof(r.key));
            lower(r.key);
            lstrcpynA(r.wish, eq + 1, sizeof(r.wish));
            g_rules[g_nrules++] = r;
        }
        *eq = '=';
    }
    g_pending = g_nrules > 0;
    LeaveCriticalSection(&g_fx_cmd_cs);
    if (g_nrules) {
        _snprintf_s(buf, sizeof(buf), _TRUNCATE, "ini: %d rule(s) loaded", g_nrules);
        flog(buf);
    }
}

/* ---- commands (chat thread: mailbox + TryEnter only) ---- */
static int matches_filter(const fx_tpl *t, const char *filter_lc)
{
    return !filter_lc[0] || contains_lc(t->path, filter_lc);
}

/* Line packer for multi-line replies ('\n' separated; client_chat.c shows one chat message per line). Items are
 * appended with ", " and wrapped at FX_LINE visible chars; returns 0 when the item did not fit in max_lines. */
typedef struct { char *out; int size, len, line_len, lines, max_lines; } fx_pack;

static int pack_add(fx_pack *p, const char *item)
{
    int n = (int)strlen(item), sep = p->line_len ? 2 : 0;
    if (p->line_len && p->line_len + sep + n > FX_LINE) {
        if (p->lines >= p->max_lines || p->len + 1 >= p->size)
            return 0;
        p->out[p->len++] = '\n';
        p->out[p->len] = 0;
        p->lines++;
        p->line_len = 0;
        sep = 0;
    }
    if (p->len + sep + n + 1 > p->size)
        return 0;
    if (sep) { memcpy(p->out + p->len, ", ", 2); p->len += 2; }
    memcpy(p->out + p->len, item, (size_t)n + 1);
    p->len += n;
    p->line_len += sep + n;
    return 1;
}

static void pack_init(fx_pack *p, char *out, int size, int max_lines, const char *head)
{
    p->out = out; p->size = size; p->len = 0; p->line_len = 0; p->lines = 1; p->max_lines = max_lines;
    out[0] = 0;
    lstrcpynA(out, head, size);
    p->len = p->line_len = lstrlenA(out);
}

/* unique short names matching the filter (g_sorted is ordered by short name) */
static int count_unique(const char *filter_lc)
{
    const char *prev = NULL;
    int i, total = 0;
    for (i = 0; i < g_nsorted; i++) {
        const fx_tpl *t = &g_tpl[g_sorted[i]];
        if (!matches_filter(t, filter_lc) || (prev && strcmp(prev, t->shortn) == 0))
            continue;
        prev = t->shortn;
        total++;
    }
    return total;
}

/* rule names that make useful groups (substrings of the asset paths); /fx list shows those with a count */
static const char *const GROUPS[] = {
    "muzz", "tracer", "trail", "blood", "impact", "spark", "fire", "flame", "flare", "glow", "explo", "vexp", "elec",
    "smoke", "debris", "water", "snow", "sand", "ambient", "destruction", "vehicles", "weapons/",
};

static void list_groups(char *out, int size)
{
    char head[80], item[40];
    fx_pack p;
    int i, total = count_unique("");
    _snprintf_s(head, sizeof(head), _TRUNCATE, "fx: %d emitters. Groups for /fx <group> <color>:", total);
    pack_init(&p, out, size, 4, head);
    p.line_len = FX_LINE;   /* groups start on the second line */
    for (i = 0; i < (int)(sizeof(GROUPS) / sizeof(GROUPS[0])); i++) {
        int n = count_unique(GROUPS[i]);
        if (!n)
            continue;
        _snprintf_s(item, sizeof(item), _TRUNCATE, "%s %d", GROUPS[i], n);
        if (!pack_add(&p, item))
            break;
    }
    pack_add(&p, "| names: /fx list <group>");
}

/* pack every matching name into pages of 3 chat lines; keeps the text of page want in buf; returns the page count */
#define LIST_LINES 3
static int pack_pages(const char *filter_lc, int want, char *buf, int size)
{
    const char *prev = NULL;
    int i, cur = 1, lines = 1, line_len = 0;
    buf[0] = 0;
    for (i = 0; i < g_nsorted; i++) {
        const fx_tpl *t = &g_tpl[g_sorted[i]];
        int n, sep;
        if (!matches_filter(t, filter_lc) || (prev && strcmp(prev, t->shortn) == 0))
            continue;
        prev = t->shortn;
        n = (int)strlen(t->shortn);
        sep = line_len ? 2 : 0;
        if (line_len && line_len + sep + n > FX_LINE) {
            sep = 0;
            line_len = 0;
            if (lines < LIST_LINES)
                lines++;
            else {   /* page full: the next page starts with this name */
                cur++;
                lines = 1;
            }
        }
        if (cur == want) {
            if (buf[0])
                strcat_s(buf, size, line_len ? ", " : "\n");
            strncat_s(buf, size, t->shortn, _TRUNCATE);
        }
        line_len += sep + n;
    }
    return cur;
}

static void list_names(const char *filter_lc, int page, char *out, int size)
{
    char buf[LIST_LINES * (FX_LINE + 2) + 8];
    int total = count_unique(filter_lc), pages;
    if (!total) {
        _snprintf_s(out, size, _TRUNCATE, "fx: no colour emitters%s%s known (%d docs this level; /fx status)",
                    filter_lc[0] ? " matching " : "", filter_lc, g_ndocs_seen);
        return;
    }
    pages = pack_pages(filter_lc, page, buf, sizeof(buf));
    if (page > pages)
        _snprintf_s(out, size, _TRUNCATE, "fx: %d emitters%s%s, only %d page(s)", total, filter_lc[0] ? " ~" : "", filter_lc, pages);
    else if (page < pages)
        _snprintf_s(out, size, _TRUNCATE, "fx: %d%s%s, page %d/%d, next: /fx list %s%s%d\n%s", total, filter_lc[0] ? " ~" : "",
                    filter_lc, page, pages, filter_lc, filter_lc[0] ? " " : "", page + 1, buf);
    else
        _snprintf_s(out, size, _TRUNCATE, "fx: %d%s%s, page %d/%d\n%s", total, filter_lc[0] ? " ~" : "", filter_lc, page, pages, buf);
}

static void status_line(char *out, int size)
{
    char rules[96] = "";
    int i, nr, have = -1, nobj = 0, patched = 0, ndocs = 0;
    EnterCriticalSection(&g_fx_cmd_cs);
    nr = g_nrules;
    for (i = 0; i < g_nrules; i++) {
        char one[64];
        _snprintf_s(one, sizeof(one), _TRUNCATE, "%s%s=%s", i ? " " : "", g_rules[i].key, g_rules[i].wish);
        strncat_s(rules, sizeof(rules), one, _TRUNCATE);
    }
    LeaveCriticalSection(&g_fx_cmd_cs);
    if (TryEnterCriticalSection(&g_fx_cs)) {
        have = g_nsorted;
        nobj = g_nobj;
        ndocs = g_ndocs_seen;
        for (i = 0; i < g_nobj; i++)
            patched += memcmp(g_obj[i].orig1, g_obj[i].cur1, 12) != 0 || memcmp(g_obj[i].orig0, g_obj[i].cur0, 12) != 0;
        LeaveCriticalSection(&g_fx_cs);
    }
    if (have < 0)
        _snprintf_s(out, size, _TRUNCATE, "fx: %d rule(s) %s; scan in progress, ask again in a few seconds", nr, rules);
    else
        _snprintf_s(out, size, _TRUNCATE, "fx: %d rule(s)%s%s; %d/%d emitters, %d obj, %d patched, scan %lu", nr,
                    nr ? ": " : "", rules, have, ndocs, nobj, patched, (unsigned long)g_fx_scans);
}

int bc2_fx_command(const char *args, char *out, int out_size)
{
    char a[96], *w1, *w2 = NULL, *w3 = NULL;
    float rgb[3];
    fx_rule snap[FX_MAX_RULES];
    int i, nsnap;
    fx_ready();
    strncpy_s(a, sizeof(a), args ? args : "", _TRUNCATE);
    w1 = a;
    while (*w1 == ' ' || *w1 == '\t') w1++;
    for (i = (int)strlen(w1) - 1; i >= 0 && (w1[i] == ' ' || w1[i] == '\t'); i--)
        w1[i] = 0;
    w2 = strchr(w1, ' ');
    if (w2) {
        *w2++ = 0;
        while (*w2 == ' ') w2++;
        w3 = strchr(w2, ' ');
        if (w3) {
            *w3++ = 0;
            while (*w3 == ' ') w3++;
        }
    }
    if (!w1[0] || _stricmp(w1, "status") == 0) {
        status_line(out, out_size);
        return 1;
    }
    if (_stricmp(w1, "list") == 0) {
        char filter[FX_KEY] = "";
        int page = 1;
        char *end;
        /* "/fx list", "/fx list 2", "/fx list all 2", "/fx list tracer", "/fx list tracer 2" */
        if (w2 && *w2) {
            long v = strtol(w2, &end, 10);
            if (!*end && !w3 && v >= 1) page = (int)v;
            else {
                lstrcpynA(filter, w2, sizeof(filter));
                lower(filter);
                if (w3 && *w3) {
                    v = strtol(w3, &end, 10);
                    if (*end || v < 1) { lstrcpynA(out, "Usage: /fx list [filter] [page]", out_size); return 1; }
                    page = (int)v;
                }
            }
        }
        if (!TryEnterCriticalSection(&g_fx_cs)) {
            lstrcpynA(out, "fx: scan in progress, ask again in a few seconds", out_size);
            return 1;
        }
        if (g_ndocs_seen == 0) {
            LeaveCriticalSection(&g_fx_cs);
            EnterCriticalSection(&g_fx_cmd_cs);
            g_force_scan = 1;
            LeaveCriticalSection(&g_fx_cmd_cs);
            lstrcpynA(out, "fx: nothing scanned yet, scanning now (in a level only); ask again in a few seconds", out_size);
            return 1;
        }
        if (!w2 || !*w2)
            list_groups(out, out_size);   /* /fx list = group counts; /fx list all|<filter> [page] = names */
        else
            list_names(_stricmp(filter, "all") == 0 ? "" : filter, page, out, out_size);
        LeaveCriticalSection(&g_fx_cs);
        return 1;
    }
    if (_stricmp(w1, "off") == 0 && !w2) {
        EnterCriticalSection(&g_fx_cmd_cs);
        g_nrules = 0;
        g_pending = 1;
        LeaveCriticalSection(&g_fx_cmd_cs);
        ini_save(NULL, 0);
        lstrcpynA(out, "fx: off, all emitters restored on the next tick", out_size);
        return 1;
    }
    if (w2 && *w2 && !w3 && bc2_fx_key_ok(w1)) {
        int off = _stricmp(w2, "off") == 0, j, found = -1;
        if (!off && !bc2_smoke_parse_color(w2, rgb)) {
            lstrcpynA(out, "fx: unknown colour (red green blue ... pumpkin ghost blood, or #rrggbb)", out_size);
            return 1;
        }
        lower(w1);
        EnterCriticalSection(&g_fx_cmd_cs);
        for (j = 0; j < g_nrules; j++)
            if (strcmp(g_rules[j].key, w1) == 0) { found = j; break; }
        if (found >= 0) {   /* drop the old entry; a re-set rule moves to the end (the last match wins) */
            memmove(&g_rules[found], &g_rules[found + 1], (size_t)(g_nrules - found - 1) * sizeof(g_rules[0]));
            g_nrules--;
        }
        if (!off && g_nrules < FX_MAX_RULES) {
            fx_rule *r = &g_rules[g_nrules++];
            lstrcpynA(r->key, w1, sizeof(r->key));
            lstrcpynA(r->wish, w2, sizeof(r->wish));
            memcpy(r->rgb, rgb, sizeof(rgb));
        } else if (!off) {
            LeaveCriticalSection(&g_fx_cmd_cs);
            lstrcpynA(out, "fx: rule table full (16), remove one with /fx <name> off", out_size);
            return 1;
        }
        g_pending = 1;
        if (!off && g_nsorted == 0)
            g_force_scan = 1;
        memcpy(snap, g_rules, sizeof(snap));
        nsnap = g_nrules;
        LeaveCriticalSection(&g_fx_cmd_cs);
        ini_save(snap, nsnap);
        if (off)
            _snprintf_s(out, out_size, _TRUNCATE, found >= 0 ? "fx: %s restored on the next tick" : "fx: no rule '%s'", w1);
        else
            _snprintf_s(out, out_size, _TRUNCATE, "fx: '%s' -> %s on the next tick (smoke grenades: /smoke; /fx status)", w1, w2);
        return 1;
    }
    lstrcpynA(out, "Usage: /fx list [filter] [page] | /fx <name> <color|#rrggbb|off> | /fx off | /fx status", out_size);
    return 1;
}
