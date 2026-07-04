// plug_hook — transparent connect()/DNS interception for plug ("N1" layer).
//
// Copyright 2026 Softwarity / the plug authors.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at http://www.apache.org/licenses/LICENSE-2.0 . Unless required by
// applicable law or agreed to in writing, software distributed under the License
// is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.
//
// This is ORIGINAL code. The high-level design (LD_PRELOAD/DYLD interposition of
// connect() + getaddrinfo(), plus a "fake IP" table so cluster names resolve
// remotely through the SOCKS proxy) is the well-known proxychains model, but no
// proxychains-ng source was copied — proxychains-ng is GPL-2.0, incompatible
// with plug's Apache-2.0. This file was written from scratch against RFC 1928
// (SOCKS5) and the POSIX / dyld interposition APIs.
//
// ---------------------------------------------------------------------------
// What it does
// ---------------------------------------------------------------------------
// A libc-based child process that plug launches has this shared library injected
// (DYLD_INSERT_LIBRARIES on macOS, LD_PRELOAD on Linux). It interposes two libc
// entry points:
//
//   * getaddrinfo()  — for a *remote* name (anything that is not already a
//     literal IP), instead of resolving locally we mint a synthetic "fake" IPv4
//     address from a reserved pool (240.0.0.0/4, RFC 1112 class E — never
//     routed) and remember name <-> fakeIP in a small thread-safe table. The app
//     gets the fake IP back and thinks resolution succeeded.
//
//   * connect()      — for an outbound TCP connection (AF_INET/AF_INET6,
//     SOCK_STREAM) to a non-loopback address, we speak SOCKS5 to the proxy named
//     by $PLUG_SOCKS and issue a CONNECT to the intended target, then hand the
//     socket back to the app so it proceeds transparently. If the destination is
//     one of our fake IPs we recover the original hostname and send THAT in the
//     SOCKS request (socks5h remote resolution), so the cluster's DNS resolves
//     it cluster-side. Loopback, unix sockets, and non-stream sockets are passed
//     straight through to the real connect().
//
// If $PLUG_SOCKS is unset the library is a transparent no-op: every interposed
// call just forwards to the real libc function. So the lib is harmless when plug
// did not configure it.
//
// TCP only. UDP / QUIC / ICMP are left entirely alone.

// _GNU_SOURCE must precede every include so glibc exposes RTLD_NEXT (dlfcn.h).
// Harmless on macOS. Kept unconditional and first for correctness on Linux.
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <netdb.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <unistd.h>

// dlsym / RTLD_NEXT on both platforms (we always fetch the real symbols this way).
#include <dlfcn.h>

// ---------------------------------------------------------------------------
// Fake-IP pool: 240.0.0.0/4 (class E, reserved, never routed on the public
// Internet). We hand these out for remote names and recognise them in connect()
// to recover the hostname. A cluster is extremely unlikely to number services in
// class E, so collisions with real destinations are practically impossible.
// ---------------------------------------------------------------------------
#define FAKE_NET_BASE 0xF0000000u  // 240.0.0.0
#define FAKE_NET_MASK 0xF0000000u  // /4
#define FAKE_MAX_ENTRIES 4096

struct fake_entry {
    uint32_t ip;                 // host byte order
    char host[256];              // the original name we were asked to resolve
};

static struct fake_entry g_table[FAKE_MAX_ENTRIES];
static int g_table_len = 0;
static uint32_t g_next = 1;      // offset within the pool (skip .0)
static pthread_mutex_t g_lock = PTHREAD_MUTEX_INITIALIZER;

// Resolved lazily from $PLUG_SOCKS on first use, then cached.
static pthread_once_t g_socks_once = PTHREAD_ONCE_INIT;
static int g_have_socks = 0;
static char g_socks_host[256];
static int g_socks_port = 0;

// Optional debug logging: set PLUG_HOOK_DEBUG=1 to trace decisions on stderr.
static int g_debug = 0;

static void plug_log(const char *fmt, ...) {
    if (!g_debug) return;
    va_list ap;
    va_start(ap, fmt);
    fprintf(stderr, "[plug-hook] ");
    vfprintf(stderr, fmt, ap);
    fprintf(stderr, "\n");
    va_end(ap);
}

