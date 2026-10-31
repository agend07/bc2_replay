/* In-game chat commands + mute / hash filters (r255), see client_chat.h and README section 6.
 *
 * Ported from bc2_mute/src/chat_filter.c (msimg32.dll proxy, live-proven since 2026-10): instance-vtable clone
 * hooks on the retail BFBC2Game.exe (SHA-256 3911fcc8...), unchanged sites:
 *   UI event dispatcher   vtable 0x01440804 slot 1 = 0x009640D0  (incoming chat: cat 0x21, code 0x1A)
 *   chat input submit     vtable 0x0142A3D0 slot 0 = 0x00751380  (outgoing: __fastcall(this, edx, channel, eastl::string16*))
 *   (the HUD subobject hook of bc2_mute, vtable 0x0142B794, is NOT installed: it never sees chat code 0x1A, r254)
 * (addresses of the retail build; the Steam build's equivalents and the selection live in the profile table below,
 * see bc2_exe.h, r300)
 * Each hook swaps the INSTANCE's vtable pointer for a clone whose one slot points here; no .text byte changes, so the
 * recorder's site gate is unaffected. A 250 ms monitor thread re-attaches whenever the UI singleton (0x019A4CA8)
 * recreates the objects. Incoming chat message (code 0x1A, vtable 0x141C038, size 0x78, stack object built by
 * ClientEvent15_Execute; r254 bytes): +0x20/+0x24/+0x28 fixed_vector of 1..4 eastl::string8 TEXT SEGMENTS (inline at
 * +0x30), +0x60 float display seconds, +0x68 channel (0 team, 1 squad, 2 all, 4 help), +0x6C eastl::string8 sender
 * (message-owned copy, NUL-terminated UTF-8). Codes 0x11/0x12 are DIFFERENT objects (vtables 0x143B994/0x143B9A4)
 * whose +0x6C is not a char*: bc2_mute filtered them too and dereferenced that word as a name; this port leaves them alone.
 * /hash rewrites a hashed sender's text segments IN PLACE and same-length (letters/digits -> symbols, multi-byte UTF-8
 * and punctuation kept), which r254 verified as the one safe rewrite (buffers are message-owned, cap = len + 1).
 * Local feedback = a fake message with that layout pushed through the ORIGINAL dispatcher (never to the server).
 * Every game-memory read is under __try.
 */
#include <windows.h>
#include <string.h>
#include <stdio.h>
#include "client_chat.h"
#ifndef BC2_CHAT_STANDALONE
#include "client_recorder.h"   /* /rec: the shared dinput8 build only; the standalone msimg32 build has no recorder */
#endif
#include "version.h"
#include "client_smoke.h"
#include "client_fx.h"
#include "bc2_exe.h"

/* Build-time identity (the standalone bc2-chat-smoke build overrides all three, see its build.ps1):
 * BC2_CHAT_APPDIR = folder under %LOCALAPPDATA% for the lists, config.ini and logs; BC2_CHAT_TAG = sender of the
 * local replies; BC2_CHAT_LOG_PREFIX = OutputDebugString prefix and the list-file header. */
#ifndef BC2_CHAT_APPDIR
#define BC2_CHAT_APPDIR      "BC2Replay"
#endif
#ifndef BC2_CHAT_TAG
#define BC2_CHAT_TAG         "[dinput8]"
#endif
#ifndef BC2_CHAT_LOG_PREFIX
#define BC2_CHAT_LOG_PREFIX  "dinput8 proxy"
#endif

#define OFFSET_UI_EVENT_SUBOBJECT    0x00082AF8u   /* same in both builds (r300) */
#define OFFSET_UI_TEXTBOX_MGR_PTR    0x00082D0Cu

