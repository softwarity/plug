// plug_seccomp.c — plug's Go / statically-linked coverage supervisor (Linux).
//
// A libc app is covered by the LD_PRELOAD hook (plug_hook.c): it interposes
// getaddrinfo()/connect() in userspace and reroutes through the SOCKS proxy. Go
// bypasses libc for BOTH resolution (its own pure-Go DNS resolver) and the
// connection (raw connect(2) syscall), so the preload hook never sees it. This
// supervisor closes that gap at the KERNEL boundary, with no root:
//
//   * a seccomp user-notification filter traps the child's connect(2);
//   * an embedded DNS resolver answers the child's own lookups — a single-label
//     cluster name gets a fake IP from 240.0.0.0/4 (RFC 1112 class E, never
//     routed), a dotted name is resolved for real, AAAA is answered empty so the
//     child falls back to IPv4, and `localhost` stays loopback;
//   * when the trapped connect() targets a fake IP we recover the name and open
//     the connection through the SOCKS proxy with ATYP=domain (remote DNS, so
//     the cluster resolves it) — exactly what the preload hook does for libc;
//   * a connect() to the child's configured nameserver (port 53) is spliced onto
//     the embedded resolver, so the child never needs /etc/resolv.conf rewritten;
//   * everything else — loopback, a real/direct IP, IPv6 — is let through
//     untouched, so direct connectivity and the split-horizon are preserved.
//
// The trick that makes redirection possible without ptrace-rewriting arguments:
// SECCOMP_IOCTL_NOTIF_ADDFD with SECCOMP_ADDFD_FLAG_SETFD installs a socket WE
// prepared onto the child's own fd number, then we answer the connect() as
// success — the child reads/writes its fd none the wiser.
//
// Rootless: the only capabilities are no_new_privs + a seccomp user-notifier
// (both unprivileged) and process_vm_readv on our own direct child (allowed
// under the default Yama scope). If any of that is denied (e.g. a locked-down
// container without seccomp=unconfined), we simply exec the child unsupervised —
// the libc hook still covers libc apps. This wrapper NEVER prevents the child
// from running.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <pthread.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/uio.h>
#include <sys/wait.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>

// ---- header fallbacks (older toolchains may miss the ADDFD bits) -----------
#ifndef SECCOMP_USER_NOTIF_FLAG_CONTINUE
#define SECCOMP_USER_NOTIF_FLAG_CONTINUE (1UL << 0)
#endif
#ifndef SECCOMP_ADDFD_FLAG_SETFD
#define SECCOMP_ADDFD_FLAG_SETFD (1UL << 0)
#endif
#ifndef SECCOMP_IOCTL_NOTIF_ADDFD
struct seccomp_notif_addfd {
    __u64 id;
    __u32 flags;
    __u32 srcfd;
    __u32 newfd;
    __u32 newfd_flags;
};
#define SECCOMP_IOCTL_NOTIF_ADDFD _IOW('!', 3, struct seccomp_notif_addfd)
#endif

#define FAKE_BASE 0xF0000000u // 240.0.0.0
#define FAKE_MASK 0xF0000000u // /4

#if defined(__x86_64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_X86_64
#elif defined(__aarch64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_AARCH64
#else
#error "unsupported arch"
#endif

static int g_debug = 0;
#define LOG(...)                                       \
    do {                                               \
        if (g_debug) fprintf(stderr, "[plug-sup] " __VA_ARGS__); \
    } while (0)

// ---------------------------------------------------------------------------
// fake-IP table: name <-> fake IP, shared by the resolver thread and the
// connect handlers. Grows unbounded is unnecessary; a session opens few names.
// ---------------------------------------------------------------------------
struct fake {
    char name[256];
    uint32_t ip; // host order
};
static struct fake g_tab[4096];
static int g_len = 0;
static uint32_t g_next = 1;
static pthread_mutex_t g_lock = PTHREAD_MUTEX_INITIALIZER;

