#include <windows.h>
#include <string.h>
#include "bc2_ring.h"

#define BC2_TOTAL_LIMIT (1u << 24)

int bc2_ring_init(bc2_ring *r, void *mem, unsigned size)
{
    if (r == NULL || mem == NULL || size <= 256 || (size & (size - 1)) != 0)
        return 0;
    r->base = (unsigned char *)mem;
    r->size = size;
    memset(mem, 0, BC2_RING_HDR + size);
    BC2_RING_H(r, BC2_RW_MAGIC) = BC2_RING_MAGIC;
    BC2_RING_H(r, BC2_RW_VERSION) = BC2_RING_VERSION;
    BC2_RING_H(r, BC2_RW_SIZE) = size;
    return 1;
}

int bc2_ring_try_lock(bc2_ring *r)
{
    if (InterlockedCompareExchange((volatile LONG *)&BC2_RING_H(r, BC2_RW_LOCK), 1, 0) == 0)
        return 1;
    InterlockedIncrement((volatile LONG *)&BC2_RING_H(r, BC2_RW_CONT));
    return 0;
}

void bc2_ring_unlock(bc2_ring *r)
{
    BC2_RING_H(r, BC2_RW_LOCK) = 0;
}

static void count_drop(bc2_ring *r)
{
    unsigned d = BC2_RING_H(r, BC2_RW_DROPS);
    if (d != 0xFFFFFFFFu)
        BC2_RING_H(r, BC2_RW_DROPS) = d + 1;
}

unsigned char *bc2_ring_reserve(bc2_ring *r, unsigned total)
{
    unsigned prod, cons, tail, extra;
    unsigned char *data = BC2_RING_DATA(r);

    if (total < 4 || (total & 3) != 0 || total >= BC2_TOTAL_LIMIT) {
        count_drop(r);
        return NULL;
    }
    prod = BC2_RING_H(r, BC2_RW_PROD);
    cons = BC2_RING_H(r, BC2_RW_CONS);
    tail = r->size - prod % r->size;
    extra = total > tail ? tail : 0;
    /* 64-bit sum: (prod - cons) + extra + total cannot wrap for any size < 2^31, but stay exact anyway */
    if ((unsigned long long)(prod - cons) + extra + total > r->size) {
        count_drop(r);
        return NULL;
    }
    if (extra)
        *(volatile unsigned *)(data + prod % r->size) = BC2_T_PAD | extra << 8;
    BC2_RING_H(r, BC2_RW_PROD) = prod + extra + total;
    return data + (prod + extra) % r->size;
}

void bc2_ring_publish(unsigned char *rec, unsigned type, unsigned total)
{
    *(volatile unsigned *)rec = (type & 0xFF) | total << 8;
}

void bc2_ring_abandon(unsigned char *rec, unsigned total)
{
    bc2_ring_publish(rec, BC2_T_PAD, total);
}

int bc2_ring_write(bc2_ring *r, unsigned type, const void *body, unsigned len)
{
    unsigned total;
    unsigned char *rec;

    if (len > BC2_TOTAL_LIMIT) { /* keeps 4 + len + 3 from wrapping; reserve rejects it as a drop */
        count_drop(r);
        return 0;
    }
    total = (4 + len + 3) & ~3u;
    rec = bc2_ring_reserve(r, total);
    if (rec == NULL)
        return 0;
    if (len)
        memcpy(rec + 4, body, len);
    /* bytes len+4 .. total-1 are already zero (ring invariant) */
    bc2_ring_publish(rec, type, total);
    return 1;
}

unsigned bc2_ring_drain(bc2_ring *r, unsigned char *out, unsigned cap, int *corrupt)
{
    unsigned char *data = BC2_RING_DATA(r);
    unsigned size = r->size;
    unsigned cons0 = BC2_RING_H(r, BC2_RW_CONS);
    unsigned prod = BC2_RING_H(r, BC2_RW_PROD);
    unsigned cons = cons0, copied = 0, done, off, n;

    if (corrupt)
        *corrupt = 0;
    if (prod - cons > size) {
        if (corrupt)
            *corrupt = 1;
        return 0;
    }
    while (cons != prod) {
        unsigned head, type, total;
        off = cons % size;
        head = *(volatile unsigned *)(data + off);
        if (head == 0)
            break;
        type = head & 0xFF;
        total = head >> 8;
        if (total < 4 || (total & 3) != 0 || off + total > size || total > prod - cons) {
            if (corrupt)
                *corrupt = 1;
            break;
        }
        if (type != BC2_T_PAD) {
            if (total > cap - copied)
                break;
            memcpy(out + copied, data + off, total);
            copied += total;
        }
        cons += total;
    }
    /* zero what was consumed (it may wrap once), then publish the new cons */
    done = cons - cons0;
    if (done) {
        off = cons0 % size;
        n = size - off < done ? size - off : done;
        memset(data + off, 0, n);
        if (done > n)
            memset(data, 0, done - n);
        BC2_RING_H(r, BC2_RW_CONS) = cons;
    }
    return copied;
}