/* r300 per-build addresses, see bc2_exe.h. Column order = bc2_exe_profile. */
static const bc2_exe_profile EXE_PROFILES[] = {
    /* retail / Project Rome 3911fcc8... (bc2_mute + r252..r259 facts) */
    { "retail", 0x019A4CA8u, 0x01440830u, 0x01440804u, 0x009640D0u, 0x0142A3D0u, 0x00751380u, 0x0141C038u,
      0x0145BE04u, 0x0145AE6Cu, 0x01575A50u, 0x00B29362u, 0x00B26FB0u },
    /* Steam 22e96001... (SteamStub 2.1), mapped from the decrypted .text on 2026-10-31 */
    { "steam",  0x0198D9B8u, 0x01443398u, 0x0144336Cu, 0x0096BC60u, 0x0142CF38u, 0x00758F10u, 0x0141EBA0u,
      0x014B895Cu, 0x014B78D0u, 0x01BE13E0u, 0x0119C402u, 0x0119A050u },
};
static const bc2_exe_profile *volatile g_exe = &EXE_PROFILES[0];
static volatile LONG g_exe_known;

const bc2_exe_profile *bc2_exe(void) { return g_exe; }
int bc2_exe_known(void) { return g_exe_known != 0; }

const bc2_exe_profile *bc2_exe_detect(void)
{
    int i;
    for (i = 0; i < (int)(sizeof(EXE_PROFILES) / sizeof(EXE_PROFILES[0])); i++) {
        const bc2_exe_profile *p = &EXE_PROFILES[i];
        int ok = 0;
        __try {   /* .rdata only: valid on the Steam exe before SteamStub has decrypted .text */
            ok = *(DWORD *)(UINT_PTR)(p->vt_ui_event + 4) == p->fn_ui_event &&
                 *(DWORD *)(UINT_PTR)(p->vt_chat_input) == p->fn_chat_input;
        } __except (EXCEPTION_EXECUTE_HANDLER) {
            ok = 0;
        }
        if (ok) {
            g_exe = p;
            InterlockedExchange(&g_exe_known, 1);
            return p;
        }
    }
    return NULL;
}

#ifdef BC2_CHAT_SELFTEST
int bc2_exe_test_select(const char *name)
{
    int i;
    for (i = 0; i < (int)(sizeof(EXE_PROFILES) / sizeof(EXE_PROFILES[0])); i++)
        if (lstrcmpA(EXE_PROFILES[i].name, name) == 0) {
            g_exe = &EXE_PROFILES[i];
            return 1;
        }
    return 0;
}
#endif
#define MSG_SEG_BEGIN                0x20u
#define MSG_SEG_END                  0x24u
#define MSG_SENDER                   0x6Cu
#define MSG_SEG_MAX                  4

#define MAX_ENTRIES    256
#define MAX_NAME_LEN   64
#define POLL_MS        250u
#define ANIM_MS        50u    /* smoke animation cadence (r255d); the hook re-attach stays at POLL_MS */
#define RELOAD_MS      2000u
#define FEEDBACK_TAG   BC2_CHAT_TAG

typedef struct { const char *b, *e, *cap; } estring8;
typedef struct { const wchar_t *b, *e, *cap; } estring16;
typedef void (__fastcall *fn_ui_event)(void *self, void *edx, int cat, int type, void *a3, void *msg);
typedef void (__fastcall *fn_chat_submit)(void *self, void *edx, int channel, estring16 *text);

/* a file-backed name list (mute / hash), hot-reloaded every RELOAD_MS */
typedef struct {
    const char *file_name, *verb, *what, *undone;   /* default basename, "mute"/"hash", "Muted"/"Hashed", "Unmuted"/... */
    char file[MAX_PATH];
    char entries[MAX_ENTRIES][MAX_NAME_LEN];
    int count;
    FILETIME mtime;
    DWORD last_reload;
} namelist;

static namelist g_mute = { "muted_players.txt", "mute", "Muted", "Unmuted" };
static namelist g_hash = { "hashed_players.txt", "hash", "Hashed", "Unhashed" };
static CRITICAL_SECTION g_cs;
static int g_cs_ready;
static char g_log_path[MAX_PATH];
static volatile LONG g_stop;
static HANDLE g_thread;
static DWORD g_t0;

