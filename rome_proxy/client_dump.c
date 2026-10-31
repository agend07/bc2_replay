/* Crash minidump for BFBC2Game.exe (r263). See client_dump.h.
 * The filter itself uses no CRT (wsprintfA / CreateFileA / WriteFile / LoadLibraryA only), static buffers, an
 * interlocked busy flag (dbghelp is not re-entrant; a second concurrent crash just chains) and one __try around
 * the whole body. dbghelp.dll is loaded from the system directory on first use, never from the game folder. */
#include <windows.h>
#include <dbghelp.h>
#include <string.h>
#include "client_dump.h"
#include "version.h"

typedef BOOL (WINAPI *minidump_fn)(HANDLE, DWORD, HANDLE, MINIDUMP_TYPE, PMINIDUMP_EXCEPTION_INFORMATION,
                                   PMINIDUMP_USER_STREAM_INFORMATION, PMINIDUMP_CALLBACK_INFORMATION);

static char g_dir[MAX_PATH];        /* dump directory, no trailing backslash */
static char g_log[MAX_PATH];        /* log file; "" = none */
static char g_exe[64];              /* exe base name without extension */
static char g_last[MAX_PATH];       /* last dump written */
static char g_line[MAX_PATH + 160]; /* filter scratch (static: no stack growth in the filter) */
static DWORD g_last_bytes, g_t0;
static LONG g_busy, g_calls, g_reasserts, g_stop, g_installed, g_reassert_logs;
static LPTOP_LEVEL_EXCEPTION_FILTER g_prev;
static HANDLE g_thread;

/* ---- paths / log ---- */
static int local_appdata(char *out, DWORD size)
{
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", out, size);
    return n > 0 && n < size;
}

