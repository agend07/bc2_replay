/* .bc2raw capture file writer and r247 record body packers (pure C, no game reads).
 * File = 128 B header + the drained ring records verbatim (PAD stripped). Header (little-endian, packed):
 *   0 magic "BC2RAW\0\0" | 8 format u16 = 1 | 10 flags u16 = 0 | 12 exe_sha256[32] | 44 dll_version u32
 *   | 48 session_guid[16] | 64 FILETIME u64 | 72 InterruptTime u64 | 80 ring_size u32 | 84..127 zero.
 * Bodies are packed field by field at exact byte offsets (never a naturally aligned C struct): the reader
 * (recorder_v3.py) parses S '<9IQQ' + name, T '<QQ6I', E '<IQII', Q '<8I' + payload.
 */
#ifndef BC2_CAPTURE_H_INCLUDED
#define BC2_CAPTURE_H_INCLUDED

#include <windows.h>

#define BC2_CAPTURE_HDR      128u
#define BC2_CAPTURE_FORMAT   1u
#define BC2_S_HEAD_LEN       52u   /* 9 x u32 + 2 x u64 */
#define BC2_S_NAME_MAX       255u  /* longer names are cut */
#define BC2_S_MAX_BODY       (BC2_S_HEAD_LEN + 256u)
#define BC2_T_BODY_LEN       40u
#define BC2_E_BODY_LEN       20u
#define BC2_Q_HEAD_LEN       32u
#define BC2_E_DESTROY        0u
#define BC2_E_DETACH         1u
#define BC2_E_RECORD_OFF     2u
#define BC2_S_LEVEL_VALID    1u
#define BC2_S_FRAGMENT       2u
#define BC2_S_NO_OBSERVER    4u

typedef struct {
    HANDLE h;
    unsigned long long bytes; /* bytes written incl. the header */
    char path[MAX_PATH];
} bc2_capture;

typedef struct {               /* S session; ver = 1 is written by bc2_pack_s */
    unsigned flags, conn, dispatcher_vt, ghost_bits, qseq, pseq, level_ptr;
    unsigned long long filetime, interrupt_time;
    const char *name;          /* ASCII, no NUL needed */
    unsigned name_len;         /* clamped to BC2_S_NAME_MAX */
} bc2_s_fields;

typedef struct {               /* T sync */
    unsigned long long interrupt_time, filetime;
    unsigned qseq, pseq, prod, drops, contention, skip;
} bc2_t_fields;

typedef struct {               /* E end */
    unsigned reason;
    unsigned long long interrupt_time;
    unsigned qseq, pseq;
} bc2_e_fields;

typedef struct {               /* Q raw packet head (P_HEAD layout); the payload (span bytes, pad to 4) follows */
    unsigned t_hi1, t_lo, t_hi2, qseq, buffer, limit, first_dword, span;
} bc2_q_head_fields;

/* KUSER_SHARED_DATA InterruptTime read exactly like the cave: hi1 @0x7FFE000C, lo @0x7FFE0008,
 * hi2 @0x7FFE0010, repeated until hi1 == hi2. 100 ns units. */
unsigned long long bc2_interrupt_time(void);

/* GetSystemTimeAsFileTime as u64. */
unsigned long long bc2_filetime_now(void);

/* %LOCALAPPDATA%\BC2Replay\captures (create != 0 creates both folders). 1 ok, 0 no LOCALAPPDATA / too long. */
int bc2_capture_dir(char *out, int out_size, int create);

/* Creates <dir>\<utc yyyymmdd_hhmmss>_<pid>.bc2raw (CREATE_NEW; on a collision _2, _3, ... up to _99), share read,
 * and writes the 128 B header (random v4 session GUID from BCryptGenRandom). 1 ok; 0 -> f->h is INVALID_HANDLE_VALUE. */
int bc2_capture_open(bc2_capture *f, const char *dir, const unsigned char exe_sha[32], unsigned dll_version,
                     unsigned ring_size);

/* WriteFile until all of data is written. 1 ok. */
int bc2_capture_append(bc2_capture *f, const void *data, unsigned len);

void bc2_capture_flush(bc2_capture *f);  /* FlushFileBuffers */
void bc2_capture_close(bc2_capture *f);  /* idempotent */

/* Scans complete records in recs[0..len) and returns the offset just after the first E record (BC2_T_E),
 * or len if there is none (a zero / invalid / short head ends the scan). */
unsigned bc2_capture_split_point(const unsigned char *recs, unsigned len);

/* Body packers: write the body (no head) to out, return its length (4-aligned, padding zero).
 * out sizes: S BC2_S_MAX_BODY, T BC2_T_BODY_LEN, E BC2_E_BODY_LEN, Q head BC2_Q_HEAD_LEN. */
unsigned bc2_pack_s(unsigned char *out, const bc2_s_fields *f);
unsigned bc2_pack_t(unsigned char *out, const bc2_t_fields *f);
unsigned bc2_pack_e(unsigned char *out, const bc2_e_fields *f);
unsigned bc2_pack_q_head(unsigned char *out, const bc2_q_head_fields *f);

#endif