static uint32_t mint_fake(const char *name) {
    pthread_mutex_lock(&g_lock);
    for (int i = 0; i < g_len; i++)
        if (strcmp(g_tab[i].name, name) == 0) {
            uint32_t ip = g_tab[i].ip;
            pthread_mutex_unlock(&g_lock);
            return ip;
        }
    uint32_t ip = FAKE_BASE | (g_next++ & ~FAKE_MASK);
    if (ip == FAKE_BASE) ip = FAKE_BASE | (g_next++ & ~FAKE_MASK); // skip .0
    if (g_len < (int)(sizeof g_tab / sizeof g_tab[0])) {
        g_tab[g_len].ip = ip;
        snprintf(g_tab[g_len].name, sizeof g_tab[0].name, "%s", name);
        g_len++;
    }
    pthread_mutex_unlock(&g_lock);
    return ip;
}
static int lookup_fake(uint32_t ip, char *out, size_t n) {
    int found = 0;
    pthread_mutex_lock(&g_lock);
    for (int i = 0; i < g_len; i++)
        if (g_tab[i].ip == ip) {
            snprintf(out, n, "%s", g_tab[i].name);
            found = 1;
            break;
        }
    pthread_mutex_unlock(&g_lock);
    return found;
}

// ---------------------------------------------------------------------------
// SOCKS proxy address, parsed once from $PLUG_SOCKS ("host:port", tolerating a
// socks5h:// / socks5:// prefix). g_proxy_port == 0 means "no proxy".
// ---------------------------------------------------------------------------
static char g_proxy_host[128] = "127.0.0.1";
static int g_proxy_port = 0;

static void parse_proxy(void) {
    const char *s = getenv("PLUG_SOCKS");
    if (!s || !*s) return;
    if (strncmp(s, "socks5h://", 10) == 0) s += 10;
    else if (strncmp(s, "socks5://", 9) == 0) s += 9;
    const char *colon = strrchr(s, ':');
    if (!colon) return;
    size_t hlen = (size_t)(colon - s);
    if (hlen == 0 || hlen >= sizeof g_proxy_host) return;
    memcpy(g_proxy_host, s, hlen);
    g_proxy_host[hlen] = 0;
    g_proxy_port = atoi(colon + 1);
}

// ---------------------------------------------------------------------------
// blocking, complete I/O — the SOCKS handshake needs ordered whole reads/writes
// ---------------------------------------------------------------------------
static ssize_t write_all(int fd, const void *buf, size_t n) {
    const char *p = buf;
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
    char *p = buf;
    size_t off = 0;
    while (off < n) {
        ssize_t r = recv(fd, p + off, n - off, 0);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (r == 0) return -1;
        off += (size_t)r;
    }
    return (ssize_t)off;
}

// socks5_negotiate — RFC 1928 no-auth handshake + CONNECT to (host, port) with
// ATYP=domain, so the cluster's DNS resolves the name (socks5h). Returns 0 ok.
static int socks5_negotiate(int fd, const char *host, uint16_t port) {
    unsigned char greet[3] = {0x05, 0x01, 0x00};
    if (write_all(fd, greet, sizeof greet) < 0) return -1;
    unsigned char sel[2];
    if (read_all(fd, sel, sizeof sel) < 0) return -1;
    if (sel[0] != 0x05 || sel[1] != 0x00) return -1;

    unsigned char req[262];
    size_t n = 0;
    req[n++] = 0x05; // VER
    req[n++] = 0x01; // CONNECT
    req[n++] = 0x00; // RSV
    size_t hlen = strlen(host);
    if (hlen > 255) hlen = 255;
    req[n++] = 0x03; // ATYP = domain
    req[n++] = (unsigned char)hlen;
    memcpy(req + n, host, hlen);
    n += hlen;
    req[n++] = (unsigned char)((port >> 8) & 0xff);
    req[n++] = (unsigned char)(port & 0xff);
    if (write_all(fd, req, n) < 0) return -1;

    unsigned char rep[4];
    if (read_all(fd, rep, sizeof rep) < 0) return -1;
    if (rep[0] != 0x05 || rep[1] != 0x00) return -1;
    size_t skip;
    switch (rep[3]) {
        case 0x01: skip = 4; break;
        case 0x04: skip = 16; break;
        case 0x03: {
            unsigned char l;
            if (read_all(fd, &l, 1) < 0) return -1;
            skip = l;
            break;
        }
        default: return -1;
    }
    unsigned char scratch[256 + 2];
    if (read_all(fd, scratch, skip + 2) < 0) return -1;
    return 0;
}

