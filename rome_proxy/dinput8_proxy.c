/* dinput8.dll proxy in front of the Project Rome hook (renamed dinput8_rome.dll).
 *
 * Without BC2_ROME_HOST in the environment it only forwards DirectInput8Create,
 * so normal Rome online play is unchanged. With BC2_ROME_HOST=127.0.0.1 (set by
 * the Local Replay panel) it rewrites Rome's hard-coded master host
 * "bfbc2.emulatornexus.com" in Rome's image right after loading it, so the
 * local backend is reached without a hosts-file entry. In BFBC2Game.exe it also starts the
 * client subsystems (dump filter, recorder or probe, chat commands).
 */
#include <windows.h>
#include <string.h>
#include "version.h"
#include "client_probe.h"
#include "client_recorder.h"
#include "client_chat.h"
#include "client_dump.h"

static const char NEEDLE[] = "bfbc2.emulatornexus.com";
static const char CLIENT_NAME[] = "bfbc2game.exe";
static HMODULE rome;

typedef HRESULT (WINAPI *create_fn)(HINSTANCE, DWORD, REFIID, LPVOID *, LPUNKNOWN);

HRESULT WINAPI DirectInput8Create(HINSTANCE inst, DWORD version, REFIID iid, LPVOID *out, LPUNKNOWN outer)
{
    create_fn fn = rome ? (create_fn)GetProcAddress(rome, "DirectInput8Create") : NULL;
    return fn ? fn(inst, version, iid, out, outer) : E_FAIL;
}

/* Replace every NEEDLE inside a NUL-terminated string with host, keeping the tail. */
static int patch_range(BYTE *base, SIZE_T size, const char *host)
{
    SIZE_T n = sizeof(NEEDLE) - 1, hl = strlen(host), i;
    int count = 0;
    for (i = 0; i + n <= size; i++) {
        BYTE *p = base + i, *end;
        DWORD old;
        SIZE_T tail;
        if (memcmp(p, NEEDLE, n) != 0)
            continue;
        end = p + n;
        while (end < base + size && *end)
            end++;
        tail = (SIZE_T)(end - (p + n));
        if (!VirtualProtect(p, n + tail + 1, PAGE_READWRITE, &old))
            continue;
        memmove(p + hl, p + n, tail);
        memcpy(p, host, hl);
        memset(p + hl + tail, 0, n - hl + 1);
        VirtualProtect(p, n + tail + 1, old, &old);
        count++;
    }
    return count;
}

static void redirect_host(const char *host)
{
    BYTE *image = (BYTE *)rome;
    IMAGE_NT_HEADERS *nt = (IMAGE_NT_HEADERS *)(image + ((IMAGE_DOS_HEADER *)image)->e_lfanew);
    IMAGE_SECTION_HEADER *sec = IMAGE_FIRST_SECTION(nt);
    char msg[128];
    int i, count = 0;
    for (i = 0; i < nt->FileHeader.NumberOfSections; i++, sec++)
        if (!(sec->Characteristics & IMAGE_SCN_MEM_EXECUTE))
            count += patch_range(image + sec->VirtualAddress, sec->Misc.VirtualSize, host);
    wsprintfA(msg, "dinput8 proxy: Rome host -> %s (%d strings)\n", host, count);
    OutputDebugStringA(msg);
}

static void log_line(const char *text)
{
    char line[MAX_PATH + 640];
    wsprintfA(line, "dinput8 proxy: %s\r\n", text);
    OutputDebugStringA(line);
}

/* Client subsystems start only in BFBC2Game.exe. static_load = DllMain's lpReserved != NULL
 * (import-time load, before the exe entry point): the client recorder installs only then. */
static void bc2_mode_attach(int static_load)
{
    char exe[MAX_PATH], name[MAX_PATH], line[MAX_PATH + 320];
    const char *base_name;
    DWORD len = GetModuleFileNameA(NULL, exe, sizeof(exe));

    if (len == 0 || len >= sizeof(exe))
        return;
    base_name = strrchr(exe, 0x5C);
    base_name = base_name ? base_name + 1 : exe;
    lstrcpynA(name, base_name, sizeof(name));
    CharLowerA(name);
    /* r266: the marker literal "BC2_PROXY_VERSION=<version>\0" is what the panel reads from the dll
     * bytes (client_install.dll_version); BC2_SHARED_MARKER is how client_install.classify tells this
     * build from Rome's dll. These references keep both in the image. */
    log_line(BC2_VERSION_MARKER);
    log_line(BC2_SHARED_MARKER);

    if (lstrcmpA(name, CLIENT_NAME) == 0) {
        bc2_client_dump_attach(static_load);  /* r263: crash minidump filter first, so attach-time crashes are dumped too */
        /* recorder (r248) first: once its hooks are in, the probe's site byte gate would fail, so the
         * read-only probe (BC2_PROBE=1 / ini probe=1) only runs when the recorder is off or refused. */
        if (bc2_client_recorder_attach(static_load))
            log_line("client mode: recorder installed (probe skipped)");
        else
            bc2_client_probe_attach();
        bc2_client_chat_attach();  /* r255: vtable-clone hooks only, independent of the recorder */
    } else {
        wsprintfA(line, "process %s: host redirect only", base_name);
        log_line(line);
    }
}

BOOL WINAPI DllMain(HINSTANCE self, DWORD reason, LPVOID reserved)
{
    char host[sizeof(NEEDLE)];
    DWORD len;
    (void)self;
    if (reason == DLL_PROCESS_DETACH) {
        bc2_client_probe_detach();    /* stop flags only, never wait */
        bc2_client_recorder_detach();
        bc2_client_chat_detach();
        bc2_client_dump_detach();
    }
    if (reason != DLL_PROCESS_ATTACH)
        return TRUE;
    rome = LoadLibraryA("dinput8_rome.dll");
    if (!rome)
        return FALSE;
    len = GetEnvironmentVariableA("BC2_ROME_HOST", host, sizeof(host));
    if (len > 0 && len < sizeof(host))
        redirect_host(host);
    /* client recorder only on a static (import-time) load, which is what reserved != NULL means
     * for DLL_PROCESS_ATTACH */
    bc2_mode_attach(reserved != NULL);
    return TRUE;
}