/* ---- log ---- */
static void chat_log(const char *text)
{
    char line[1100];
    DWORD written;
    HANDLE f;
    wsprintfA(line, BC2_CHAT_LOG_PREFIX ": chat +%lu %s\r\n", GetTickCount() - g_t0, text);
    OutputDebugStringA(line);
    if (!g_log_path[0])
        return;
    wsprintfA(line, "+%lu %s\r\n", GetTickCount() - g_t0, text);
    f = CreateFileA(g_log_path, FILE_APPEND_DATA, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_ALWAYS,
                    FILE_ATTRIBUTE_NORMAL, NULL);
    if (f == INVALID_HANDLE_VALUE)
        return;
    WriteFile(f, line, lstrlenA(line), &written, NULL);
    CloseHandle(f);
}

void bc2_chat_log(const char *text) { chat_log(text); }

static int base_dir(char *out, DWORD size)  /* %LOCALAPPDATA%\<BC2_CHAT_APPDIR> */
{
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", out, size);
    if (n == 0 || n + 16 >= size)
        return 0;
    lstrcatA(out, "\\" BC2_CHAT_APPDIR);
    CreateDirectoryA(out, NULL);
    return 1;
}

static void list_default_path(namelist *l)
{
    char dir[MAX_PATH];
    if (!l->file[0] && base_dir(dir, MAX_PATH - 40)) {
        lstrcpyA(l->file, dir);
        lstrcatA(l->file, "\\");
        lstrcatA(l->file, l->file_name);
    }
}

static void paths_init(void)
{
    char dir[MAX_PATH], name[40];
    g_t0 = GetTickCount();
    g_log_path[0] = 0;
    list_default_path(&g_mute);
    list_default_path(&g_hash);
    if (base_dir(dir, MAX_PATH - 64)) {
        lstrcatA(dir, "\\logs");
        CreateDirectoryA(dir, NULL);
        wsprintfA(name, "\\chat_%lu.log", GetCurrentProcessId());
        if (lstrlenA(dir) + lstrlenA(name) < (int)sizeof(g_log_path)) {
            lstrcpyA(g_log_path, dir);
            lstrcatA(g_log_path, name);
        }
    }
}

int bc2_client_chat_enabled(void)
{
    char ini[MAX_PATH], value[16];
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", ini, MAX_PATH - 32);
    if (n == 0 || n >= MAX_PATH - 32)
        return 1;
    lstrcatA(ini, "\\" BC2_CHAT_APPDIR "\\config.ini");
    GetPrivateProfileStringA("dll", "chat", "on", value, sizeof(value), ini);
    return lstrcmpiA(value, "off") != 0;
}

/* ---- name lists (file-backed, hot reload) ---- */
static void trim(char *s)
{
    char *p = s;
    size_t n;
    while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n')
        p++;
    if (p != s)
        memmove(s, p, strlen(p) + 1);
    n = strlen(s);
    while (n && (s[n - 1] == ' ' || s[n - 1] == '\t' || s[n - 1] == '\r' || s[n - 1] == '\n'))
        s[--n] = 0;
}

static int contains_ci(const char *hay, const char *needle)
{
    size_t n = needle ? strlen(needle) : 0;
    const char *p;
    if (!hay || !n)
        return 0;
    for (p = hay; *p; p++)
        if (_strnicmp(p, needle, n) == 0)
            return 1;
    return 0;
}

static void lock(void)   { if (g_cs_ready) EnterCriticalSection(&g_cs); }
static void unlock(void) { if (g_cs_ready) LeaveCriticalSection(&g_cs); }

static void write_header(FILE *f, const namelist *l)
{
    fprintf(f, "# BC2 " BC2_CHAT_LOG_PREFIX " - %s players / keywords (one per line, case-insensitive substring)\n", l->what);
    fprintf(f, "# In-game: /%s <name>, /un%s <name>, /%slist\n", l->verb, l->verb, l->verb);
}