static int dial_ipv4(const char *host, uint16_t port) {
    int s = socket(AF_INET, SOCK_STREAM, 0);
    if (s < 0) return -1;
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof sa);
    sa.sin_family = AF_INET;
    sa.sin_port = htons(port);
    if (inet_pton(AF_INET, host, &sa.sin_addr) != 1) {
        close(s);
        return -1;
    }
    if (connect(s, (struct sockaddr *)&sa, sizeof sa) != 0) {
        close(s);
        return -1;
    }
    return s;
}

// ---------------------------------------------------------------------------
// embedded DNS resolver — bound to an ephemeral loopback port. The child's
// connect() to <nameserver>:53 is spliced onto a socket connected here, so its
// pure-Go resolver queries us without any resolv.conf change.
// ---------------------------------------------------------------------------
static int g_resolver_fd = -1;
static uint16_t g_resolver_port = 0; // host order

static int ends_with_ci(const char *s, const char *suf) {
    size_t ls = strlen(s), lf = strlen(suf);
    return ls >= lf && strcasecmp(s + ls - lf, suf) == 0;
}

static void *dns_thread(void *arg) {
    (void)arg;
    for (;;) {
        unsigned char q[512];
        struct sockaddr_in from;
        socklen_t fl = sizeof from;
        ssize_t n = recvfrom(g_resolver_fd, q, sizeof q, 0, (struct sockaddr *)&from, &fl);
        if (n < 13) continue;

        // parse the first question: name labels from offset 12, then qtype
        char name[256];
        int p = 12, np = 0;
        while (p < n && q[p] != 0 && np < 250) {
            int len = q[p++];
            if (np > 0) name[np++] = '.';
            for (int i = 0; i < len && p < n && np < 250; i++) name[np++] = q[p++];
        }
        name[np] = 0;
        p++; // root label
        if (p + 4 > n) continue;
        int qtype = (q[p] << 8) | q[p + 1];
        int qend = p + 4;

        int is_single = (np > 0) && (strchr(name, '.') == NULL);
        int is_local = strcasecmp(name, "localhost") == 0 || ends_with_ci(name, ".localhost");

        uint32_t answer_ip = 0; // host order; 0 => no A answer
        int rcode = 0;          // 0 ok, 3 NXDOMAIN

        if (qtype == 28) {
            // AAAA — answer empty (NOERROR/NODATA) so the child uses IPv4.
        } else if (qtype != 1) {
            // anything other than A — NODATA.
        } else if (is_local) {
            answer_ip = INADDR_LOOPBACK; // 127.0.0.1 — stay local
        } else if (is_single) {
            answer_ip = mint_fake(name); // cluster name -> fake, routed via SOCKS
        } else {
            // dotted name — resolve for real (we are in the host netns).
            struct addrinfo hints, *res = NULL;
            memset(&hints, 0, sizeof hints);
            hints.ai_family = AF_INET;
            hints.ai_socktype = SOCK_STREAM;
            if (getaddrinfo(name, NULL, &hints, &res) == 0 && res) {
                answer_ip = ntohl(((struct sockaddr_in *)res->ai_addr)->sin_addr.s_addr);
                freeaddrinfo(res);
            } else {
                rcode = 3; // NXDOMAIN
            }
        }

        unsigned char r[600];
        int rl = 0;
        r[rl++] = q[0];
        r[rl++] = q[1];                                  // id
        r[rl++] = 0x81;                                  // QR=1, RD
        r[rl++] = 0x80 | (answer_ip ? 0 : rcode);        // RA + RCODE
        r[rl++] = 0; r[rl++] = 1;                        // QDCOUNT
        r[rl++] = 0; r[rl++] = answer_ip ? 1 : 0;        // ANCOUNT
        r[rl++] = 0; r[rl++] = 0;                        // NSCOUNT
        r[rl++] = 0; r[rl++] = 0;                        // ARCOUNT
        memcpy(r + rl, q + 12, qend - 12);               // echo the question
        rl += qend - 12;
        if (answer_ip) {
            r[rl++] = 0xC0; r[rl++] = 0x0C;              // name pointer
            r[rl++] = 0; r[rl++] = 1;                    // TYPE A
            r[rl++] = 0; r[rl++] = 1;                    // CLASS IN
            r[rl++] = 0; r[rl++] = 0; r[rl++] = 0; r[rl++] = 30; // TTL
            r[rl++] = 0; r[rl++] = 4;                    // RDLENGTH
            uint32_t nip = htonl(answer_ip);
            memcpy(r + rl, &nip, 4);
            rl += 4;
        }
        sendto(g_resolver_fd, r, rl, 0, (struct sockaddr *)&from, fl);
    }
    return NULL;
}

