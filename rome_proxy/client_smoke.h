/* Smoke-grenade recolor (r255): /smoke <color|#rrggbb|off|status>. Patches the loaded smoke emitter color data
 * (PolynomialColorInterpData objects, r252/r253) in place: Color0/Color1 of the three smoke-grenade emitters
 * (vapor cloud, trail smoke, projectile trail; the launcher and hand-grenade variants share the same bytes).
 * The scan + patch run on the chat monitor thread (bc2_smoke_tick), never on a game thread; the command only sets
 * the wish. Re-applied after every level load (EmitterManager pointer change) and on a slow periodic rescan.
 * Persisted in %LOCALAPPDATA%\BC2Replay\config.ini [smoke] color=<name|#rrggbb> (empty/off = original greys).
 * Global recolor (every player's smoke); per-grenade colours: /smoke per (r259 per-instance hook, see below).
 */
#ifndef BC2_CLIENT_SMOKE_H
#define BC2_CLIENT_SMOKE_H

/* Read the ini wish; called once from the chat attach (any thread, no game memory access). */
void bc2_smoke_init(void);

/* Called every ~250 ms from the chat monitor thread: level-change detection, pending scans, patches. */
void bc2_smoke_tick(void);

/* Animation (r255d): /smoke rainbow [s] (hue cycles, whole cloud), /smoke psycho [s] (young/old particles get opposite
 * rotating hues, each emitter template 120 deg apart), /smoke pulse <color> [s]; the monitor thread rewrites the known
 * colour objects 20x/s (no scan). Modes persist in the ini ([smoke] mode= period=).
 * /smoke duo <young> <old> (r297): two fixed colours (Color1 / Color0), no animation; ini color= color2= mode=duo.
 * /smoke halloween = duo pumpkin blood. A build may define BC2_SMOKE_DEFAULT (e.g. "halloween"): applied once, on the
 * first run without any [smoke] color= key, then persisted like a typed command (/smoke off sticks).
 * args = text after "/smoke" (may be empty = status). Writes the reply into out. Always returns 1. */
int bc2_smoke_command(const char *args, char *out, int out_size);

/* Pure helpers (selftest): color name / #rrggbb -> rgb floats (1 = ok); the per-emitter mapping of a wish color
 * onto an original (grey) Color1/Color0 pair; the byte matcher for one candidate object. */
int bc2_smoke_parse_color(const char *text, float rgb[3]);
void bc2_smoke_map_colors(const float rgb[3], const float orig1[3], const float orig0[3], float new1[3], float new0[3]);
int bc2_smoke_match(const unsigned char *obj);  /* 0 = no, else 1..3 = emitter kind */
void bc2_smoke_hsv(float hue_turns, float v, float rgb[3]);
void bc2_smoke_anim_colors(int mode, float period, float t, const float rgb[3], int kind, float c1[3], float c0[3]);

/* Per-grenade colours (r259): /smoke per [N] (N = 2..12 hues, default 8; ini [smoke] mode=per per=N color=per).
 * The shared colour objects keep their greys; the per-instance hook (client_smoke_hook.c) calls bc2_smoke_per_apply()
 * for every compiled eval block, which swaps the block's colour-data pointer for a DLL-owned clone carrying the palette
 * colour of that grenade: key = effect root above [inst+0x154] (parent chain +0x64), index = bc2_smoke_per_index(). */
unsigned bc2_smoke_per_index(unsigned long key, unsigned npal);   /* 0..npal-1, deterministic (0 when npal == 0) */
#ifdef BC2_CHAT_SELFTEST
/* test-only publisher: replaces the known colour objects; npal < 2 or n == 0 = disabled */
void bc2_smoke_per_test_set_known(const unsigned long *addrs, const int *kinds, int n, int npal);
#endif

#endif