static void dump_log(const char *text)
{
    char line[MAX_PATH + 200];
    DWORD ms = GetTickCount() - g_t0, written;
    HANDLE f;
    wsprintfA(line, "dinput8 proxy: dump +%lu %s\r\n", ms, text);
    OutputDebugStringA(line);
    if (!g_log[0])
        return;
    wsprintfA(line, "+%lu %s\r\n", ms, text);
    f = CreateFileA(g_log, FILE_APPEND_DATA, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL,
                    OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (f == INVALID_HANDLE_VALUE)
        return;
    WriteFile(f, line, lstrlenA(line), &written, NULL);
    CloseHandle(f);
}

void bc2_dump_set_dir(const char *dir)
{
    g_dir[0] = 0;
    if (dir)
        lstrcpynA(g_dir, dir, sizeof(g_dir));
}

void bc2_dump_set_log(const char *path)
{
    g_log[0] = 0;
    if (path)
        lstrcpynA(g_log, path, sizeof(g_log));
}

/* Default dir/log under %LOCALAPPDATA%\BC2Replay (only for the members still empty). */
static void paths_init(void)
{
    char base[MAX_PATH], name[48], *p, *dot;
    DWORD n;
    g_t0 = GetTickCount();
    if (local_appdata(base, MAX_PATH - 64)) {
        lstrcatA(base, "\\BC2Replay");
        CreateDirectoryA(base, NULL);
        if (!g_dir[0]) {
            lstrcpyA(g_dir, base);
            lstrcatA(g_dir, "\\dumps");
        }
        if (!g_log[0]) {
            lstrcpyA(g_log, base);
            lstrcatA(g_log, "\\logs");
            CreateDirectoryA(g_log, NULL);
            wsprintfA(name, "\\dump_%lu.log", GetCurrentProcessId());
            lstrcatA(g_log, name);
        }
    }
    if (g_dir[0])
        CreateDirectoryA(g_dir, NULL);
    n = GetModuleFileNameA(NULL, base, sizeof(base));
    p = (n > 0 && n < sizeof(base)) ? strrchr(base, 0x5C) : NULL;
    p = p ? p + 1 : base;
    lstrcpynA(g_exe, p, sizeof(g_exe));
    dot = strrchr(g_exe, '.');
    if (dot && dot != g_exe)
        *dot = 0;
    if (!g_exe[0])
        lstrcpyA(g_exe, "process");
}

/* ---- the filter ---- */
static minidump_fn load_minidump(void)
{
    char path[MAX_PATH];
    HMODULE h;
    UINT n = GetSystemDirectoryA(path, MAX_PATH - 16);
    if (n == 0 || n >= MAX_PATH - 16)
        return NULL;
    lstrcatA(path, "\\dbghelp.dll");
    h = LoadLibraryA(path);
    return h ? (minidump_fn)(UINT_PTR)GetProcAddress(h, "MiniDumpWriteDump") : NULL;
}

static void write_dump(EXCEPTION_POINTERS *ep)
{
    static char name[96];
    SYSTEMTIME t;
    HANDLE f;
    minidump_fn fn;
    MINIDUMP_EXCEPTION_INFORMATION info;
    DWORD code = 0, addr = 0, gle;
    BOOL ok;

    if (ep && ep->ExceptionRecord) {
        code = ep->ExceptionRecord->ExceptionCode;
        addr = (DWORD)(UINT_PTR)ep->ExceptionRecord->ExceptionAddress;
    }
    if (!g_dir[0]) {
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump FAILED no dir", code, addr, GetCurrentThreadId());
        dump_log(g_line);
        return;
    }
    GetLocalTime(&t);
    wsprintfA(name, "\\%s_%lu_%04u%02u%02u_%02u%02u%02u", g_exe, GetCurrentProcessId(),
              t.wYear, t.wMonth, t.wDay, t.wHour, t.wMinute, t.wSecond);
    if (g_calls > 1)  /* a second dump of the same process in the same second must not overwrite the first */
        wsprintfA(name + lstrlenA(name), "_%ld", g_calls);
    lstrcatA(name, ".dmp");
    if (lstrlenA(g_dir) + lstrlenA(name) >= (int)sizeof(g_last)) {
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump FAILED path too long", code, addr, GetCurrentThreadId());
        dump_log(g_line);
        return;
    }
    lstrcpyA(g_last, g_dir);
    lstrcatA(g_last, name);
    fn = load_minidump();
    if (!fn) {
        gle = GetLastError();
        g_last[0] = 0;
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump FAILED dbghelp gle %lu", code, addr, GetCurrentThreadId(), gle);
        dump_log(g_line);
        return;
    }
    f = CreateFileA(g_last, GENERIC_WRITE, FILE_SHARE_READ, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (f == INVALID_HANDLE_VALUE) {
        gle = GetLastError();
        g_last[0] = 0;
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump FAILED create gle %lu", code, addr, GetCurrentThreadId(), gle);
        dump_log(g_line);
        return;
    }
    info.ThreadId = GetCurrentThreadId();
    info.ExceptionPointers = ep;
    info.ClientPointers = FALSE;
    ok = fn(GetCurrentProcess(), GetCurrentProcessId(), f,
            (MINIDUMP_TYPE)(MiniDumpWithIndirectlyReferencedMemory | MiniDumpWithDataSegs),
            ep ? &info : NULL, NULL, NULL);
    gle = GetLastError();
    g_last_bytes = ok ? GetFileSize(f, NULL) : 0;
    CloseHandle(f);
    if (!ok) {
        DeleteFileA(g_last);
        g_last[0] = 0;
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump FAILED write gle %lu", code, addr, GetCurrentThreadId(), gle);
    } else {
        wsprintfA(g_line, "CRASH code 0x%08X addr 0x%08X tid %lu dump %s %lu B", code, addr, GetCurrentThreadId(),
                  g_last, g_last_bytes);
    }
    dump_log(g_line);
}

LONG WINAPI bc2_dump_filter(EXCEPTION_POINTERS *ep)
{
    LPTOP_LEVEL_EXCEPTION_FILTER prev = g_prev;
    InterlockedIncrement(&g_calls);
    if (InterlockedCompareExchange(&g_busy, 1, 0) == 0) {
        __try {
            write_dump(ep);
        } __except (EXCEPTION_EXECUTE_HANDLER) {
            OutputDebugStringA("dinput8 proxy: dump: exception inside the filter\n");
        }
        InterlockedExchange(&g_busy, 0);
    }
    if (prev && prev != bc2_dump_filter)
        return prev(ep);
    return EXCEPTION_CONTINUE_SEARCH;
}

/* ---- install / re-assert ---- */
LPTOP_LEVEL_EXCEPTION_FILTER bc2_dump_install(void)
{
    paths_init();
    g_prev = SetUnhandledExceptionFilter(bc2_dump_filter);
    if (g_prev == bc2_dump_filter)
        g_prev = NULL;
    InterlockedExchange(&g_installed, 1);
    return g_prev;
}

void bc2_dump_set_prev(LPTOP_LEVEL_EXCEPTION_FILTER prev)
{
    g_prev = (prev == bc2_dump_filter) ? NULL : prev;
}

int bc2_dump_reassert(void)
{
    LPTOP_LEVEL_EXCEPTION_FILTER top = SetUnhandledExceptionFilter(bc2_dump_filter);
    if (top == bc2_dump_filter)
        return 0;
    if (top)
        g_prev = top;
    InterlockedIncrement(&g_reasserts);
    if (g_reassert_logs < 8) {
        g_reassert_logs++;
        wsprintfA(g_line, "re-asserted over 0x%08X (now the chain target)", (DWORD)(UINT_PTR)top);
        dump_log(g_line);
    }
    return 1;
}

static DWORD WINAPI reassert_thread(LPVOID arg)
{
    (void)arg;
    while (!g_stop) {
        Sleep(1000);
        if (!g_stop)
            bc2_dump_reassert();
    }
    return 0;
}

/* ---- prune ---- */
int bc2_dump_prune(const char *dir, int keep)
{
    enum { MAX_ENTRIES = 256 };
    static WIN32_FIND_DATAA fd;
    static struct { char name[MAX_PATH]; FILETIME t; } ent[MAX_ENTRIES];
    char pattern[MAX_PATH + 8], path[MAX_PATH * 2];
    HANDLE h;
    int n = 0, deleted = 0, i, j, more = 0;
    if (!dir || !dir[0] || lstrlenA(dir) >= MAX_PATH - 8)
        return -1;
    wsprintfA(pattern, "%s\\*.dmp", dir);
    h = FindFirstFileA(pattern, &fd);
    if (h == INVALID_HANDLE_VALUE)
        return GetLastError() == ERROR_FILE_NOT_FOUND ? 0 : -1;
    do {
        if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
            continue;
        if (n >= MAX_ENTRIES) { more = 1; break; }
        lstrcpynA(ent[n].name, fd.cFileName, MAX_PATH);
        ent[n].t = fd.ftLastWriteTime;
        n++;
    } while (FindNextFileA(h, &fd));
    FindClose(h);
    if (more || keep < 0)
        return 0;
    /* newest first (selection sort; n <= 256) */
    for (i = 0; i < n; i++) {
        int best = i;
        for (j = i + 1; j < n; j++)
            if (CompareFileTime(&ent[j].t, &ent[best].t) > 0)
                best = j;
        if (best != i) {
            char tmp[MAX_PATH];
            FILETIME tt = ent[i].t;
            lstrcpyA(tmp, ent[i].name);
            lstrcpyA(ent[i].name, ent[best].name);
            lstrcpyA(ent[best].name, tmp);
            ent[i].t = ent[best].t;
            ent[best].t = tt;
        }
    }
    for (i = keep; i < n; i++) {
        wsprintfA(path, "%s\\%s", dir, ent[i].name);
        if (DeleteFileA(path))
            deleted++;
    }
    return deleted;
}

/* ---- queries ---- */
int bc2_dump_last(char *path_out, int path_size, DWORD *bytes_out)
{
    if (bytes_out)
        *bytes_out = g_last_bytes;
    if (path_out && path_size > 0)
        lstrcpynA(path_out, g_last, path_size);
    return g_last[0] != 0;
}

int bc2_dump_filter_calls(void) { return (int)g_calls; }
int bc2_dump_reasserts(void) { return (int)g_reasserts; }

/* ---- gate / attach / detach ---- */
int bc2_client_dump_enabled(void)
{
    char ini[MAX_PATH], value[16];
    if (!local_appdata(ini, MAX_PATH - 32))
        return 1;
    lstrcatA(ini, "\\BC2Replay\\config.ini");
    GetPrivateProfileStringA("dll", "dumps", "1", value, sizeof(value), ini);
    return lstrcmpA(value, "0") != 0;
}

int bc2_client_dump_attach(int static_load)
{
    int pruned;
    if (g_installed)
        return 1;
    paths_init();
    dump_log("version " BC2_VERSION_STRING);  /* r266: first line of every dump_<pid>.log */
    if (!bc2_client_dump_enabled()) {
        dump_log("off (ini dumps=0); no filter");
        return 0;
    }
    if (!static_load) {
        dump_log("dynamic load (lpReserved == NULL): no filter");
        return 0;
    }
    bc2_dump_install();
    pruned = g_dir[0] ? bc2_dump_prune(g_dir, BC2_DUMP_KEEP) : -1;
    wsprintfA(g_line, "filter installed prev=0x%08X dir=%s pruned %d", (DWORD)(UINT_PTR)g_prev,
              g_dir[0] ? g_dir : "(none)", pruned);
    dump_log(g_line);
    g_thread = CreateThread(NULL, 0, reassert_thread, NULL, 0, NULL);
    if (!g_thread)
        dump_log("re-assert thread FAILED (the game's own filter will replace ours)");
    else
        CloseHandle(g_thread);
    return 1;
}

void bc2_client_dump_detach(void)
{
    InterlockedExchange(&g_stop, 1);
}
