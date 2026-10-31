/* Crash minidump for BFBC2Game.exe (r263): a chained top-level exception filter writes
 * %LOCALAPPDATA%\BC2Replay\dumps\<exe>_<pid>_<yyyymmdd_hhmmss>.dmp (dbghelp MiniDumpWriteDump, loaded lazily from
 * the system directory inside the filter; MiniDumpWithIndirectlyReferencedMemory | MiniDumpWithDataSegs), logs one
 * line to %LOCALAPPDATA%\BC2Replay\logs\dump_<pid>.log and then calls the previous filter (the game's own at
 * 0x401570, which formats its crash text and returns EXCEPTION_EXECUTE_HANDLER) or returns EXCEPTION_CONTINUE_SEARCH.
 * The game installs its own filter from main (call site 0x401E4A) AFTER DllMain, replacing ours, so a 1 s thread
 * re-asserts ours on top and keeps the replaced one as the chain target. At most the newest 5 dumps are kept
 * (older ones deleted at attach, never in the filter). ON by default; off with [dll] dumps=0 in config.ini.
 * The dump contains memory of the game process (player names, chat text). Not written for a stack overflow
 * (the filter runs on the faulting thread's stack). */
#ifndef BC2_CLIENT_DUMP_H
#define BC2_CLIENT_DUMP_H
#include <windows.h>

#define BC2_DUMP_KEEP 5

/* 0 only when the ini has [dll] dumps=0. */
int bc2_client_dump_enabled(void);

/* Called first in bc2_mode_attach (client process, DllMain PROCESS_ATTACH); static_load = lpReserved != NULL.
 * Returns 1 when the filter is installed (dir created, old dumps pruned, re-assert thread started), 0 otherwise. */
int bc2_client_dump_attach(int static_load);

/* Called from DllMain(DLL_PROCESS_DETACH): sets the thread's stop flag, never waits. The filter stays installed. */
void bc2_client_dump_detach(void);

/* ---- selftest seams (client_dump_selftest.c; no game needed) ---- */
/* Dump directory / log file overrides, set before bc2_dump_install. NULL = default under %LOCALAPPDATA%\BC2Replay;
 * log "" = no file (OutputDebugString only). Copies the strings. */
void bc2_dump_set_dir(const char *dir);
void bc2_dump_set_log(const char *path);
/* Installs bc2_dump_filter as the top-level filter (SetUnhandledExceptionFilter), creates the dump dir, resolves
 * the exe base name; returns the filter that was installed before (also kept as the chain target). */
LPTOP_LEVEL_EXCEPTION_FILTER bc2_dump_install(void);
/* Replaces the chain target (selftest: a dummy previous filter). */
void bc2_dump_set_prev(LPTOP_LEVEL_EXCEPTION_FILTER prev);
/* The filter itself (callable directly from an __except expression with GetExceptionInformation()). */
LONG WINAPI bc2_dump_filter(EXCEPTION_POINTERS *ep);
/* Deletes all but the newest `keep` *.dmp files (by last-write time) in dir. Returns the number deleted,
 * -1 when the directory cannot be listed. Never deletes when more than 256 dumps are present (unknown tail). */
int bc2_dump_prune(const char *dir, int keep);
/* Path and size of the last dump written by this process; returns 0 when none. */
int bc2_dump_last(char *path_out, int path_size, DWORD *bytes_out);
/* Number of times the filter ran, and the number of re-asserts the thread did. */
int bc2_dump_filter_calls(void);
int bc2_dump_reasserts(void);
/* One re-assert pass (what the thread does every second): if another filter sits on top, put ours back and keep
 * the other one as the chain target. Returns 1 when something was replaced. */
int bc2_dump_reassert(void);

#endif
