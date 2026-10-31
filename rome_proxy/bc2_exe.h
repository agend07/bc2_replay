/* Per-build address profiles of BFBC2Game.exe (r300). The chat / smoke modules read every game address through
 * bc2_exe() instead of compile-time constants, so one dll serves both known builds:
 *
 *   retail / Project Rome  SHA-256 3911fcc8914b0158c434cee36f19295316e715d4c9a0e70c857e8c39e3d54258, PE stamp 4E55445D
 *   Steam                  SHA-256 22e96001382eb847d3ab0a02b59ec7bb79ba47e04146e74c6c49e38465f38c96, PE stamp 4E55462F
 *
 * The Steam exe is the same source compiled 8 minutes later and wrapped in SteamStub 2.1 (.bind section; .text is
 * AES-encrypted on disk and decrypted in memory by the stub before the game's entry point). Its addresses were derived
 * on 2026-10-31 from an offline-decrypted copy of .text: every retail function below was located by a masked byte
 * signature (absolute operands and rel32 wildcarded, unique hit, equal over 0x180 bytes), every vtable and global by
 * the code that references it (same reference counts in both builds, every vtable slot matching), and the per-grenade
 * hook site bytes are identical (same rel32 distance). Struct offsets are unchanged (the matched code is byte-equal
 * apart from absolute addresses).
 *
 * Selection (bc2_exe_detect, chat monitor thread): the first profile whose UI-event vtable slot 1 and chat-input
 * vtable slot 0 hold that build's handler addresses. The check reads .rdata only, which SteamStub never encrypts, so it
 * is valid before the stub has decrypted .text. Before detection (and in the selftest) bc2_exe() is the retail
 * profile; nothing is hooked until bc2_exe_detect() succeeds. */
#ifndef BC2_EXE_H
#define BC2_EXE_H
#include <windows.h>

typedef struct {
    const char *name;        /* "retail" / "steam" (log + /smoke status) */
    DWORD ui_singleton_ptr;  /* global: UI root object pointer */
    DWORD vt_ui_root;        /* UI root vtable (validates the singleton) */
    DWORD vt_ui_event;       /* UI event dispatcher subobject vtable; slot 1 = fn_ui_event (incoming chat) */
    DWORD fn_ui_event;
    DWORD vt_chat_input;     /* chat input handler vtable; slot 0 = fn_chat_input (outgoing submit) */
    DWORD fn_chat_input;
    DWORD vt_msg_chat;       /* incoming chat message (code 0x1A) vtable */
    DWORD vt_pcid;           /* PolynomialColorInterpData vtable (smoke colour objects) */
    DWORD vt_emitter_doc;    /* EmitterDocument vtable (smoke templates) */
    DWORD emitter_mgr_ptr;   /* global: EmitterManager pointer (recreated per level) */
    DWORD hook_site_call;    /* /smoke per: `call CompileEvalBlock` (e8 rel32) in the vertex builder */
    DWORD hook_tgt_compile;  /* CompileEvalBlock */
} bc2_exe_profile;

/* The active profile (retail until bc2_exe_detect() selects another). Never NULL. */
const bc2_exe_profile *bc2_exe(void);

/* Selects the profile of the running exe; returns it, or NULL on an unknown exe (bc2_exe() then stays retail and the
 * caller must not hook anything). Monitor thread only. */
const bc2_exe_profile *bc2_exe_detect(void);

/* 1 once bc2_exe_detect() has selected a profile. */
int bc2_exe_known(void);

#ifdef BC2_CHAT_SELFTEST
/* test-only: force a profile by name ("retail" / "steam"); 0 = no such profile */
int bc2_exe_test_select(const char *name);
#endif

#endif
