/* Per-grenade smoke colour hook (r259), see client_smoke_hook.h. Written by the main session.
 *
 * Site facts (retail BFBC2Game.exe 3911fcc8..., r253 section 2 + r256 section 5, bytes re-read 2026-10-28):
 *   0xB2935E  8d 4c 24 44        lea ecx, [esp+0x44]      ; ecx = stack eval block (after the 5 arg pushes)
 *   0xB29362  e8 49 dc ff ff     call 0xB26FB0            ; CompileEvalBlock(block=ecx, a1, inst, a3, a4, a5), ret 0x14
 *   0xB29367  8b 4c 24 10        mov ecx, [esp+0x10]      ; -> block = esp+0x30 here (0x44 - 0x14)
 *   0xB2936B  85 c9              test ecx, ecx
 *   0xB26FB0  55 8b ec 83 e4 f0 83 ec 44   push ebp; mov ebp,esp; and esp,-16; sub esp,0x44   (callee prologue)
 * 0xB26FB0 has exactly one caller (0xB29362). In the caller's frame [ebp+0x10] = EmitterInstance* (arg3, read-only,
 * both render entry points 0xBC0FBC / 0xBBC5D6 pass it that way, r256).
 *
 * Shape: the rel32 of that one call is redirected to stub_compile, which swaps the return address for stub_after and
 * jumps to the original callee with the stack untouched (its 5 args are exactly where it expects them). The callee
 * returns (ret 0x14) into stub_after with esp/ebp as the original caller would see them; stub_after saves all
 * registers + flags, calls bc2_smoke_per_apply(block = esp+0x30, inst = [ebp+0x10]) and resumes at 0xB29367.
 * The write is one lock cmpxchg8b on the 8-byte-aligned bytes 0xB29360..0xB29367 (the rel32 sits inside), so a
 * render worker fetching the call sees either the old or the new target, never a torn one.
 * Steam build (r300, bc2_exe.h): the same instructions at call 0x119C402 / callee 0x119A050, byte-identical (same
 * rel32 distance); the addresses come from bc2_exe(), the byte gates below are shared.
 */
#include <windows.h>
#include <string.h>
#include "client_smoke_hook.h"
#include "bc2_exe.h"

#ifdef BC2_CHAT_SELFTEST
int bc2_smoke_hook_install(char *why, int why_size)
{
    lstrcpynA(why, "selftest", why_size);
    return 0;
}
int bc2_smoke_hook_installed(void) { return 0; }
#else

#define EXE_BASE          0x00400000u
#define SITE_CALL         (bc2_exe()->hook_site_call)    /* e8 rel32; retail 0xB29362 */
#define SITE_ALIGNED      (SITE_CALL - 2)                /* 8-byte aligned window holding the rel32; retail 0xB29360 */
#define SITE_RET          (SITE_CALL + 5)                /* original return address of the call; retail 0xB29367 */
#define TGT_COMPILE       (bc2_exe()->hook_tgt_compile)  /* CompileEvalBlock; retail 0xB26FB0 */

static const BYTE OLD_SITE[13]   = { 0x8d, 0x4c, 0x24, 0x44, 0xe8, 0x49, 0xdc, 0xff, 0xff, 0x8b, 0x4c, 0x24, 0x10 }; /* 0xB2935E.. */
static const BYTE OLD_CALLEE[9]  = { 0x55, 0x8b, 0xec, 0x83, 0xe4, 0xf0, 0x83, 0xec, 0x44 };                         /* 0xB26FB0.. */

static volatile LONG g_installed;
static DWORD bc2_tgt_compile;       /* set from the profile right before the site is written */
static DWORD bc2_tgt_compile_ret;

static int cmpmem(DWORD addr, const BYTE *bytes, DWORD len)
{
    __try {
        return memcmp((const void *)(UINT_PTR)addr, bytes, len) == 0 ? 1 : 0;
    } __except (EXCEPTION_EXECUTE_HANDLER) {
        return -1;
    }
}

static void __cdecl stub_after(void);