static void load_locked(namelist *l, int force)
{
    WIN32_FILE_ATTRIBUTE_DATA fad;
    FILE *f = NULL;
    char line[256];
    if (!l->file[0])
        return;
    if (!GetFileAttributesExA(l->file, GetFileExInfoStandard, &fad)) {
        if (force && fopen_s(&f, l->file, "w") == 0 && f) {
            write_header(f, l);
            fclose(f);
        }
        l->count = 0;
        return;
    }
    if (!force && fad.ftLastWriteTime.dwLowDateTime == l->mtime.dwLowDateTime &&
        fad.ftLastWriteTime.dwHighDateTime == l->mtime.dwHighDateTime)
        return;
    l->mtime = fad.ftLastWriteTime;
    if (fopen_s(&f, l->file, "r") != 0 || !f)
        return;
    l->count = 0;
    while (fgets(line, sizeof(line), f)) {
        trim(line);
        if (!line[0] || line[0] == '#' || line[0] == ';')
            continue;
        if (l->count < MAX_ENTRIES)
            strncpy_s(l->entries[l->count++], MAX_NAME_LEN, line, _TRUNCATE);
    }
    fclose(f);
}

static void save_locked(namelist *l)
{
    FILE *f = NULL;
    WIN32_FILE_ATTRIBUTE_DATA fad;
    int i;
    if (!l->file[0] || fopen_s(&f, l->file, "w") != 0 || !f)
        return;
    write_header(f, l);
    for (i = 0; i < l->count; i++)
        fprintf(f, "%s\n", l->entries[i]);
    fclose(f);
    if (GetFileAttributesExA(l->file, GetFileExInfoStandard, &fad))
        l->mtime = fad.ftLastWriteTime;
}

static void maybe_reload_locked(namelist *l)
{
    DWORD now = GetTickCount();
    if (now - l->last_reload >= RELOAD_MS) {
        l->last_reload = now;
        load_locked(l, 0);
    }
}

static void list_set_file(namelist *l, const char *path)
{
    lstrcpynA(l->file, path ? path : "", MAX_PATH);
    l->mtime.dwLowDateTime = l->mtime.dwHighDateTime = 0;
    l->count = 0;
    l->last_reload = 0;
}

static int list_matches(namelist *l, const char *a, const char *b)
{
    int i, hit = 0;
    lock();
    maybe_reload_locked(l);
    for (i = 0; i < l->count && !hit; i++)
        hit = l->entries[i][0] && (contains_ci(a, l->entries[i]) || contains_ci(b, l->entries[i]));
    unlock();
    return hit;
}

void bc2_chat_set_mute_file(const char *path) { list_set_file(&g_mute, path); }
void bc2_chat_set_hash_file(const char *path) { list_set_file(&g_hash, path); }
int bc2_chat_is_muted(const char *sender, const char *first_word) { return list_matches(&g_mute, sender, first_word); }
int bc2_chat_is_hashed(const char *sender) { return list_matches(&g_hash, sender, NULL); }

/* r243 section 5 transform: same length; ASCII letters/digits -> symbol by (byte + position) % 8; everything else
 * (spaces, punctuation, every byte of a multi-byte UTF-8 sequence) unchanged, so code points stay intact. */
