/* Offline check of the read-only client probe in a process that is NOT the game:
 * gating (env + ini), log path builder, rd32 fault handling, SHA-256 helper, the
 * identity gate failing cleanly, and the probe thread end to end (GATE fail line).
 * Uses a private LOCALAPPDATA under %TEMP%; removes it afterwards.
 * Usage: client_probe_selftest.exe
 */
#include <stdio.h>
#include "client_probe.c" /* static helpers (rd32, sha256_file, g_thread) are under test */

static int fails;
#define CHECK(c, what) do { if (c) printf("ok   %s\n", what); else { printf("FAIL %s\n", what); fails++; } } while (0)

static int read_text(const char *path, char *buf, DWORD size)
{
    HANDLE f = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING, 0, NULL);
    DWORD got = 0;
    if (f == INVALID_HANDLE_VALUE)
        return 0;
    ReadFile(f, buf, size - 1, &got, NULL);
    CloseHandle(f);
    buf[got] = 0;
    return 1;
}

int main(void)
{
    char tmp[MAX_PATH], root[MAX_PATH], dir[MAX_PATH], ini[MAX_PATH], path[MAX_PATH], expect[64], why[256];
    char text[2048], abc_path[MAX_PATH];
    static const BYTE ABC_SHA[32] = {
        0xba, 0x78, 0x16, 0xbf, 0x8f, 0x01, 0xcf, 0xea, 0x41, 0x41, 0x40, 0xde, 0x5d, 0xae, 0x22, 0x23,
        0xb0, 0x03, 0x61, 0xa3, 0x96, 0x17, 0x7a, 0x9c, 0xb4, 0x10, 0xff, 0x61, 0xf2, 0x00, 0x15, 0xad
    };
    BYTE hash[32];
    DWORD v = 0, local = 0x12345678, written;
    HANDLE f;
    int ok;

    GetTempPathA(sizeof(tmp), tmp);
    wsprintfA(root, "%sbc2_probe_selftest_%lu", tmp, GetCurrentProcessId());
    CreateDirectoryA(root, NULL);
    SetEnvironmentVariableA("LOCALAPPDATA", root);
    wsprintfA(dir, "%s\\BC2Replay", root);
    wsprintfA(ini, "%s\\config.ini", dir);

    /* gating */
    SetEnvironmentVariableA("BC2_PROBE", NULL);
    CHECK(bc2_client_probe_enabled() == 0, "gating: no env, no ini -> off");
    SetEnvironmentVariableA("BC2_PROBE", "1");
    CHECK(bc2_client_probe_enabled() == 1, "gating: BC2_PROBE=1 -> on");
    SetEnvironmentVariableA("BC2_PROBE", "0");
    CHECK(bc2_client_probe_enabled() == 0, "gating: BC2_PROBE=0 -> off");
    SetEnvironmentVariableA("BC2_PROBE", "11");
    CHECK(bc2_client_probe_enabled() == 0, "gating: BC2_PROBE=11 -> off");
    SetEnvironmentVariableA("BC2_PROBE", NULL);
    CreateDirectoryA(dir, NULL);
    WritePrivateProfileStringA("dll", "probe", "1", ini);
    CHECK(bc2_client_probe_enabled() == 1, "gating: ini [dll] probe=1 -> on");
    WritePrivateProfileStringA("dll", "probe", "0", ini);
    CHECK(bc2_client_probe_enabled() == 0, "gating: ini [dll] probe=0 -> off");
    WritePrivateProfileStringA("other", "probe", "1", ini);
    CHECK(bc2_client_probe_enabled() == 0, "gating: probe=1 in another section -> off");

    /* log path */
    ok = bc2_client_probe_log_path(path, sizeof(path), 1);
    wsprintfA(expect, "\\BC2Replay\\logs\\probe_%lu.log", GetCurrentProcessId());
    printf("     log path %s\n", ok ? path : "(none)");
    CHECK(ok && lstrlenA(path) > lstrlenA(expect)
              && lstrcmpiA(path + lstrlenA(path) - lstrlenA(expect), expect) == 0
              && strncmp(path, root, lstrlenA(root)) == 0, "log path = %LOCALAPPDATA%\\BC2Replay\\logs\\probe_<pid>.log");
    wsprintfA(text, "%s\\logs", dir);
    CHECK(GetFileAttributesA(text) != INVALID_FILE_ATTRIBUTES, "log folder created");
    CHECK(bc2_client_probe_log_path(path, 8, 0) == 0, "log path: too small buffer rejected");

    /* rd32 */
    CHECK(rd32(0x10, &v) == 0, "rd32(0x10) faults -> 0");
    CHECK(rd32((DWORD)(UINT_PTR)&local, &v) == 1 && v == 0x12345678, "rd32(valid) reads the value");
    CHECK(cmpmem(0x10, SITES[0].bytes, SITES[0].len) == -1, "site compare on unmapped address -> unreadable");

    /* SHA-256 helper */
    wsprintfA(abc_path, "%s\\abc.bin", root);
    f = CreateFileA(abc_path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    WriteFile(f, "abc", 3, &written, NULL);
    CloseHandle(f);
    CHECK(sha256_file(abc_path, hash) && memcmp(hash, ABC_SHA, 32) == 0, "sha256_file(\"abc\") = ba7816bf...");

    /* identity gate in a non-game process */
    why[0] = 0;
    ok = bc2_client_probe_gate(why);
    printf("     gate: %s\n", why);
    CHECK(ok == 0 && why[0], "gate fails with a reason in a non-game process");

    /* probe thread end to end: the version line (r266) then GATE fail, nothing else */
    SetEnvironmentVariableA("BC2_PROBE", "1");
    bc2_client_probe_attach();
    CHECK(g_thread != NULL && WaitForSingleObject(g_thread, 10000) == WAIT_OBJECT_0, "probe thread exits after gate fail");
    text[0] = 0;
    read_text(path, text, sizeof(text));
    printf("     log: %s", text);
    {
        const char *gate = strstr(text, " GATE fail "), *ver = strstr(text, " version " BC2_VERSION_STRING "\r\n");
        int lines = 0;
        const char *p = text;
        while ((p = strstr(p, "\r\n")) != NULL) { lines++; p += 2; }
        CHECK(text[0] == '+' && ver && gate && gate > ver && lines == 2 && strstr(gate, "\r\n") == text + lstrlenA(text) - 2,
              "log holds the version line, then exactly one '+<ms> GATE fail ...' line");
    }
    bc2_client_probe_detach();
    CHECK(g_stop == 1, "detach sets the stop flag");

    DeleteFileA(path);
    DeleteFileA(abc_path);
    DeleteFileA(ini);
    wsprintfA(text, "%s\\logs", dir);
    RemoveDirectoryA(text);
    RemoveDirectoryA(dir);
    RemoveDirectoryA(root);
    printf(fails ? "SELFTEST FAILED (%d)\n" : "SELFTEST PASSED\n", fails);
    return fails ? 1 : 0;
}