/* Entered by `call` from 0xB29362: [esp] = 0xB29367, [esp+4..] = the callee's 5 args, ecx = block. */
__declspec(naked) static void stub_compile(void)
{
    __asm {
        mov dword ptr [esp], offset stub_after
        jmp dword ptr [bc2_tgt_compile]
    }
}

/* Reached by the callee's `ret 0x14`: esp and ebp are exactly what 0xB29367 would see. */
__declspec(naked) static void __cdecl stub_after(void)
{
    __asm {
        pushfd
        pushad                              ; 4 + 32 bytes above the caller's esp
        mov eax, dword ptr [ebp+0x10]       ; inst (caller frame, ebp preserved by the callee)
        lea edx, dword ptr [esp+0x24+0x30]  ; block = original esp + 0x30
        push eax
        push edx
        call bc2_smoke_per_apply
        add esp, 8
        popad
        popfd
        push dword ptr [bc2_tgt_compile_ret]
        ret
    }
}

int bc2_smoke_hook_installed(void)
{
    return g_installed != 0;
}

int bc2_smoke_hook_install(char *why, int why_size)
{
    DWORD base = (DWORD)(UINT_PTR)GetModuleHandleA(NULL), old_prot, tmp, rel;
    LONGLONG oldv, newv, seen;
    BYTE newb[8];
    int rc;

    if (g_installed) {
        lstrcpynA(why, "already installed", why_size);
        return 1;
    }
    if (base != EXE_BASE) {
        lstrcpynA(why, "exe base mismatch", why_size);
        return 0;
    }
    if (!bc2_exe_known() || (SITE_ALIGNED & 7u) != 0) {
        lstrcpynA(why, "unknown exe", why_size);
        return 0;
    }
    rc = cmpmem(SITE_CALL - 4, OLD_SITE, sizeof(OLD_SITE));
    if (rc != 1) {
        lstrcpynA(why, rc < 0 ? "call site unreadable" : "call site bytes differ (already hooked or other exe)", why_size);
        return 0;
    }
    if (cmpmem(TGT_COMPILE, OLD_CALLEE, sizeof(OLD_CALLEE)) != 1) {
        lstrcpynA(why, "callee prologue bytes differ", why_size);
        return 0;
    }
    bc2_tgt_compile = TGT_COMPILE;      /* before the site points at the stub */
    bc2_tgt_compile_ret = SITE_RET;
    rel = (DWORD)(UINT_PTR)stub_compile - (SITE_CALL + 5);
    memcpy(&oldv, (const void *)(UINT_PTR)SITE_ALIGNED, 8);            /* 24 44 e8 <rel32 old> 8b, just verified */
    memcpy(newb, &oldv, 8);
    memcpy(newb + 3, &rel, 4);
    memcpy(&newv, newb, 8);
    if (!VirtualProtect((void *)(UINT_PTR)SITE_ALIGNED, 8, PAGE_EXECUTE_READWRITE, &old_prot)) {
        char msg[80];
        wsprintfA(msg, "VirtualProtect failed (error %lu); nothing written", GetLastError());
        lstrcpynA(why, msg, why_size);
        return 0;
    }
    seen = InterlockedCompareExchange64((volatile LONGLONG *)(UINT_PTR)SITE_ALIGNED, newv, oldv);
    VirtualProtect((void *)(UINT_PTR)SITE_ALIGNED, 8, old_prot, &tmp);
    FlushInstructionCache(GetCurrentProcess(), (void *)(UINT_PTR)SITE_ALIGNED, 8);
    if (seen != oldv) {
        lstrcpynA(why, "site changed under us; nothing written", why_size);
        return 0;
    }
    InterlockedExchange(&g_installed, 1);
    {   /* wsprintfA has no size argument: format locally, then copy bounded (the old text overran a 64-byte why) */
        char msg[128];
        wsprintfA(msg, "installed: call %lx -> stub %08lx -> %lx", (unsigned long)SITE_CALL,
                  (unsigned long)(UINT_PTR)stub_compile, (unsigned long)TGT_COMPILE);
        lstrcpynA(why, msg, why_size);
    }
    return 1;
}
#endif
