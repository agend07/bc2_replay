#include <windows.h>
#include <bcrypt.h>
#include <string.h>
#include "bc2_capture.h"
#include "client_cave.h"

#pragma comment(lib, "bcrypt.lib")

static void put16(unsigned char *p, unsigned v)
{
    p[0] = (unsigned char)v;
    p[1] = (unsigned char)(v >> 8);
}

static void put32(unsigned char *p, unsigned v)
{
    p[0] = (unsigned char)v;
    p[1] = (unsigned char)(v >> 8);
    p[2] = (unsigned char)(v >> 16);
    p[3] = (unsigned char)(v >> 24);
}

static void put64(unsigned char *p, unsigned long long v)
{
    put32(p, (unsigned)v);
    put32(p + 4, (unsigned)(v >> 32));
}

static unsigned get32(const unsigned char *p)
{
    return p[0] | (unsigned)p[1] << 8 | (unsigned)p[2] << 16 | (unsigned)p[3] << 24;
}

unsigned long long bc2_interrupt_time(void)
{
    volatile const unsigned *lo = (volatile const unsigned *)0x7FFE0008;
    volatile const unsigned *hi1 = (volatile const unsigned *)0x7FFE000C;
    volatile const unsigned *hi2 = (volatile const unsigned *)0x7FFE0010;
    unsigned a, l, b;

    do {
        a = *hi1;
        l = *lo;
        b = *hi2;
    } while (a != b);
    return (unsigned long long)a << 32 | l;
}

unsigned long long bc2_filetime_now(void)
{
    FILETIME ft;
    GetSystemTimeAsFileTime(&ft);
    return (unsigned long long)ft.dwHighDateTime << 32 | ft.dwLowDateTime;
}

int bc2_capture_dir(char *out, int out_size, int create)
{
    char base[MAX_PATH];
    DWORD n = GetEnvironmentVariableA("LOCALAPPDATA", base, sizeof(base));

    if (n == 0 || n >= sizeof(base) || out_size <= 0 || (int)n + 20 >= out_size)
        return 0;
    wsprintfA(out, "%s\\BC2Replay", base);
    if (create)
        CreateDirectoryA(out, NULL);
    lstrcatA(out, "\\captures");
    if (create)
        CreateDirectoryA(out, NULL);
    return 1;
}

int bc2_capture_open(bc2_capture *f, const char *dir, const unsigned char exe_sha[32], unsigned dll_version,
                     unsigned ring_size)
{
    unsigned char hdr[BC2_CAPTURE_HDR], guid[16];
    SYSTEMTIME st;
    char stem[MAX_PATH];
    int k;

    f->h = INVALID_HANDLE_VALUE;
    f->bytes = 0;
    f->path[0] = 0;
    if (lstrlenA(dir) > MAX_PATH - 40)
        return 0;
    GetSystemTime(&st);
    wsprintfA(stem, "%s\\%04u%02u%02u_%02u%02u%02u_%lu", dir, st.wYear, st.wMonth, st.wDay, st.wHour, st.wMinute,
              st.wSecond, (unsigned long)GetCurrentProcessId());
    for (k = 1; k < 100; k++) {
        if (k == 1)
            wsprintfA(f->path, "%s.bc2raw", stem);
        else
            wsprintfA(f->path, "%s_%d.bc2raw", stem, k);
        f->h = CreateFileA(f->path, GENERIC_WRITE, FILE_SHARE_READ, NULL, CREATE_NEW, FILE_ATTRIBUTE_NORMAL, NULL);
        if (f->h != INVALID_HANDLE_VALUE || GetLastError() != ERROR_FILE_EXISTS)
            break;
    }
    if (f->h == INVALID_HANDLE_VALUE) {
        f->path[0] = 0;
        return 0;
    }

    if (BCryptGenRandom(NULL, guid, sizeof(guid), BCRYPT_USE_SYSTEM_PREFERRED_RNG) != 0) {
        /* fallback: not cryptographic, only needs to be unique per session */
        put64(guid, bc2_filetime_now());
        put64(guid + 8, bc2_interrupt_time() ^ ((unsigned long long)GetCurrentProcessId() << 32));
    }
    guid[7] = (unsigned char)((guid[7] & 0x0F) | 0x40); /* UUID v4 (bytes_le layout) */
    guid[8] = (unsigned char)((guid[8] & 0x3F) | 0x80);

    memset(hdr, 0, sizeof(hdr));
    memcpy(hdr, "BC2RAW\0\0", 8);
    put16(hdr + 8, BC2_CAPTURE_FORMAT);
    put16(hdr + 10, 0);
    if (exe_sha)
        memcpy(hdr + 12, exe_sha, 32);
    put32(hdr + 44, dll_version);
    memcpy(hdr + 48, guid, 16);
    put64(hdr + 64, bc2_filetime_now());
    put64(hdr + 72, bc2_interrupt_time());
    put32(hdr + 80, ring_size);
    if (!bc2_capture_append(f, hdr, sizeof(hdr))) {
        CloseHandle(f->h);
        f->h = INVALID_HANDLE_VALUE;
        DeleteFileA(f->path);
        return 0;
    }
    return 1;
}