// start_resolver binds the loopback UDP socket and launches the thread. Returns
// 0 on success; on failure the resolver stays off (DNS is let through for real).
static int start_resolver(void) {
    int s = socket(AF_INET, SOCK_DGRAM, 0);
    if (s < 0) return -1;
    struct sockaddr_in a;
    memset(&a, 0, sizeof a);
    a.sin_family = AF_INET;
    a.sin_port = 0; // ephemeral
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (bind(s, (struct sockaddr *)&a, sizeof a) != 0) {
        close(s);
        return -1;
    }
    socklen_t al = sizeof a;
    if (getsockname(s, (struct sockaddr *)&a, &al) != 0) {
        close(s);
        return -1;
    }
    g_resolver_fd = s;
    g_resolver_port = ntohs(a.sin_port);
    pthread_t th;
    if (pthread_create(&th, NULL, dns_thread, NULL) != 0) {
        close(s);
        g_resolver_fd = -1;
        return -1;
    }
    pthread_detach(th);
    LOG("resolver up on 127.0.0.1:%u\n", g_resolver_port);
    return 0;
}

// ---------------------------------------------------------------------------
// seccomp plumbing
// ---------------------------------------------------------------------------
static int seccomp(unsigned int op, unsigned int flags, void *args) {
    return syscall(SYS_seccomp, op, flags, args);
}

static int install_filter(void) {
    struct sock_filter filter[] = {
        BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, arch)),
        BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, MY_AUDIT_ARCH, 1, 0),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
        BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
        BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_connect, 0, 1),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_USER_NOTIF),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
    };
    struct sock_fprog prog = {.len = sizeof filter / sizeof filter[0], .filter = filter};
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) return -1;
    return seccomp(SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_NEW_LISTENER, &prog);
}

static int send_fd(int sock, int fd) {
    char buf[CMSG_SPACE(sizeof(int))];
    memset(buf, 0, sizeof buf);
    char c = 'x';
    struct iovec iov = {.iov_base = &c, .iov_len = 1};
    struct msghdr msg = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof buf};
    struct cmsghdr *cm = CMSG_FIRSTHDR(&msg);
    cm->cmsg_level = SOL_SOCKET;
    cm->cmsg_type = SCM_RIGHTS;
    cm->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(cm), &fd, sizeof(int));
    return sendmsg(sock, &msg, 0) < 0 ? -1 : 0;
}
static int recv_fd(int sock) {
    char buf[CMSG_SPACE(sizeof(int))];
    memset(buf, 0, sizeof buf);
    char c;
    struct iovec iov = {.iov_base = &c, .iov_len = 1};
    struct msghdr msg = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof buf};
    if (recvmsg(sock, &msg, 0) < 0) return -1;
    struct cmsghdr *cm = CMSG_FIRSTHDR(&msg);
    if (!cm || cm->cmsg_type != SCM_RIGHTS) return -1;
    int fd;
    memcpy(&fd, CMSG_DATA(cm), sizeof(int));
    return fd;
}

