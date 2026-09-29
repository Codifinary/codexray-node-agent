// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

// Userspace stand-ins for the kernel-only pieces used by ebpftracer/ebpf/l7/*.c,
// so the protocol classifiers can be compiled with the host compiler and called
// from Go tests. consistency_test.go fails if these drift from ebpf.c / l7.c.

#ifndef CTEST_HOST_SHIM_H
#define CTEST_HOST_SHIM_H

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>

#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif

// Same underlying types as the kernel's asm-generic/int-ll64.h.
typedef unsigned char      __u8;
typedef unsigned short     __u16;
typedef unsigned int       __u32;
typedef unsigned long long __u64;
typedef short              __s16;
typedef int                __s32;
typedef unsigned int       __be32;
typedef unsigned long long __be64;

// tcp/state.c
#define MAX_PAYLOAD_SIZE 1024

// ebpf.c
#define MIN(a,b) (((a)<(b))?(a):(b))

// l7/l7.c
#define STATUS_UNKNOWN  0
#define STATUS_OK       200
#define STATUS_FAILED   500

#define METHOD_UNKNOWN              0
#define METHOD_PRODUCE              1
#define METHOD_CONSUME              2
#define METHOD_STATEMENT_PREPARE    3
#define METHOD_STATEMENT_CLOSE      4
#define METHOD_HTTP2_CLIENT_FRAMES  5
#define METHOD_HTTP2_SERVER_FRAMES  6

#define bpf_htonl(x) htonl(x)
#define bpf_ntohl(x) ntohl(x)
#define bpf_htons(x) htons(x)
#define bpf_ntohs(x) ntohs(x)

// Never fails: the harness surrounds every payload with zeroed memory, the way
// the kernel reads past the captured bytes into the rest of the user buffer.
#define bpf_probe_read(dst, size, src) (memcpy((dst), (src), (size)), 0)

// Kernel semantics: copies up to size-1 bytes, stops at NUL, always
// NUL-terminates, returns the copied length including the NUL.
static inline long bpf_probe_read_str(void *dst, __u32 size, const void *src) {
    const char *s = src;
    char *d = dst;
    __u32 i = 0;
    for (; i + 1 < size && s[i]; i++) {
        d[i] = s[i];
    }
    d[i] = 0;
    return i + 1;
}

// ebpf.c
#define bpf_read(src, dst)                            \
({                                                    \
    if (bpf_probe_read(&dst, sizeof(dst), src) < 0) { \
        return 0;                                     \
    }                                                 \
})

// l7/l7.c, with the BPF-only `asm volatile ("%0 &= %1")` written as plain C.
#define TRUNCATE_PAYLOAD_SIZE(size) ({   \
    size = MIN(size, MAX_PAYLOAD_SIZE-1); \
    size &= MAX_PAYLOAD_SIZE-1;           \
})

#endif