int bc2_capture_append(bc2_capture *f, const void *data, unsigned len)
{
    const unsigned char *p = (const unsigned char *)data;
    DWORD w;

    if (f->h == INVALID_HANDLE_VALUE)
        return 0;
    while (len) {
        if (!WriteFile(f->h, p, len, &w, NULL) || w == 0)
            return 0;
        p += w;
        len -= w;
        f->bytes += w;
    }
    return 1;
}

void bc2_capture_flush(bc2_capture *f)
{
    if (f->h != INVALID_HANDLE_VALUE)
        FlushFileBuffers(f->h);
}

void bc2_capture_close(bc2_capture *f)
{
    if (f->h != INVALID_HANDLE_VALUE) {
        FlushFileBuffers(f->h);
        CloseHandle(f->h);
        f->h = INVALID_HANDLE_VALUE;
    }
}

unsigned bc2_capture_split_point(const unsigned char *recs, unsigned len)
{
    unsigned i = 0;

    while (len - i >= 4) {
        unsigned head = get32(recs + i), total = head >> 8;
        if (head == 0 || total < 4 || (total & 3) != 0 || total > len - i)
            break;
        i += total;
        if ((head & 0xFF) == BC2_T_E)
            return i;
    }
    return len;
}

unsigned bc2_pack_s(unsigned char *out, const bc2_s_fields *f)
{
    unsigned n = f->name == NULL ? 0 : f->name_len > BC2_S_NAME_MAX ? BC2_S_NAME_MAX : f->name_len;
    unsigned len = (BC2_S_HEAD_LEN + n + 3) & ~3u;

    put32(out + 0, 1);
    put32(out + 4, f->flags);
    put32(out + 8, f->conn);
    put32(out + 12, f->dispatcher_vt);
    put32(out + 16, f->ghost_bits);
    put32(out + 20, f->qseq);
    put32(out + 24, f->pseq);
    put32(out + 28, f->level_ptr);
    put32(out + 32, n);
    put64(out + 36, f->filetime);
    put64(out + 44, f->interrupt_time);
    if (n)
        memcpy(out + BC2_S_HEAD_LEN, f->name, n);
    memset(out + BC2_S_HEAD_LEN + n, 0, len - BC2_S_HEAD_LEN - n);
    return len;
}

unsigned bc2_pack_t(unsigned char *out, const bc2_t_fields *f)
{
    put64(out + 0, f->interrupt_time);
    put64(out + 8, f->filetime);
    put32(out + 16, f->qseq);
    put32(out + 20, f->pseq);
    put32(out + 24, f->prod);
    put32(out + 28, f->drops);
    put32(out + 32, f->contention);
    put32(out + 36, f->skip);
    return BC2_T_BODY_LEN;
}

unsigned bc2_pack_e(unsigned char *out, const bc2_e_fields *f)
{
    put32(out + 0, f->reason);
    put64(out + 4, f->interrupt_time);
    put32(out + 12, f->qseq);
    put32(out + 16, f->pseq);
    return BC2_E_BODY_LEN;
}

unsigned bc2_pack_q_head(unsigned char *out, const bc2_q_head_fields *f)
{
    put32(out + 0, f->t_hi1);
    put32(out + 4, f->t_lo);
    put32(out + 8, f->t_hi2);
    put32(out + 12, f->qseq);
    put32(out + 16, f->buffer);
    put32(out + 20, f->limit);
    put32(out + 24, f->first_dword);
    put32(out + 28, f->span);
    return BC2_Q_HEAD_LEN;
}
