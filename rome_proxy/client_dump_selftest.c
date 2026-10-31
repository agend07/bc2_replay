/* Offline selftest of client_dump.c (crash minidump filter, r263; no game, no hooks). Checks the retention prune,
 * an in-process dump written by the filter called directly from an __except expression, the chain to the previous
 * filter, the re-assert pass, and an end-to-end real unhandled access violation in a child copy of this exe.
 * Usage: client_dump_selftest.exe [outdir]   (default out_shared); exit 0 = all checks passed.
 * Everything is written under <outdir>\dump_selftest\ (cleared at start; the dumps of the run are kept there).
 * Internal: client_dump_selftest.exe --crash <outdir> is the child mode of check E. */
#include <windows.h>
#include <stdio.h>
#include <string.h>
#include "client_dump.h"

static int fails, checks;
static void check(int ok, const char *what)
{
    printf("%s %s\n", ok ? "ok  " : "FAIL", what);
    checks++;
    if (!ok) fails++;
}

/* ---- small Win32 helpers ---- */
static int file_exists(const char *path)
{
    DWORD a = GetFileAttributesA(path);
    return a != INVALID_FILE_ATTRIBUTES && !(a & FILE_ATTRIBUTE_DIRECTORY);
}

static int file_size(const char *path, DWORD *size)
{
    WIN32_FILE_ATTRIBUTE_DATA fa;
    if (!GetFileAttributesExA(path, GetFileExInfoStandard, &fa)) return 0;
    *size = fa.nFileSizeLow;
    return fa.nFileSizeHigh == 0;
}

static void lower_ascii(char *s)
{
    for (; *s; s++)
        if (*s >= 'A' && *s <= 'Z') *s = (char)(*s - 'A' + 'a');
}

/* Reads up to cap-1 bytes of a file (shared with a writer that may still hold it), NUL-terminated, lowercased. */
static int read_lower(const char *path, char *buf, DWORD cap)
{
    HANDLE h;
    DWORD got = 0;
    buf[0] = 0;
    h = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, NULL, OPEN_EXISTING,
                    FILE_ATTRIBUTE_NORMAL, NULL);
    if (h == INVALID_HANDLE_VALUE) return 0;
    if (!ReadFile(h, buf, cap - 1, &got, NULL)) got = 0;
    CloseHandle(h);
    buf[got] = 0;
    lower_ascii(buf);
    return 1;
}

/* 1 when one line of the file contains every non-NULL needle (case-insensitive). */
static char g_logbuf[65536];
static int log_has_line(const char *path, const char *n1, const char *n2, const char *n3)
{
    char a[MAX_PATH * 2], b[MAX_PATH * 2], c[MAX_PATH * 2];
    char *line, *nl;
    if (!read_lower(path, g_logbuf, sizeof(g_logbuf))) return 0;
    lstrcpynA(a, n1 ? n1 : "", sizeof(a)); lower_ascii(a);
    lstrcpynA(b, n2 ? n2 : "", sizeof(b)); lower_ascii(b);
    lstrcpynA(c, n3 ? n3 : "", sizeof(c)); lower_ascii(c);
    for (line = g_logbuf; *line; line = nl + 1) {
        nl = strchr(line, '\n');
        if (nl) *nl = 0;
        if (strstr(line, a) && strstr(line, b) && strstr(line, c)) return 1;
        if (!nl) break;
    }
    return 0;
}

static int count_dmp(const char *dir)
{
    char pat[MAX_PATH];
    WIN32_FIND_DATAA fd;
    HANDLE h;
    int n = 0;
    wsprintfA(pat, "%s\\*.dmp", dir);
    h = FindFirstFileA(pat, &fd);
    if (h == INVALID_HANDLE_VALUE) return 0;
    do {
        if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) n++;
    } while (FindNextFileA(h, &fd));
    FindClose(h);
    return n;
}

/* Deletes every plain file in dir (leftovers of a previous run). */
static void clean_dir(const char *dir)
{
    char pat[MAX_PATH], p[MAX_PATH];
    WIN32_FIND_DATAA fd;
    HANDLE h;
    wsprintfA(pat, "%s\\*", dir);
    h = FindFirstFileA(pat, &fd);
    if (h == INVALID_HANDLE_VALUE) return;
    do {
        if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) continue;
        wsprintfA(p, "%s\\%s", dir, fd.cFileName);
        SetFileAttributesA(p, FILE_ATTRIBUTE_NORMAL);
        DeleteFileA(p);
    } while (FindNextFileA(h, &fd));
    FindClose(h);
}

