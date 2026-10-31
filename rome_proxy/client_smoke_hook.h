/* Per-grenade smoke colour hook (r259). The eval-block compiler 0xB26FB0 has exactly one caller, the vertex builder
 * 0xB292E0 (`call 0xB26FB0` at 0xB29362, callee `ret 0x14`, r253/r256). The hook redirects that one call through a
 * stub which, after the original returns, hands the freshly compiled stack block and the EmitterInstance to
 * bc2_smoke_per_apply(); the stub preserves every register and the stack layout. The block is stack-local to the
 * caller, so replacing its colour-data pointer (block+0xE0) affects only this instance for this call; the shared
 * PolynomialColorInterpData objects are never written by this path.
 * Install = one atomic 8-byte exchange of the aligned bytes 0xB29360..0xB29367 (rel32 of the call), gated on the
 * original bytes; never uninstalled (the DLL lives as long as the process).
 */
#ifndef BC2_CLIENT_SMOKE_HOOK_H
#define BC2_CLIENT_SMOKE_HOOK_H
#include <windows.h>

/* Install once (idempotent). 1 = installed (now or earlier), 0 = refused, why = reason. Call from the chat monitor
 * thread only. In the BC2_CHAT_SELFTEST build this is a stub that returns 0 with why = "selftest". */
int bc2_smoke_hook_install(char *why, int why_size);
int bc2_smoke_hook_installed(void);

/* Implemented in client_smoke.c. Called by the stub on a RENDER WORKER thread for every compiled eval block:
 * block = address of the stack eval block (colour node at +0xD0, its data pointer at +0xE0), inst = EmitterInstance*
 * (+0x154 = owning ClientEmitterEntity, r256). Must be fast (a few hundred ns), must never block for long, never
 * log, never touch game memory outside __try. */
void bc2_smoke_per_apply(DWORD block, DWORD inst);

#endif