static void parse_socks_env(void) {
    const char *dbg = getenv("PLUG_HOOK_DEBUG");
    g_debug = (dbg && dbg[0] && strcmp(dbg, "0") != 0);

    const char *s = getenv("PLUG_SOCKS");
    if (!s || !s[0]) {
        plug_log("PLUG_SOCKS unset — hook is a no-op");
        return;
    }
    // Accept "host:port". Also tolerate a "socks5h://host:port" style prefix.
    if (strncmp(s, "socks5h://", 10) == 0) s += 10;
    else if (strncmp(s, "socks5://", 9) == 0) s += 9;

    const char *colon = strrchr(s, ':');
    if (!colon || colon == s) {
        plug_log("PLUG_SOCKS=%s has no host:port — hook disabled", s);
        return;
    }
    size_t hlen = (size_t)(colon - s);
    if (hlen >= sizeof(g_socks_host)) hlen = sizeof(g_socks_host) - 1;
    memcpy(g_socks_host, s, hlen);
    g_socks_host[hlen] = '\0';
    g_socks_port = atoi(colon + 1);
    if (g_socks_port <= 0 || g_socks_port > 65535) {
        plug_log("PLUG_SOCKS=%s has a bad port — hook disabled", s);
        return;
    }
    g_have_socks = 1;
    plug_log("proxy = %s:%d", g_socks_host, g_socks_port);
}

static int socks_configured(void) {
    pthread_once(&g_socks_once, parse_socks_env);
    return g_have_socks;
}

// ---------------------------------------------------------------------------
// Real libc symbols. On Linux we grab them with dlsym(RTLD_NEXT). On macOS the
// interposition table (below) means the "real" function is simply the libc one
// called by name, so real_* are just the libc symbols themselves.
// ---------------------------------------------------------------------------
typedef int (*connect_fn)(int, const struct sockaddr *, socklen_t);
typedef struct hostent *(*gethostbyname_fn)(const char *);

// Getting the REAL getaddrinfo back is impossible on macOS: because we register
// it in the __interpose table, dyld makes *every* lookup of the symbol resolve
// to our shim — dlsym(RTLD_NEXT), dlsym(RTLD_DEFAULT) and even a handle-scoped
// dlsym on libsystem_info.dylib all return our own function (verified by pointer
// comparison). Calling any of them recurses forever (observed as a hang). And
// getaddrinfo is not a system call, so there is no raw path like connect() has.
//
// We sidestep the whole problem: since interposition is total, the ONLY way an
// addrinfo ever reaches the app is through OUR getaddrinfo — so we always build
// the result ourselves and never need the real getaddrinfo or freeaddrinfo.
//   * proxy configured  -> synthesize a fake-IP addrinfo (remote DNS via SOCKS);
//   * no proxy (no-op)  -> resolve for real with gethostbyname(), which we do NOT
//     interpose, so dlsym(RTLD_NEXT,"gethostbyname") returns the genuine libc
//     function (verified: it returns a real address). IPv4 only in this fallback.
// On Linux none of this is needed, but the same code is correct there too.
static gethostbyname_fn real_gethostbyname;
static pthread_once_t g_syms_once = PTHREAD_ONCE_INIT;

static void resolve_syms(void) {
    real_gethostbyname = (gethostbyname_fn)dlsym(RTLD_NEXT, "gethostbyname");
}

static void ensure_syms(void) { pthread_once(&g_syms_once, resolve_syms); }

// resolve_ipv4_real resolves host to a single IPv4 (host byte order) using the
// genuine, non-interposed gethostbyname. Returns 1 on success. Used only for the
// no-op fallback and (rarely) a named proxy host. gethostbyname keeps its result
// in static storage, so we serialise callers with g_lock.
static int resolve_ipv4_real(const char *host, uint32_t *out) {
    ensure_syms();
    if (!real_gethostbyname) return 0;
    // Already a dotted-quad? Parse directly, no lookup.
    struct in_addr lit;
    if (inet_pton(AF_INET, host, &lit) == 1) {
        *out = ntohl(lit.s_addr);
        return 1;
    }
    pthread_mutex_lock(&g_lock);
    struct hostent *he = real_gethostbyname(host);
    int ok = 0;
    if (he && he->h_addrtype == AF_INET && he->h_length == 4 &&
        he->h_addr_list && he->h_addr_list[0]) {
        uint32_t net;
        memcpy(&net, he->h_addr_list[0], 4);
        *out = ntohl(net);
        ok = 1;
    }
    pthread_mutex_unlock(&g_lock);
    return ok;
}

