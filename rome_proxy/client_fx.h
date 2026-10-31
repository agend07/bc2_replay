/* Generic emitter recolour: /fx list [filter] [page] | /fx <name> <color|#rrggbb|off> | /fx off | /fx status.
 * Generalises /smoke: instead of matching the smoke grenades' exact grey values, it enumerates the level's
 * EmitterDocuments (vtable bc2_exe()->vt_emitter_doc, found by the smoke module's RW-region scan, which now hands every
 * hit to bc2_fx_scan_doc) and walks each template's processor chain (EmitterTemplateData +0xB4 RootProcessor,
 * ProcessorData +0x20 NextProcessor / +0x28 ProcessorType / +0x0C Pre = EvaluatorData*). Colour processors
 * (UpdateColor 0x17, UpdateColorLeaf 0x18) whose evaluator is a PolynomialColorInterpData (type 0xB) are recolourable:
 * Color1 (+0x30) / Color0 (+0x40) are read live by the evaluator, so a write recolours every particle of that template.
 * A rule <name> matches every template whose lower-cased asset path contains <name> (so "tracer" or "weapons/" work);
 * the last matching rule wins. The smoke grenade templates are left to /smoke (both would validate-and-fail on each
 * other's writes). Persisted in config.ini [fx] <name>=<color>, re-applied after every level load.
 * The game-memory work runs on the chat monitor thread (called from bc2_smoke_tick); the command only sets the wish.
 */
#ifndef BC2_CLIENT_FX_H
#define BC2_CLIENT_FX_H
#include <windows.h>

/* Read the ini rules; monitor thread, once, before the first tick (no game memory access). */
void bc2_fx_init(void);

/* args = text after "/fx". Writes the reply into out (one chat line). Always returns 1. Chat thread: never touches
 * game memory, never waits on a running scan. */
int bc2_fx_command(const char *args, char *out, int out_size);

/* Called by bc2_smoke_tick (monitor thread, smoke scan lock held). level: the EmitterManager pointer changed (all
 * known objects are gone). scan_wanted: 1 = fx needs a scan now (a rule or /fx list with nothing known). One scan =
 * scan_begin, scan_doc for every dword equal to the EmitterDocument vtable, scan_end. apply: writes pending colours
 * (cheap no-op when nothing changed). */
void bc2_fx_level(DWORD mgr);
int bc2_fx_scan_wanted(DWORD now);
void bc2_fx_scan_begin(void);
void bc2_fx_scan_doc(DWORD doc);
void bc2_fx_scan_end(void);
void bc2_fx_apply(void);

/* Pure helpers (selftest). */
typedef struct { DWORD proc, ptype, ev, evtype, pcid; } bc2_fx_node;   /* evtype/pcid valid when ev != 0 */
/* walk a processor chain from root; fills up to max nodes; returns the count, -1 = not a plausible chain */
int bc2_fx_walk(DWORD root, bc2_fx_node *out, int max);
/* asset path -> short name: last path component, "Em_" prefix dropped, lower-case */
void bc2_fx_short_name(const char *path, char *out, int size);
/* recolour one colour end: hue/saturation from the wish, brightness (max channel) from the original */
void bc2_fx_tint(const float wish[3], const float orig[3], float out[3]);
/* rule name: 1..31 chars of a-z 0-9 _ - . / (case-insensitive), not a sub-command word */
int bc2_fx_key_ok(const char *key);

#endif