static int write_file(const char *path, const char *text, const FILETIME *mtime)
{
    HANDLE h;
    DWORD wr = 0;
    int ok;
    h = CreateFileA(path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h == INVALID_HANDLE_VALUE) return 0;
    ok = WriteFile(h, text, (DWORD)lstrlenA(text), &wr, NULL) && wr == (DWORD)lstrlenA(text);
    if (mtime && !SetFileTime(h, mtime, NULL, mtime)) ok = 0;
    CloseHandle(h);
    return ok;
}

static int run_thread(LPTHREAD_START_ROUTINE fn)
{
    HANDLE t = CreateThread(NULL, 0, fn, NULL, 0, NULL);
    DWORD w;
    if (!t) return 0;
    w = WaitForSingleObject(t, 30000);
    CloseHandle(t);
    return w == WAIT_OBJECT_0;
}

/* ---- faulting threads ---- */
static EXCEPTION_POINTERS *volatile g_ep_seen_filter;  /* what the __except expression got */
static EXCEPTION_POINTERS *volatile g_ep_seen_prev;    /* what dummy_prev got via the chain */
static volatile LONG g_filter_ret = -99;
static volatile LONG g_prev_calls;

static LONG WINAPI dummy_prev(EXCEPTION_POINTERS *ep)
{
    g_ep_seen_prev = ep;
    g_prev_calls++;
    return EXCEPTION_EXECUTE_HANDLER;
}

static LONG WINAPI other_filter(EXCEPTION_POINTERS *ep)
{
    (void)ep;
    return EXCEPTION_CONTINUE_SEARCH;
}

/* AV caught by __try; the filter is invoked with the real exception pointers, its return value is recorded and the
 * handler always runs, so the thread survives. */
static DWORD WINAPI av_thread(LPVOID p)
{
    (void)p;
    __try {
        *(volatile int *)0 = 1;
    } __except (g_ep_seen_filter = GetExceptionInformation(),
                g_filter_ret = bc2_dump_filter(g_ep_seen_filter), EXCEPTION_EXECUTE_HANDLER) {
    }
    return 0;
}

/* ---- child mode (check E) ---- */
static char g_marker[MAX_PATH];

static LONG WINAPI marker_prev(EXCEPTION_POINTERS *ep)
{
    HANDLE h;
    DWORD wr = 0;
    (void)ep;
    h = CreateFileA(g_marker, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h != INVALID_HANDLE_VALUE) {
        WriteFile(h, "chained\r\n", 9, &wr, NULL);
        CloseHandle(h);
    }
    /* EXECUTE_HANDLER: the process terminates with the exception code and WER is never asked. */
    return EXCEPTION_EXECUTE_HANDLER;
}

static DWORD WINAPI crash_thread(LPVOID p)
{
    (void)p;
    *(volatile int *)0 = 1;   /* no __try: goes to the top-level filter */
    return 0;
}

static int child_main(const char *outdir)
{
    char dir[MAX_PATH], log[MAX_PATH];
    HANDLE t;
    SetErrorMode(SEM_NOGPFAULTERRORBOX | SEM_FAILCRITICALERRORS);
    wsprintfA(dir, "%s\\dump_selftest", outdir);
    wsprintfA(log, "%s\\child.log", dir);
    wsprintfA(g_marker, "%s\\chained.txt", dir);
    bc2_dump_set_dir(dir);
    bc2_dump_set_log(log);
    bc2_dump_install();
    bc2_dump_set_prev(marker_prev);
    t = CreateThread(NULL, 0, crash_thread, NULL, 0, NULL);
    if (!t) return 2;
    Sleep(INFINITE);
    return 3;
}