// do_real_connect performs the ACTUAL kernel connect, bypassing our own shim.
//
// The recursion trap: on macOS a __DATA,__interpose entry swaps the `connect`
// symbol *everywhere in the process, including inside this library and including
// what dlsym(RTLD_NEXT,"connect") resolves to* — so a dlsym'd "real_connect"
// loops straight back into us (verified: it segfaults on a connect storm). Even
// a handle-specific dlsym on libsystem_kernel.dylib returns the interposed entry.
// The only reliable way to reach the un-interposed connect from inside the
// interposing image is the raw BSD system call. Apple marks the syscall() wrapper
// deprecated (they don't promise syscall NUMBERS across releases), but SYS_connect
// comes from the same SDK we compile against, so it matches the kernel plug runs
// on; and this is exactly the path proxychains-style tools use on macOS. We
// silence the deprecation locally and keep it isolated to this one call.
//
// On Linux, ELF LD_PRELOAD semantics make dlsym(RTLD_NEXT,"connect") the genuine
// libc function, so we simply use that.
#ifdef __APPLE__
#include <sys/syscall.h>
static int do_real_connect(int fd, const struct sockaddr *addr, socklen_t len) {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    return syscall(SYS_connect, fd, addr, (int)len);
#pragma clang diagnostic pop
}
#else
static connect_fn real_connect_next;
static void resolve_connect(void) {
    real_connect_next = (connect_fn)dlsym(RTLD_NEXT, "connect");
}
static int do_real_connect(int fd, const struct sockaddr *addr, socklen_t len) {
    static pthread_once_t once = PTHREAD_ONCE_INIT;
    pthread_once(&once, resolve_connect);
    return real_connect_next(fd, addr, len);
}
#endif

// ---------------------------------------------------------------------------
// Fake-IP table
// ---------------------------------------------------------------------------

// remember_name returns a fresh (or existing) fake IP for host, in host byte
// order. Thread-safe.
static uint32_t remember_name(const char *host) {
    pthread_mutex_lock(&g_lock);
    // Reuse an existing mapping so repeated lookups of the same name are stable.
    for (int i = 0; i < g_table_len; i++) {
        if (strcmp(g_table[i].host, host) == 0) {
            uint32_t ip = g_table[i].ip;
            pthread_mutex_unlock(&g_lock);
            return ip;
        }
    }
    uint32_t ip = FAKE_NET_BASE | (g_next & ~FAKE_NET_MASK);
    if (ip == FAKE_NET_BASE) ip++;  // skip the network address itself
    g_next++;
    if (g_table_len < FAKE_MAX_ENTRIES) {
        g_table[g_table_len].ip = ip;
        snprintf(g_table[g_table_len].host, sizeof(g_table[g_table_len].host),
                 "%s", host);
        g_table_len++;
    }
    pthread_mutex_unlock(&g_lock);
    return ip;
}

// lookup_fake copies the hostname for a fake IP into out (size outlen). Returns 1
// if ip is one of ours and was found, 0 otherwise.
static int lookup_fake(uint32_t ip, char *out, size_t outlen) {
    if ((ip & FAKE_NET_MASK) != FAKE_NET_BASE) return 0;
    pthread_mutex_lock(&g_lock);
    for (int i = 0; i < g_table_len; i++) {
        if (g_table[i].ip == ip) {
            snprintf(out, outlen, "%s", g_table[i].host);
            pthread_mutex_unlock(&g_lock);
            return 1;
        }
    }
    pthread_mutex_unlock(&g_lock);
    return 0;
}