void bc2_chat_hash_text(char *s, unsigned len)
{
    static const char SYM[8] = { '#', '$', '%', '&', '@', '!', '*', '?' };
    unsigned i;
    for (i = 0; i < len; i++) {
        unsigned char c = (unsigned char)s[i];
        if ((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'))
            s[i] = SYM[(c + i) & 7];
    }
}

/* ---- commands (pure) ---- */
static int cmd_is(const char *line, const char *name, const char **args)
{
    size_t n = strlen(name);
    if (_strnicmp(line, name, n) != 0 || (line[n] && line[n] != ' ' && line[n] != '\t'))
        return 0;
    *args = line + n;
    while (**args == ' ' || **args == '\t')
        (*args)++;
    return 1;
}

static int cmd_add(namelist *l, const char *arg, char *out, int size)
{
    char name[MAX_NAME_LEN];
    int i, exists = 0;
    strncpy_s(name, sizeof(name), arg, _TRUNCATE);
    trim(name);
    if (!name[0]) {
        _snprintf_s(out, size, _TRUNCATE, "Usage: /%s <player_or_word>", l->verb);
        return 1;
    }
    lock();
    load_locked(l, 0);
    for (i = 0; i < l->count; i++)
        if (_stricmp(l->entries[i], name) == 0)
            exists = 1;
    if (exists)
        _snprintf_s(out, size, _TRUNCATE, "Already %s: %s", l->what, name);
    else if (l->count < MAX_ENTRIES) {
        strncpy_s(l->entries[l->count++], MAX_NAME_LEN, name, _TRUNCATE);
        save_locked(l);
        _snprintf_s(out, size, _TRUNCATE, "%s: %s", l->what, name);
    } else
        _snprintf_s(out, size, _TRUNCATE, "%s list is full", l->what);
    unlock();
    return 1;
}

static int cmd_remove(namelist *l, const char *arg, char *out, int size)
{
    char name[MAX_NAME_LEN];
    int i, removed = 0;
    strncpy_s(name, sizeof(name), arg, _TRUNCATE);
    trim(name);
    if (!name[0]) {
        _snprintf_s(out, size, _TRUNCATE, "Usage: /un%s <player_or_word>", l->verb);
        return 1;
    }
    lock();
    load_locked(l, 0);
    for (i = 0; i < l->count;) {
        if (_stricmp(l->entries[i], name) == 0 || contains_ci(l->entries[i], name)) {
            memmove(l->entries[i], l->entries[i + 1], (size_t)(l->count - 1 - i) * MAX_NAME_LEN);
            l->count--;
            removed++;
        } else
            i++;
    }
    if (removed)
        save_locked(l);
    unlock();
    if (removed)
        _snprintf_s(out, size, _TRUNCATE, "%s: %s (%d)", l->undone, name, removed);
    else
        _snprintf_s(out, size, _TRUNCATE, "Not in %s list: %s", l->verb, name);
    return 1;
}

static int cmd_list(namelist *l, char *out, int size)
{
    int i;
    lock();
    load_locked(l, 0);
    if (!l->count)
        _snprintf_s(out, size, _TRUNCATE, "%s list is empty", l->what);
    else {
        _snprintf_s(out, size, _TRUNCATE, "%s (%d): ", l->what, l->count);
        for (i = 0; i < l->count; i++) {
            if (i)
                strncat_s(out, size, ", ", _TRUNCATE);
            strncat_s(out, size, l->entries[i], _TRUNCATE);
            if (strlen(out) > (size_t)size - 40) {
                strncat_s(out, size, "...", _TRUNCATE);
                break;
            }
        }
    }
    unlock();
    return 1;
}

#ifndef BC2_CHAT_STANDALONE
static int cmd_rec(const char *arg, char *out, int size)
{
    char a[16];
    strncpy_s(a, sizeof(a), arg, _TRUNCATE);
    trim(a);
    if (!a[0] || _stricmp(a, "status") == 0)
        return bc2_client_recorder_status(out, size), 1;
    if (_stricmp(a, "off") == 0 || _stricmp(a, "on") == 0) {
        int off = _stricmp(a, "off") == 0;
        if (!bc2_client_recorder_pause(off))
            lstrcpynA(out, "rec: recorder not installed in this session", size);
        else
            lstrcpynA(out, off ? "rec: paused (records discarded until /rec on)" : "rec: recording (new file from here)", size);
        return 1;
    }
    lstrcpynA(out, "Usage: /rec status | /rec off | /rec on", size);
    return 1;
}
#define HELP_REC "/rec status|off|on  "
#else
#define HELP_REC ""
#endif

int bc2_chat_command(const char *line, char *out, int size)
{
    const char *args;
    if (!line || !out || size < 16)
        return 0;
    out[0] = 0;
    while (*line == ' ' || *line == '\t')
        line++;
    if (line[0] != '/')
        return 0;
    if (cmd_is(line, "/mute", &args))     return cmd_add(&g_mute, args, out, size);
    if (cmd_is(line, "/unmute", &args))   return cmd_remove(&g_mute, args, out, size);
    if (cmd_is(line, "/mutelist", &args) || cmd_is(line, "/muted", &args)) return cmd_list(&g_mute, out, size);
    if (cmd_is(line, "/hash", &args))     return cmd_add(&g_hash, args, out, size);
    if (cmd_is(line, "/unhash", &args))   return cmd_remove(&g_hash, args, out, size);
    if (cmd_is(line, "/hashlist", &args) || cmd_is(line, "/hashed", &args)) return cmd_list(&g_hash, out, size);
#ifndef BC2_CHAT_STANDALONE
    if (cmd_is(line, "/rec", &args))      return cmd_rec(args, out, size);
#endif
    if (cmd_is(line, "/smoke", &args))    return bc2_smoke_command(args, out, size);
    if (cmd_is(line, "/fx", &args))       return bc2_fx_command(args, out, size);
    if (cmd_is(line, "/help", &args)) {
        lstrcpynA(out, "/mute /unmute /mutelist  /hash /unhash /hashlist  " HELP_REC "/smoke <color|halloween|per|off>  /fx list|<name> <color>", size);
        return 1;
    }
    return 0;  /* unknown slash text goes to the server as typed */
}

/* ---- hooks (game) ---- */
#ifndef BC2_CHAT_SELFTEST
static fn_ui_event g_orig_ui_event;      /* set from the detected profile before any hook goes in */
static fn_chat_submit g_orig_submit;
static DWORD g_clone_ui[8], g_clone_submit[8];

static void show_local(const char *text)
{
    unsigned char msg[0x90];
    estring8 word, sender;
    estring8 words[1];
    DWORD ui_root = 0;
    __try {
        ui_root = *(DWORD *)(UINT_PTR)bc2_exe()->ui_singleton_ptr;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        ui_root = 0;
    }
    if (!ui_root)
        return;
    memset(msg, 0, sizeof(msg));
    word.b = text; word.e = text + strlen(text); word.cap = word.e + 1;
    sender.b = FEEDBACK_TAG; sender.e = FEEDBACK_TAG + sizeof(FEEDBACK_TAG) - 1; sender.cap = sender.e + 1;
    words[0] = word;
    *(DWORD *)(msg + 0x00) = bc2_exe()->vt_msg_chat;
    *(DWORD *)(msg + MSG_SEG_BEGIN) = (DWORD)(UINT_PTR)&words[0];
    *(DWORD *)(msg + MSG_SEG_END) = (DWORD)(UINT_PTR)&words[1];
    *(DWORD *)(msg + 0x28) = (DWORD)(UINT_PTR)&words[1];
    *(float *)(msg + 0x60) = 6.0f;
    *(DWORD *)(msg + 0x68) = 2;  /* channel all */
    *(estring8 *)(msg + MSG_SENDER) = sender;
    __try {
        g_orig_ui_event((void *)(UINT_PTR)(ui_root + OFFSET_UI_EVENT_SUBOBJECT), NULL, 0x21, 0x1A, NULL, msg);
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        chat_log("show_local: exception in the UI dispatcher");
    }
}

/* sender / first segment of an incoming 0x1A message copied out under __try; 0 on any fault */
static int msg_fields(void *m, char *sender, int sender_size, char *first, int first_size)
{
    sender[0] = first[0] = 0;
    __try {
        DWORD vb = *(DWORD *)((BYTE *)m + MSG_SEG_BEGIN), ve = *(DWORD *)((BYTE *)m + MSG_SEG_END);
        const char *s = *(const char **)((BYTE *)m + MSG_SENDER);
        if (!s)
            return 0;
        lstrcpynA(sender, s, sender_size);
        if (vb && ve > vb && ve - vb <= MSG_SEG_MAX * sizeof(estring8)) {
            const char *w = *(const char **)(UINT_PTR)vb;
            if (w)
                lstrcpynA(first, w, first_size);
        }
        return 1;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        sender[0] = first[0] = 0;
        return 0;
    }
}

/* /hash: rewrite every text segment in place (same length). Returns segments rewritten, -1 on a fault. */
static int msg_hash_segments(void *m)
{
    int n = 0;
    __try {
        DWORD vb = *(DWORD *)((BYTE *)m + MSG_SEG_BEGIN), ve = *(DWORD *)((BYTE *)m + MSG_SEG_END);
        estring8 *seg = (estring8 *)(UINT_PTR)vb;
        if (!vb || ve <= vb || ve - vb > MSG_SEG_MAX * sizeof(estring8))
            return 0;
        for (; (DWORD)(UINT_PTR)seg < ve; seg++) {
            if (seg->b && seg->e > seg->b && seg->e - seg->b < 4096 && seg->cap > seg->e) {
                bc2_chat_hash_text((char *)seg->b, (unsigned)(seg->e - seg->b));
                n++;
            }
        }
        return n;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return -1;
    }
}

static void __fastcall hook_ui_event(void *self, void *edx, int cat, int type, void *a3, void *m)
{
    char sender[MAX_NAME_LEN], first[128], line[240];
    (void)edx;
    if (cat == 0x21 && type == 0x1A && m && msg_fields(m, sender, sizeof(sender), first, sizeof(first)) &&
        sender[0] && strcmp(sender, FEEDBACK_TAG) != 0) {
        if (bc2_chat_is_muted(sender, first)) {
            wsprintfA(line, "blocked chat from '%s'", sender);
            chat_log(line);
            return;
        }
        if (bc2_chat_is_hashed(sender)) {
            int n = msg_hash_segments(m);
            wsprintfA(line, "hashed %d segment(s) from '%s'", n, sender);
            chat_log(line);
        }
    }
    g_orig_ui_event(self, NULL, cat, type, a3, m);
}

static void __fastcall hook_submit(void *self, void *edx, int channel, estring16 *text)
{
    char utf8[256], reply[384];
    int n = 0;
    (void)edx;
    utf8[0] = 0;
    if (text) {
        __try {
            const wchar_t *b = text->b, *e = text->e;
            if (b && e > b && e - b < 512)
                n = WideCharToMultiByte(CP_UTF8, 0, b, (int)(e - b), utf8, sizeof(utf8) - 1, NULL, NULL);
        } __except (EXCEPTION_EXECUTE_HANDLER) {
            n = 0;
        }
    }
    utf8[n > 0 ? n : 0] = 0;
    if (n > 0 && bc2_chat_command(utf8, reply, sizeof(reply))) {
        char line[520], *p, *nl;
        int i;
        wsprintfA(line, "cmd '%.120s' -> '%.380s'", utf8, reply);
        for (i = 0; line[i]; i++)
            if (line[i] == '\n') line[i] = '|';
        chat_log(line);
        /* a multi-line reply ('\n' separated, /fx list) = one local chat message per line, in order */
        for (p = reply; *p; p = nl ? nl + 1 : p + strlen(p)) {
            nl = strchr(p, '\n');
            if (nl) *nl = 0;
            if (*p) show_local(p);
            if (!nl) break;
        }
        return;  /* consumed locally, never sent */
    }
    g_orig_submit(self, NULL, channel, text);
}

static void prepare_clones(void)
{
    /* each clone copies RTTI (-4) + 5 slots; index 0 = RTTI, index 1 = slot 0 */
    const bc2_exe_profile *x = bc2_exe();
    g_orig_ui_event = (fn_ui_event)(UINT_PTR)x->fn_ui_event;
    g_orig_submit = (fn_chat_submit)(UINT_PTR)x->fn_chat_input;
    memcpy(g_clone_ui, (const void *)(UINT_PTR)(x->vt_ui_event - 4), 6 * sizeof(DWORD));
    g_clone_ui[2] = (DWORD)(UINT_PTR)hook_ui_event;
    memcpy(g_clone_submit, (const void *)(UINT_PTR)(x->vt_chat_input - 4), 6 * sizeof(DWORD));
    g_clone_submit[1] = (DWORD)(UINT_PTR)hook_submit;
}

static void attach_pass(int *logged_ui, int *logged_in)
{
    static const int channels[4] = { 3, 5, 6, 7 };
    char line[160];
    int i;
    const bc2_exe_profile *x = bc2_exe();
    __try {
        DWORD ui_root = *(DWORD *)(UINT_PTR)x->ui_singleton_ptr, mgr;
        DWORD *vt;
        if (!ui_root || *(DWORD *)(UINT_PTR)ui_root != x->vt_ui_root)
            return;
        vt = (DWORD *)(UINT_PTR)(ui_root + OFFSET_UI_EVENT_SUBOBJECT);
        if (*vt == x->vt_ui_event) {
            InterlockedExchange((volatile LONG *)vt, (LONG)(UINT_PTR)&g_clone_ui[1]);
            if (!*logged_ui) {
                *logged_ui = 1;
                wsprintfA(line, "hook ui-event on %08lx", (unsigned long)(UINT_PTR)vt);
                chat_log(line);
            }
        }
        mgr = *(DWORD *)(UINT_PTR)(ui_root + OFFSET_UI_TEXTBOX_MGR_PTR);
        if (mgr)
            for (i = 0; i < 4; i++) {
                DWORD handler = *(DWORD *)(UINT_PTR)(mgr + channels[i] * 4);
                if (!handler)
                    continue;
                vt = (DWORD *)(UINT_PTR)handler;
                if (*vt == x->vt_chat_input) {
                    InterlockedExchange((volatile LONG *)vt, (LONG)(UINT_PTR)&g_clone_submit[1]);
                    if (!*logged_in) {
                        *logged_in = 1;
                        wsprintfA(line, "hook chat-submit on handler %08lx (channel %d)", (unsigned long)handler, channels[i]);
                        chat_log(line);
                    }
                }
            }
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        /* transient UI teardown */
    }
}

static DWORD WINAPI monitor_thread(LPVOID arg)
{
    int logged_ui = 0, logged_in = 0;
    const bc2_exe_profile *x;
    char line[120];
    DWORD stamp = 0;
    (void)arg;
    Sleep(500);
    __try {   /* PE TimeDateStamp of the exe in memory (headers are never encrypted): for bug reports */
        const BYTE *mz = (const BYTE *)GetModuleHandleA(NULL);
        stamp = *(const DWORD *)(mz + *(const LONG *)(mz + 0x3C) + 8);
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        stamp = 0;
    }
    x = bc2_exe_detect();
    if (!x) {
        wsprintfA(line, "unknown exe (PE stamp %08lx, neither retail nor Steam); chat hooks inactive", (unsigned long)stamp);
        chat_log(line);
        return 0;
    }
    wsprintfA(line, "exe profile: %s (PE stamp %08lx)", x->name, (unsigned long)stamp);
    chat_log(line);
    prepare_clones();
    lock();
    load_locked(&g_mute, 1);
    load_locked(&g_hash, 1);
    unlock();
    chat_log("monitor running (250 ms); mute/hash files loaded");
    bc2_smoke_init();
    bc2_fx_init();
    while (!g_stop) {
        unsigned i;
        attach_pass(&logged_ui, &logged_in);
        for (i = 0; i < POLL_MS / ANIM_MS && !g_stop; i++) {
            bc2_smoke_tick();  /* scans, patches and the 20 Hz colour animation run here, never on a game thread */
            Sleep(ANIM_MS);
        }
    }
    return 0;
}

void bc2_client_chat_attach(void)
{
    if (!g_cs_ready) {
        InitializeCriticalSection(&g_cs);
        g_cs_ready = 1;
    }
    paths_init();
    chat_log("version " BC2_VERSION_STRING);  /* r266: first line of every chat_<pid>.log */
    if (!bc2_client_chat_enabled()) {
        chat_log("off (ini chat=off); nothing installed");
        return;
    }
    g_stop = 0;
    g_thread = CreateThread(NULL, 0, monitor_thread, NULL, 0, NULL);
    chat_log(g_thread ? "monitor thread started" : "monitor thread FAILED");
}

void bc2_client_chat_detach(void)
{
    g_stop = 1;
}
#else
void bc2_client_chat_attach(void) {}
void bc2_client_chat_detach(void) {}
void bc2_chat_selftest_init(void)
{
    if (!g_cs_ready) {
        InitializeCriticalSection(&g_cs);
        g_cs_ready = 1;
    }
}
#endif