// install our prepared socket (already connected) onto the child's fd number,
// after making it non-blocking + close-on-exec to match a runtime's own socket.
static int addfd_onto(int nfd, __u64 id, int srcfd, unsigned int childfd) {
    int fl = fcntl(srcfd, F_GETFL, 0);
    if (fl != -1) fcntl(srcfd, F_SETFL, fl | O_NONBLOCK);
    struct seccomp_notif_addfd add;
    memset(&add, 0, sizeof add);
    add.id = id;
    add.flags = SECCOMP_ADDFD_FLAG_SETFD;
    add.srcfd = (unsigned)srcfd;
    add.newfd = childfd;
    add.newfd_flags = O_CLOEXEC;
    return ioctl(nfd, SECCOMP_IOCTL_NOTIF_ADDFD, &add) < 0 ? -1 : 0;
}

static void notif_send(int nfd, __u64 id, __s32 error, __u32 flags) {
    struct seccomp_notif_resp resp;
    memset(&resp, 0, sizeof resp);
    resp.id = id;
    resp.error = error;
    resp.flags = flags;
    if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_SEND, &resp) != 0 && errno != ENOENT)
        LOG("notif_send: %s\n", strerror(errno));
}

// ---- SOCKS-by-name worker (one detached thread per cluster connect) --------
struct work {
    int nfd;
    __u64 id;
    unsigned int childfd;
    char name[256];
    uint16_t port;
};

static void *socks_worker(void *arg) {
    struct work *w = arg;
    if (g_proxy_port == 0) {
        notif_send(w->nfd, w->id, ECONNREFUSED, 0);
        free(w);
        return NULL;
    }
    int s = dial_ipv4(g_proxy_host, (uint16_t)g_proxy_port);
    if (s < 0 || socks5_negotiate(s, w->name, w->port) != 0) {
        if (s >= 0) close(s);
        LOG("SOCKS %s:%u failed\n", w->name, w->port);
        notif_send(w->nfd, w->id, ECONNREFUSED, 0);
        free(w);
        return NULL;
    }
    if (addfd_onto(w->nfd, w->id, s, w->childfd) != 0) {
        close(s);
        notif_send(w->nfd, w->id, ECONNREFUSED, 0);
        free(w);
        return NULL;
    }
    close(s);
    notif_send(w->nfd, w->id, 0, 0); // connect() -> success
    LOG("cluster %s:%u -> SOCKS ok\n", w->name, w->port);
    free(w);
    return NULL;
}

// splice the child's DNS connect onto a socket connected to our resolver.
static int redirect_dns(int nfd, __u64 id, unsigned int childfd) {
    int s = socket(AF_INET, SOCK_DGRAM, 0);
    if (s < 0) return -1;
    struct sockaddr_in a;
    memset(&a, 0, sizeof a);
    a.sin_family = AF_INET;
    a.sin_port = htons(g_resolver_port);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (connect(s, (struct sockaddr *)&a, sizeof a) != 0 || addfd_onto(nfd, id, s, childfd) != 0) {
        close(s);
        return -1;
    }
    close(s);
    return 0;
}

// ---------------------------------------------------------------------------
// child management
// ---------------------------------------------------------------------------
static volatile pid_t g_child = 0;