int main(int argc, char **argv)
{
    char dir[MAX_PATH], log[MAX_PATH], p[MAX_PATH], path[MAX_PATH], full_dir[MAX_PATH], full_path[MAX_PATH];
    const char *outdir;
    DWORD bytes = 0, real = 0;
    int i, ok;
    LPTOP_LEVEL_EXCEPTION_FILTER prev_installed, top;

    if (argc > 2 && lstrcmpA(argv[1], "--crash") == 0) return child_main(argv[2]);

    outdir = argc > 1 ? argv[1] : "out_shared";
    CreateDirectoryA(outdir, NULL);
    wsprintfA(dir, "%s\\dump_selftest", outdir);
    CreateDirectoryA(dir, NULL);
    clean_dir(dir);
    wsprintfA(log, "%s\\dump.log", dir);
    if (!GetFullPathNameA(dir, sizeof(full_dir), full_dir, NULL)) lstrcpynA(full_dir, dir, sizeof(full_dir));
    lstrcatA(full_dir, "\\");

    /* ---- A: retention prune ---- */
    {
        FILETIME ft;
        ULARGE_INTEGER t;
        GetSystemTimeAsFileTime(&ft);
        t.LowPart = ft.dwLowDateTime;
        t.HighPart = ft.dwHighDateTime;
        t.QuadPart -= 864000000000ull;                      /* one day ago */
        ok = 1;
        for (i = 1; i <= 7; i++) {                          /* fake_prune_1 = oldest, 1 minute apart */
            ULARGE_INTEGER u = t;
            u.QuadPart += (unsigned __int64)i * 600000000ull;
            ft.dwLowDateTime = u.LowPart;
            ft.dwHighDateTime = u.HighPart;
            wsprintfA(p, "%s\\fake_prune_%d.dmp", dir, i);
            if (!write_file(p, "not a dump\r\n", &ft)) ok = 0;
        }
        wsprintfA(p, "%s\\fake_prune_note.txt", dir);
        if (!write_file(p, "not a dmp\r\n", NULL)) ok = 0;
        check(ok, "A: 7 fake .dmp files (staggered mtimes) + 1 .txt created");
        check(bc2_dump_prune(dir, 5) == 2, "A: prune(dir, 5) deletes 2");
        ok = 1;
        for (i = 1; i <= 7; i++) {
            wsprintfA(p, "%s\\fake_prune_%d.dmp", dir, i);
            if (file_exists(p) != (i > 2)) ok = 0;
        }
        check(ok, "A: the 2 oldest are gone, the 5 newest remain");
        wsprintfA(p, "%s\\fake_prune_note.txt", dir);
        check(file_exists(p), "A: non-.dmp file untouched");
        check(bc2_dump_prune(dir, 5) == 0, "A: second prune deletes 0");
        wsprintfA(p, "%s\\no_such_dir_dump_selftest", dir);
        check(bc2_dump_prune(p, 5) == -1, "A: prune of a missing directory returns -1");
        for (i = 1; i <= 7; i++) {
            wsprintfA(p, "%s\\fake_prune_%d.dmp", dir, i);
            DeleteFileA(p);
        }
        wsprintfA(p, "%s\\fake_prune_note.txt", dir);
        DeleteFileA(p);
        check(count_dmp(dir) == 0, "A: fake files removed");
    }

    /* ---- B: in-process dump (filter called directly with real exception pointers) ---- */
    bc2_dump_set_dir(dir);
    bc2_dump_set_log(log);
    prev_installed = bc2_dump_install();
    printf("     install: previous top-level filter %p\n", (void *)prev_installed);
    bc2_dump_set_prev(NULL);   /* keep the CRT's filter out of the directly-called pass */
    check(run_thread(av_thread), "B: faulting thread survived (__except)");
    check(bc2_dump_filter_calls() == 1, "B: filter ran once");
    path[0] = 0;
    check(bc2_dump_last(path, (int)sizeof(path), &bytes) == 1, "B: bc2_dump_last reports a dump");
    printf("     dump: %s (%lu B)\n", path, (unsigned long)bytes);
    {
        const char *name = strrchr(path, '\\');
        int nlen;
        name = name ? name + 1 : path;
        nlen = lstrlenA(name);
        if (!GetFullPathNameA(path, sizeof(full_path), full_path, NULL)) lstrcpynA(full_path, path, sizeof(full_path));
        check(file_exists(path) && _strnicmp(full_path, full_dir, strlen(full_dir)) == 0,
              "B: dump file exists under <outdir>\\dump_selftest\\");
        check(nlen > 4 && _stricmp(name + nlen - 4, ".dmp") == 0 &&
              _strnicmp(name, "client_dump_selftest_", 21) == 0, "B: name = client_dump_selftest_*.dmp");
    }
    check(bytes > 64u * 1024u, "B: dump larger than 64 KB");
    check(file_size(path, &real) && real == bytes, "B: file size matches the reported size");
    check(log_has_line(log, "CRASH", "c0000005", path), "B: log line has CRASH, 0xC0000005 and the dump path");

    /* ---- C: chain to the previous filter ---- */
    g_ep_seen_filter = NULL;
    g_ep_seen_prev = NULL;
    g_filter_ret = -99;
    bc2_dump_set_prev(dummy_prev);
    check(run_thread(av_thread), "C: faulting thread survived (chain to dummy_prev)");
    check(g_filter_ret == EXCEPTION_EXECUTE_HANDLER, "C: filter returns the previous filter's EXECUTE_HANDLER");
    check(g_prev_calls == 1 && g_ep_seen_prev != NULL && g_ep_seen_prev == g_ep_seen_filter,
          "C: dummy_prev saw the same EXCEPTION_POINTERS");
    g_filter_ret = -99;
    g_prev_calls = 0;
    bc2_dump_set_prev(NULL);
    check(run_thread(av_thread), "C: faulting thread survived (no previous filter)");
    check(g_filter_ret == EXCEPTION_CONTINUE_SEARCH && g_prev_calls == 0,
          "C: without a previous filter the filter returns CONTINUE_SEARCH");
    check(bc2_dump_filter_calls() == 3, "C: filter ran 3 times in total");

    /* ---- D: re-assert pass ---- */
    check(bc2_dump_reassert() == 0, "D: reassert = 0 while ours is on top");
    SetUnhandledExceptionFilter(other_filter);
    check(bc2_dump_reassert() == 1, "D: reassert = 1 after another filter replaced ours");
    top = SetUnhandledExceptionFilter(NULL);
    check(top == bc2_dump_filter, "D: ours is back on top");
    SetUnhandledExceptionFilter(bc2_dump_filter);
    check(bc2_dump_reasserts() >= 1, "D: reassert counter >= 1");
    bc2_dump_set_prev(NULL);   /* drop other_filter as the chain target again */

    /* ---- E: end-to-end real unhandled exception in a child copy of this exe ----
     * The OS calls the top-level filter only when no debugger is attached to the child: under a debugger the AV goes
     * to the debugger and this check fails. WER stays out because the chained marker_prev returns
     * EXCEPTION_EXECUTE_HANDLER (and the child sets SEM_NOGPFAULTERRORBOX). */
    {
        char exe[MAX_PATH], cmd[MAX_PATH * 3], marker[MAX_PATH], clog[MAX_PATH];
        STARTUPINFOA si;
        PROCESS_INFORMATION pi;
        DWORD code = 0, w;
        int before, after, big = 0;
        before = count_dmp(dir);
        wsprintfA(marker, "%s\\chained.txt", dir);
        wsprintfA(clog, "%s\\child.log", dir);
        DeleteFileA(marker);
        DeleteFileA(clog);
        GetModuleFileNameA(NULL, exe, sizeof(exe));
        wsprintfA(cmd, "\"%s\" --crash \"%s\"", exe, outdir);
        ZeroMemory(&si, sizeof(si));
        si.cb = sizeof(si);
        ZeroMemory(&pi, sizeof(pi));
        ok = CreateProcessA(NULL, cmd, NULL, NULL, FALSE, 0, NULL, NULL, &si, &pi) != 0;
        check(ok, "E: child started");
        if (ok) {
            w = WaitForSingleObject(pi.hProcess, 30000);
            if (w != WAIT_OBJECT_0) TerminateProcess(pi.hProcess, 1);
            check(w == WAIT_OBJECT_0, "E: child terminated within 30 s");
            GetExitCodeProcess(pi.hProcess, &code);
            CloseHandle(pi.hThread);
            CloseHandle(pi.hProcess);
            printf("     child exit code 0x%08lX\n", (unsigned long)code);
            check(code == 0xC0000005u, "E: child exit code 0xC0000005 (STATUS_ACCESS_VIOLATION)");
            check(file_exists(marker), "E: chained previous filter ran (chained.txt)");
            check(log_has_line(clog, "CRASH", NULL, NULL), "E: child.log has a CRASH line");
            after = count_dmp(dir);
            {
                /* <exe>_<pid>_<yyyymmdd_hhmmss>.dmp: the child's pid tells its dump from the B/C ones */
                char pat[MAX_PATH];
                WIN32_FIND_DATAA fd;
                HANDLE h;
                wsprintfA(pat, "%s\\client_dump_selftest_%lu_*.dmp", dir, (unsigned long)pi.dwProcessId);
                h = FindFirstFileA(pat, &fd);
                if (h != INVALID_HANDLE_VALUE) {
                    do {
                        if (fd.nFileSizeHigh != 0 || fd.nFileSizeLow > 64u * 1024u) big = 1;
                    } while (FindNextFileA(h, &fd));
                    FindClose(h);
                }
            }
            check(after == before + 1, "E: exactly one new .dmp file");
            check(big, "E: the child's dump (<exe>_<child pid>_...) is larger than 64 KB");
        }
    }

    printf("dump selftest: %d checks, %d failed\n", checks, fails);
    return fails ? 1 : 0;
}
