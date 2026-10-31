/* Lock-free (single lock word, never blocking) record ring shared by the client cave and the C producers (r247).
 * Same layout and protocol as recorder_v3.Ring (bc2-client-recorder): 128 B header of u32 words at base
 * (BC2_RW_* indices in client_cave.h), data area of `size` bytes at base + BC2_RING_HDR.
 * Record = u32 head (type | total << 8, total = bytes incl. the head, 4-aligned, < 1 << 24) + body.
 * A record never straddles the end of the data area: a PAD record (type BC2_T_PAD) fills the tail.
 * prod / cons (words 3 / 4) are monotonic u32 byte counters; the data offset is counter % size.
 *
 * Invariant: every data byte not inside [cons, prod) is zero (the ring starts zeroed and the drainer zeroes
 * what it consumes). So a head that reads 0 means "reserved, not yet published", and a PAD needs only its
 * 4 head bytes written.
 *
 * Memory ordering: MSVC x86 (/volatile:ms, the default) gives volatile loads acquire and volatile stores
 * release semantics, and x86 keeps stores in order, so publishing the head with a volatile store after the
 * body is written and the drainer's volatile head load before its body copy need no extra barriers.
 * Producers never spin and never wait: a busy lock drops the record (contention counter).
 */
#ifndef BC2_RING_H_INCLUDED
#define BC2_RING_H_INCLUDED

#include "client_cave.h"

typedef struct {
    unsigned char *base; /* header (BC2_RING_HDR bytes) at base, data at base + BC2_RING_HDR */
    unsigned size;       /* data area bytes, power of two */
} bc2_ring;

#define BC2_RING_H(r, i) (((volatile unsigned *)(r)->base)[i])
#define BC2_RING_DATA(r) ((r)->base + BC2_RING_HDR)

/* mem = BC2_RING_HDR + size bytes. Zeroes all of it and writes magic / version / size (words 0..2).
 * size must be a power of two (so counter % size stays continuous across the u32 wrap) and > 256.
 * Returns 1 ok, 0 bad arguments (nothing written). */
int bc2_ring_init(bc2_ring *r, void *mem, unsigned size);

/* InterlockedCompareExchange(&h[18], 1, 0) == 0 -> 1 (lock held).
 * Otherwise InterlockedIncrement(&h[6]) (contention) and 0: the caller drops its record, never retries. */
int bc2_ring_try_lock(bc2_ring *r);

/* Plain (volatile, release) store of 0 to h[18]. */
void bc2_ring_unlock(bc2_ring *r);

/* Lock held. total = 4-aligned bytes incl. the 4-byte head. Exactly recorder_v3.Ring.reserve:
 *   tail = size - prod % size; extra = total > tail ? tail : 0;
 *   (prod - cons) + extra + total > size -> h[5] (drops) saturating ++, return NULL;
 *   extra -> PAD head (0xFF | extra << 8) at data[prod % size];
 *   h[3] = prod + extra + total; return &data[(prod + extra) % size] (the head slot, still 0).
 * The caller fills the body at +4 and then calls bc2_ring_publish (or bc2_ring_abandon) - always one of them,
 * otherwise the drainer stops at that head forever.
 * A total that is < 4, not 4-aligned or >= 1 << 24 is a caller bug: it is counted as a drop and NULL returned. */
unsigned char *bc2_ring_reserve(bc2_ring *r, unsigned total);

/* *(volatile unsigned *)rec = type | total << 8 (written LAST, after the body). */
void bc2_ring_publish(unsigned char *rec, unsigned type, unsigned total);

/* Publish the reserved slot as PAD (type 0xFF): used when the body copy faulted, so the drainer skips it. */
void bc2_ring_abandon(unsigned char *rec, unsigned total);

/* Lock held. total = pad4(4 + len) (padding bytes are zero); reserve, copy, publish. 1 ok, 0 dropped. */
int bc2_ring_write(bc2_ring *r, unsigned type, const void *body, unsigned len);

/* Consumer (one writer thread, no lock). Walks [cons, prod) with prod read once:
 *   head 0 -> stop (unpublished);
 *   total < 4, total % 4, cons % size + total > size or total > prod - cons -> *corrupt = 1, stop;
 *   PAD -> consumed, not copied; other records (head + body, verbatim) are copied to out while they fit
 *   in cap - the first one that does not fit stops the walk and stays in the ring.
 * prod - cons > size on entry -> *corrupt = 1, return 0. *corrupt is set to 0 first (corrupt may be NULL).
 * Then zeroes the consumed bytes and stores h[4] = new cons (in that order). Returns bytes copied to out. */
unsigned bc2_ring_drain(bc2_ring *r, unsigned char *out, unsigned cap, int *corrupt);

#endif