// ---------------------------------------------------------------------------
// Blocking I/O helpers. The app may have put the socket in non-blocking mode
// (async connect). The SOCKS handshake needs ordered, complete reads/writes, so
// we perform it synchronously on the fd, then restore the caller's flags.
// ---------------------------------------------------------------------------
static ssize_t write_all(int fd, const void *buf, size_t n) {
    const char *p = (const char *)buf;
    size_t off = 0;
    while (off < n) {
        ssize_t w = send(fd, p + off, n - off, 0);
        if (w < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (w == 0) return -1;
        off += (size_t)w;
    }
    return (ssize_t)off;
}

static ssize_t read_all(int fd, void *buf, size_t n) {
    char *p = (char *)buf;
    size_t off = 0;
    while (off < n) {
        ssize_t r = recv(fd, p + off, n - off, 0);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (r == 0) return -1;  // peer closed early
        off += (size_t)r;
    }
    return (ssize_t)off;
}

// socks5_negotiate performs the RFC 1928 no-auth handshake and a CONNECT to
// (host, port). host is either a name (remote resolution, ATYP=domain) or NULL,
// in which case ip4 (host byte order) is sent as ATYP=IPv4. Returns 0 on success.
static int socks5_negotiate(int fd, const char *host, uint32_t ip4, uint16_t port) {
    // Greeting: VER=5, NMETHODS=1, METHOD=0 (no auth).
    unsigned char greet[3] = {0x05, 0x01, 0x00};
    if (write_all(fd, greet, sizeof(greet)) < 0) return -1;

    unsigned char sel[2];
    if (read_all(fd, sel, sizeof(sel)) < 0) return -1;
    if (sel[0] != 0x05 || sel[1] != 0x00) {
        plug_log("socks: server refused no-auth (%02x %02x)", sel[0], sel[1]);
        return -1;
    }

    // Request: VER CMD RSV ATYP DST.ADDR DST.PORT
    unsigned char req[262];
    size_t n = 0;
    req[n++] = 0x05;  // VER
    req[n++] = 0x01;  // CMD = CONNECT
    req[n++] = 0x00;  // RSV
    if (host) {
        size_t hlen = strlen(host);
        if (hlen > 255) hlen = 255;
        req[n++] = 0x03;               // ATYP = domain name
        req[n++] = (unsigned char)hlen;
        memcpy(req + n, host, hlen);
        n += hlen;
    } else {
        req[n++] = 0x01;                       // ATYP = IPv4
        req[n++] = (unsigned char)((ip4 >> 24) & 0xff);
        req[n++] = (unsigned char)((ip4 >> 16) & 0xff);
        req[n++] = (unsigned char)((ip4 >> 8) & 0xff);
        req[n++] = (unsigned char)(ip4 & 0xff);
    }
    req[n++] = (unsigned char)((port >> 8) & 0xff);
    req[n++] = (unsigned char)(port & 0xff);
    if (write_all(fd, req, n) < 0) return -1;

    // Reply: VER REP RSV ATYP BND.ADDR BND.PORT. Read the fixed 4 bytes, then the
    // variable bound-address, then the 2 port bytes, and discard them.
    unsigned char rep[4];
    if (read_all(fd, rep, sizeof(rep)) < 0) return -1;
    if (rep[0] != 0x05 || rep[1] != 0x00) {
        plug_log("socks: CONNECT rejected (rep=%02x)", rep[1]);
        errno = ECONNREFUSED;
        return -1;
    }
    size_t skip;
    switch (rep[3]) {
        case 0x01: skip = 4; break;   // IPv4
        case 0x04: skip = 16; break;  // IPv6
        case 0x03: {                  // domain: 1 length byte + that many
            unsigned char l;
            if (read_all(fd, &l, 1) < 0) return -1;
            skip = l;
            break;
        }
        default: return -1;
    }
    unsigned char scratch[256 + 2];
    if (read_all(fd, scratch, skip + 2) < 0) return -1;  // bound addr + port
    return 0;
}

// connect_to_proxy opens a fresh blocking TCP socket to the SOCKS proxy and
// connects it (via the real connect, to loopback). Returns the fd or -1.
static int connect_to_proxy(void) {
    ensure_syms();
    int pfd = socket(AF_INET, SOCK_STREAM, 0);
    if (pfd < 0) return -1;
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons((uint16_t)g_socks_port);
    if (inet_pton(AF_INET, g_socks_host, &sa.sin_addr) != 1) {
        // Proxy host is a name (unusual — plug passes 127.0.0.1). Resolve it with
        // the genuine, non-interposed resolver so we don't recurse into our own
        // fake table.
        uint32_t ip;
        if (!resolve_ipv4_real(g_socks_host, &ip)) {
            close(pfd);
            return -1;
        }
        sa.sin_addr.s_addr = htonl(ip);
    }
    if (do_real_connect(pfd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        close(pfd);
        return -1;
    }
    return pfd;
}

// is_loopback4 reports whether a host-order IPv4 is in 127.0.0.0/8.
static int is_loopback4(uint32_t ip_host_order) {
    return (ip_host_order >> 24) == 127;
}

// ---------------------------------------------------------------------------
// The core: reroute an app's connect() through the SOCKS proxy.
//
// We do NOT connect the app's own fd to the target. Instead we open a *second*
// socket to the proxy, run the SOCKS handshake+CONNECT on it, then dup2() it
// onto the app's fd. From the app's point of view its fd is now connected to the
// target and it can read/write normally — every byte travels the SOCKS tunnel.
//
// dup2 preserves the app's fd number (so any reference it kept stays valid). We
// then restore the non-blocking flag the app had set, if any.
// ---------------------------------------------------------------------------
static int route_through_socks(int fd, const char *host, uint32_t ip4,
                               uint16_t port) {
    // Remember and clear the caller's non-blocking flag for a clean handshake.
    int flags = fcntl(fd, F_GETFL, 0);
    int was_nonblock = (flags != -1) && (flags & O_NONBLOCK);

    int pfd = connect_to_proxy();
    if (pfd < 0) {
        plug_log("cannot reach proxy %s:%d", g_socks_host, g_socks_port);
        errno = ECONNREFUSED;
        return -1;
    }
    if (socks5_negotiate(pfd, host, ip4, port) != 0) {
        close(pfd);
        return -1;
    }

    // Splice the proxy socket onto the app's fd.
    if (dup2(pfd, fd) < 0) {
        close(pfd);
        return -1;
    }
    close(pfd);

    // Restore the caller's non-blocking mode if it had one.
    if (was_nonblock) {
        int nf = fcntl(fd, F_GETFL, 0);
        if (nf != -1) fcntl(fd, F_SETFL, nf | O_NONBLOCK);
    }
    if (host) plug_log("connect → SOCKS → %s:%u (remote DNS)", host, port);
    else plug_log("connect → SOCKS → %u.%u.%u.%u:%u",
                  (ip4 >> 24) & 0xff, (ip4 >> 16) & 0xff, (ip4 >> 8) & 0xff,
                  ip4 & 0xff, port);
    return 0;
}

// plug_connect is the shared implementation behind both the Linux override and
// the macOS interpose. It decides whether to hijack the connect and does so.
static int plug_connect(int fd, const struct sockaddr *addr, socklen_t len) {
    ensure_syms();

    // Pass through anything we don't handle: no proxy configured, no address,
    // or a non-stream socket. (SOCK_STREAM check below.)
    if (!addr || !socks_configured()) {
        return do_real_connect(fd, addr, len);
    }

    // Only TCP. Ask the socket its type; leave UDP/raw alone.
    int stype = 0;
    socklen_t slen = sizeof(stype);
    if (getsockopt(fd, SOL_SOCKET, SO_TYPE, &stype, &slen) == 0 &&
        stype != SOCK_STREAM) {
        return do_real_connect(fd, addr, len);
    }

    if (addr->sa_family == AF_INET) {
        const struct sockaddr_in *in = (const struct sockaddr_in *)addr;
        uint32_t ip = ntohl(in->sin_addr.s_addr);
        uint16_t port = ntohs(in->sin_port);
        if (is_loopback4(ip)) {
            return do_real_connect(fd, addr, len);  // 127/8 stays local
        }
        char host[256];
        if (lookup_fake(ip, host, sizeof(host))) {
            return route_through_socks(fd, host, 0, port);  // remote DNS
        }
        return route_through_socks(fd, NULL, ip, port);      // literal IPv4
    }

    if (addr->sa_family == AF_INET6) {
        const struct sockaddr_in6 *in6 = (const struct sockaddr_in6 *)addr;
        // Loopback ::1 stays local.
        static const unsigned char lo6[16] = {0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1};
        if (memcmp(&in6->sin6_addr, lo6, 16) == 0) {
            return do_real_connect(fd, addr, len);
        }
        // IPv4-mapped ::ffff:a.b.c.d — treat as IPv4 so it works over SOCKS.
        const unsigned char *b = (const unsigned char *)&in6->sin6_addr;
        static const unsigned char v4map[12] = {0,0,0,0,0,0,0,0,0,0,0xff,0xff};
        if (memcmp(b, v4map, 12) == 0) {
            uint32_t ip = ((uint32_t)b[12] << 24) | ((uint32_t)b[13] << 16) |
                          ((uint32_t)b[14] << 8) | (uint32_t)b[15];
            uint16_t port = ntohs(in6->sin6_port);
            if (is_loopback4(ip)) return do_real_connect(fd, addr, len);
            char host[256];
            if (lookup_fake(ip, host, sizeof(host)))
                return route_through_socks(fd, host, 0, port);
            return route_through_socks(fd, NULL, ip, port);
        }
        // A real IPv6 literal: our fake pool is IPv4-only, so we can't map it to
        // a name. SOCKS5 does support ATYP=IPv6, but plug's fake-DNS path
        // (getaddrinfo) hands out IPv4 fakes, so a genuine v6 literal here is a
        // direct numeric target. We pass it through unproxied rather than fail —
        // documented as a v6-literal gap. (Names still work: they resolve to a
        // v4 fake and go through SOCKS.)
        return do_real_connect(fd, addr, len);
    }

    // AF_UNIX and everything else: straight through.
    return do_real_connect(fd, addr, len);
}

// parse_service turns a service string ("8080" or "https") into a port. 0 if none.
static uint16_t parse_service(const char *service) {
    if (!service || !service[0]) return 0;
    char *end = NULL;
    long p = strtol(service, &end, 10);
    if (end && *end == '\0' && p >= 0 && p <= 65535) return (uint16_t)p;
    struct servent *se = getservbyname(service, NULL);
    if (se) return (uint16_t)ntohs((uint16_t)se->s_port);
    return 0;
}

// build_ai_v4 allocates a one-entry AF_INET addrinfo for (ip4 host-order, port).
// The app frees it via our freeaddrinfo shim. Returns 0 or EAI_MEMORY.
static int build_ai_v4(uint32_t ip4, uint16_t port, int socktype,
                       struct addrinfo **res) {
    struct addrinfo *ai = (struct addrinfo *)calloc(1, sizeof(struct addrinfo));
    if (!ai) return EAI_MEMORY;
    struct sockaddr_in *sa =
        (struct sockaddr_in *)calloc(1, sizeof(struct sockaddr_in));
    if (!sa) {
        free(ai);
        return EAI_MEMORY;
    }
    sa->sin_family = AF_INET;
    sa->sin_port = htons(port);
    sa->sin_addr.s_addr = htonl(ip4);
#ifdef __APPLE__
    sa->sin_len = sizeof(struct sockaddr_in);
#endif
    ai->ai_family = AF_INET;
    ai->ai_socktype = socktype ? socktype : SOCK_STREAM;
    ai->ai_protocol = 0;
    ai->ai_addrlen = sizeof(struct sockaddr_in);
    ai->ai_addr = (struct sockaddr *)sa;
    *res = ai;
    return 0;
}

// plug_getaddrinfo is the shared implementation for the DNS side. Because our
// interpose is total on macOS, EVERY addrinfo the app sees is built right here —
// we never call the real getaddrinfo (we couldn't reach it anyway).
static int plug_getaddrinfo(const char *node, const char *service,
                            const struct addrinfo *hints,
                            struct addrinfo **res) {
    ensure_syms();
    if (!res) return EAI_FAIL;

    int socktype = (hints ? hints->ai_socktype : 0);
    uint16_t port = parse_service(service);

    // Case 1: no node name (e.g. AI_PASSIVE bind lookups). Resolve/format with the
    // real resolver is not reachable; return a sensible loopback/any for the
    // common bind case, else fail. Callers using AI_PASSIVE bind to INADDR_ANY.
    if (!node) {
        uint32_t any =
            (hints && (hints->ai_flags & AI_PASSIVE)) ? 0 /*0.0.0.0*/
                                                      : 0x7f000001 /*127.0.0.1*/;
        return build_ai_v4(any, port, socktype, res);
    }

    // Case 2: node is a numeric literal. Format it directly (no lookup). connect()
    // still routes non-loopback literals through SOCKS.
    struct in_addr lit4;
    if ((hints && (hints->ai_flags & AI_NUMERICHOST)) ||
        inet_pton(AF_INET, node, &lit4) == 1) {
        uint32_t ip;
        if (inet_pton(AF_INET, node, &lit4) == 1) {
            ip = ntohl(lit4.s_addr);
        } else {
            // AI_NUMERICHOST but not v4 (maybe v6) — we can't serve v6 here.
            return EAI_NONAME;
        }
        return build_ai_v4(ip, port, socktype, res);
    }
    struct in6_addr lit6;
    if (inet_pton(AF_INET6, node, &lit6) == 1) {
        // A v6 literal: our synthesized results are v4-only. Fall back to a real
        // v4 resolution of the literal (won't apply) — instead just fail v6 here;
        // callers overwhelmingly also try v4. Documented v6-literal gap.
        return EAI_ADDRFAMILY;
    }

    // Case 3: a real hostname.
    if (socks_configured()) {
        // Proxy present: hand back a fake IP; connect() will recover the name and
        // let the SOCKS proxy resolve it cluster-side (socks5h remote DNS).
        uint32_t fake = remember_name(node);
        plug_log("getaddrinfo(%s) → fake 240.x %u.%u.%u.%u", node,
                 (fake >> 24) & 0xff, (fake >> 16) & 0xff, (fake >> 8) & 0xff,
                 fake & 0xff);
        return build_ai_v4(fake, port, socktype, res);
    }

    // No proxy (no-op mode): resolve for real with the genuine, non-interposed
    // resolver so the library is harmless when plug did not configure it. IPv4.
    uint32_t ip;
    if (resolve_ipv4_real(node, &ip)) {
        return build_ai_v4(ip, port, socktype, res);
    }
    return EAI_NONAME;
}

// plug_freeaddrinfo frees a result. Every addrinfo the app holds was built by
// our plug_getaddrinfo (interposition is total, so the real getaddrinfo is never
// invoked), and each is a single-entry {addrinfo, sockaddr_in} pair we calloc'd.
// So we simply free the chain ourselves — we never call the real freeaddrinfo
// (which, like getaddrinfo, we could not reach without recursing anyway).
static void plug_freeaddrinfo(struct addrinfo *ai) {
    while (ai) {
        struct addrinfo *next = ai->ai_next;
        if (ai->ai_addr) free(ai->ai_addr);
        if (ai->ai_canonname) free(ai->ai_canonname);
        free(ai);
        ai = next;
    }
}

// ---------------------------------------------------------------------------
// Platform binding.
//   * Linux: define connect/getaddrinfo/freeaddrinfo with external linkage; the
//     dynamic linker prefers our LD_PRELOAD'd definitions, and we reach the real
//     connect via dlsym(RTLD_NEXT).
//   * macOS: the two-level namespace means symbol overriding by name doesn't
//     work; we register an __interpose table so dyld swaps calls to the libc
//     functions for ours. The real connect is reached via the raw syscall; the
//     real getaddrinfo is never needed (we build every addrinfo ourselves).
// ---------------------------------------------------------------------------
#ifdef __APPLE__

int plug_connect_interpose(int fd, const struct sockaddr *addr, socklen_t len) {
    return plug_connect(fd, addr, len);
}
int plug_getaddrinfo_interpose(const char *node, const char *service,
                               const struct addrinfo *hints,
                               struct addrinfo **res) {
    return plug_getaddrinfo(node, service, hints, res);
}
void plug_freeaddrinfo_interpose(struct addrinfo *ai) { plug_freeaddrinfo(ai); }

// The __interpose section holds {replacement, original} function-pointer pairs.
typedef struct {
    const void *replacement;
    const void *original;
} interpose_t;

__attribute__((used)) static const interpose_t interposers[]
    __attribute__((section("__DATA,__interpose"))) = {
        {(const void *)plug_connect_interpose, (const void *)connect},
        {(const void *)plug_getaddrinfo_interpose, (const void *)getaddrinfo},
        {(const void *)plug_freeaddrinfo_interpose, (const void *)freeaddrinfo},
};

#else  // Linux and other ELF platforms

int connect(int fd, const struct sockaddr *addr, socklen_t len) {
    return plug_connect(fd, addr, len);
}
int getaddrinfo(const char *node, const char *service,
                const struct addrinfo *hints, struct addrinfo **res) {
    return plug_getaddrinfo(node, service, hints, res);
}
void freeaddrinfo(struct addrinfo *ai) { plug_freeaddrinfo(ai); }

#endif