static void forward_signal(int sig) {
    if (g_child > 0) kill(g_child, sig);
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <cmd> [args...]\n", argv[0]);
        return 2;
    }
    const char *dbg = getenv("PLUG_HOOK_DEBUG");
    g_debug = dbg && *dbg && strcmp(dbg, "0") != 0;

    parse_proxy();
    start_resolver(); // best effort; off => DNS let through untouched

    int sp[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sp) != 0) {
        // cannot supervise — run the child directly so plug never breaks.
        execvp(argv[1], &argv[1]);
        perror("execvp");
        return 127;
    }

    pid_t pid = fork();
    if (pid < 0) {
        execvp(argv[1], &argv[1]);
        perror("execvp");
        return 127;
    }

    if (pid == 0) {
        // ---- child: install the filter, hand the notifier back, exec the app.
        close(sp[0]);
        int nfd = install_filter();
        if (nfd >= 0) {
            if (send_fd(sp[1], nfd) != 0) { /* parent will just wait */
            }
            close(nfd);
        }
        // else: no filter (seccomp denied) — parent's recv_fd fails, we run
        // unsupervised. The libc hook still covers libc apps.
        close(sp[1]);

        // Re-inject the preload hook for the APP only (the supervisor itself ran
        // without it — no PLUG_SOCKS in our env, so our own resolver/SOCKS calls
        // stayed real). PLUG_PRELOAD carries the merged LD_PRELOAD value.
        const char *pre = getenv("PLUG_PRELOAD");
        if (pre && *pre) setenv("LD_PRELOAD", pre, 1);
        unsetenv("PLUG_PRELOAD");

        execvp(argv[1], &argv[1]);
        perror("execvp");
        _exit(127);
    }

    // ---- parent: the supervisor.
    close(sp[1]);
    g_child = pid;

    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = forward_signal;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);
    sigaction(SIGHUP, &sa, NULL);
    sigaction(SIGQUIT, &sa, NULL);

    int nfd = recv_fd(sp[0]);
    close(sp[0]);

    if (nfd >= 0) {
        LOG("supervising pid %d (resolver %s, proxy %s:%d)\n", pid,
            g_resolver_fd >= 0 ? "on" : "off", g_proxy_host, g_proxy_port);
        for (;;) {
            struct seccomp_notif req;
            memset(&req, 0, sizeof req);
            if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_RECV, &req) != 0) {
                if (errno == EINTR) continue;
                break; // child gone / notifier closed
            }
            if (req.data.nr != __NR_connect) {
                notif_send(nfd, req.id, 0, SECCOMP_USER_NOTIF_FLAG_CONTINUE);
                continue;
            }

            // read the child's target sockaddr (it is blocked in connect(), so
            // the memory is stable for the lifetime of this notification).
            struct sockaddr_storage ss;
            memset(&ss, 0, sizeof ss);
            unsigned long alen = req.data.args[2];
            if (alen > sizeof ss) alen = sizeof ss;
            struct iovec liov = {.iov_base = &ss, .iov_len = alen};
            struct iovec riov = {.iov_base = (void *)req.data.args[1], .iov_len = alen};
            int got = process_vm_readv(req.pid, &liov, 1, &riov, 1, 0) > 0;

            if (!got || ss.ss_family != AF_INET) {
                // IPv6 / unix / unreadable — let the kernel run it as-is.
                notif_send(nfd, req.id, 0, SECCOMP_USER_NOTIF_FLAG_CONTINUE);
                continue;
            }
            struct sockaddr_in *sin = (struct sockaddr_in *)&ss;
            uint32_t iph = ntohl(sin->sin_addr.s_addr);
            uint16_t port = ntohs(sin->sin_port);

            if (port == 53 && g_resolver_fd >= 0) {
                if (redirect_dns(nfd, req.id, (unsigned)req.data.args[0]) == 0)
                    notif_send(nfd, req.id, 0, 0);
                else
                    notif_send(nfd, req.id, 0, SECCOMP_USER_NOTIF_FLAG_CONTINUE);
            } else if ((iph & FAKE_MASK) == FAKE_BASE) {
                struct work *w = calloc(1, sizeof *w);
                if (!w) {
                    notif_send(nfd, req.id, ECONNREFUSED, 0);
                    continue;
                }
                w->nfd = nfd;
                w->id = req.id;
                w->childfd = (unsigned)req.data.args[0];
                w->port = port;
                lookup_fake(iph, w->name, sizeof w->name);
                if (w->name[0] == 0) snprintf(w->name, sizeof w->name, "%u.%u.%u.%u",
                                              (iph >> 24) & 0xff, (iph >> 16) & 0xff,
                                              (iph >> 8) & 0xff, iph & 0xff);
                pthread_t th;
                if (pthread_create(&th, NULL, socks_worker, w) != 0) {
                    free(w);
                    notif_send(nfd, req.id, ECONNREFUSED, 0);
                } else {
                    pthread_detach(th);
                }
            } else {
                // loopback or a real/direct IP — untouched (host netns has the
                // network; the split-horizon direct path is preserved).
                notif_send(nfd, req.id, 0, SECCOMP_USER_NOTIF_FLAG_CONTINUE);
            }
        }
        close(nfd);
    } else {
        LOG("running pid %d unsupervised (seccomp unavailable)\n", pid);
    }

    int status = 0;
    while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {
    }
    if (WIFSIGNALED(status)) return 128 + WTERMSIG(status);
    return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
}
