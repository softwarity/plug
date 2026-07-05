// supervisor.c (v3) — proves the FULL, self-contained Linux/Go path:
//
//   1. an embedded DNS server mints a fake IP (240/4) for a cluster name
//      (single-label) — like the libc hook's getaddrinfo, but Go uses it via
//      normal DNS instead of getaddrinfo;
//   2. a seccomp filter traps the child's connect(); a connect to the fake IP
//      recovers the name (what the real path hands to SOCKS as ATYP=domain) and
//      splices the connection.
//
// Go bypasses libc for BOTH resolution (pure-Go resolver) and connect() (raw
// syscall) — so this is the only way to make a Go binary reach a cluster
// service BY NAME. Prototype: the service connect is redirected to a fixed
// backend to prove the splice; the real path SOCKS-connects using the name.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/uio.h>
#include <sys/wait.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>

#define REDIRECT_IP "127.0.0.1"
#define REDIRECT_PORT 18080
#define DNS_PORT 53 // the embedded resolver binds loopback:53

#define FAKE_BASE 0xF0000000u // 240.0.0.0
#define FAKE_MASK 0xF0000000u // /4

#if defined(__x86_64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_X86_64
#elif defined(__aarch64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_AARCH64
#else
#error "unsupported arch"
#endif

// ---- fake-IP table (name <-> fake), shared by the DNS thread and the handler ----
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
    if (g_len < 4096) {
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

// ---- minimal DNS server: A query for a single-label name -> a fake IP ----
static void *dns_thread(void *arg) {
    (void)arg;
    int s = socket(AF_INET, SOCK_DGRAM, 0);
    struct sockaddr_in a;
    memset(&a, 0, sizeof a);
    a.sin_family = AF_INET;
    a.sin_port = htons(DNS_PORT);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (bind(s, (struct sockaddr *)&a, sizeof a) != 0) {
        perror("[dns] bind");
        return NULL;
    }
    fprintf(stderr, "[dns] resolver up on 127.0.0.1:%d\n", DNS_PORT);
    for (;;) {
        unsigned char q[512];
        struct sockaddr_in from;
        socklen_t fl = sizeof from;
        ssize_t n = recvfrom(s, q, sizeof q, 0, (struct sockaddr *)&from, &fl);
        if (n < 13) continue;
        // parse the first question's name (labels from offset 12)
        char name[256];
        int p = 12, np = 0;
        while (p < n && q[p] != 0 && np < 250) {
            int len = q[p++];
            for (int i = 0; i < len && p < n && np < 250; i++) {
                if (i == 0 && np > 0) name[np++] = '.';
                else if (i == 0 && np == 0) { /* first label */ }
                name[np++] = q[p++];
            }
        }
        name[np] = 0;
        p++;              // skip the root 0
        int qend = p + 4; // + qtype + qclass
        if (qend > n) continue;

        int single = (np > 0) && (strchr(name, '.') == NULL);
        uint32_t fake = single ? mint_fake(name) : 0;

        unsigned char r[600];
        int rl = 0;
        r[rl++] = q[0];
        r[rl++] = q[1];                          // id
        r[rl++] = 0x81;                          // QR=1, RD
        r[rl++] = single ? 0x80 : 0x83;          // RA + RCODE (0 or NXDOMAIN)
        r[rl++] = 0; r[rl++] = 1;                // QDCOUNT
        r[rl++] = 0; r[rl++] = single ? 1 : 0;   // ANCOUNT
        r[rl++] = 0; r[rl++] = 0;                // NSCOUNT
        r[rl++] = 0; r[rl++] = 0;                // ARCOUNT
        memcpy(r + rl, q + 12, qend - 12);       // copy the question
        rl += qend - 12;
        if (single) {
            r[rl++] = 0xC0; r[rl++] = 0x0C;      // name pointer to the question
            r[rl++] = 0; r[rl++] = 1;            // TYPE A
            r[rl++] = 0; r[rl++] = 1;            // CLASS IN
            r[rl++] = 0; r[rl++] = 0; r[rl++] = 0; r[rl++] = 60; // TTL
            r[rl++] = 0; r[rl++] = 4;            // RDLENGTH
            uint32_t nip = htonl(fake);
            memcpy(r + rl, &nip, 4);
            rl += 4;
            fprintf(stderr, "[dns] %s -> fake %u.%u.%u.%u\n", name,
                    (fake >> 24) & 0xff, (fake >> 16) & 0xff, (fake >> 8) & 0xff, fake & 0xff);
        }
        sendto(s, r, rl, 0, (struct sockaddr *)&from, fl);
    }
}

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
    struct sock_fprog prog = {.len = sizeof(filter) / sizeof(filter[0]), .filter = filter};
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) {
        perror("no_new_privs");
        return -1;
    }
    int fd = seccomp(SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_NEW_LISTENER, &prog);
    if (fd < 0) perror("seccomp NEW_LISTENER");
    return fd;
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
    int fd;
    memcpy(&fd, CMSG_DATA(cm), sizeof(int));
    return fd;
}

// splice a socket we control (connected to `to`) onto the child's fd.
static int splice_onto(int nfd, __u64 id, unsigned int childfd, struct sockaddr_in *to) {
    int s = socket(AF_INET, SOCK_STREAM, 0);
    if (s < 0) return -1;
    if (connect(s, (struct sockaddr *)to, sizeof *to) != 0) {
        close(s);
        return -1;
    }
    int fl = fcntl(s, F_GETFL, 0);
    if (fl != -1) fcntl(s, F_SETFL, fl | O_NONBLOCK);
    struct seccomp_notif_addfd add;
    memset(&add, 0, sizeof add);
    add.id = id;
    add.flags = SECCOMP_ADDFD_FLAG_SETFD;
    add.srcfd = s;
    add.newfd = childfd;
    int rc = ioctl(nfd, SECCOMP_IOCTL_NOTIF_ADDFD, &add);
    close(s);
    return rc < 0 ? -1 : 0;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <cmd> [args...]\n", argv[0]);
        return 2;
    }
    pthread_t dns;
    pthread_create(&dns, NULL, dns_thread, NULL);
    usleep(200000); // let the resolver bind

    int sp[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sp) != 0) {
        perror("socketpair");
        return 1;
    }
    pid_t pid = fork();
    if (pid < 0) {
        perror("fork");
        return 1;
    }
    if (pid == 0) {
        close(sp[0]);
        int nfd = install_filter();
        if (nfd < 0) _exit(127);
        if (send_fd(sp[1], nfd) != 0) _exit(127);
        close(nfd);
        close(sp[1]);
        execvp(argv[1], &argv[1]);
        perror("execvp");
        _exit(127);
    }

    close(sp[1]);
    int nfd = recv_fd(sp[0]);
    if (nfd < 0) {
        fprintf(stderr, "[supervisor] recv_fd failed\n");
        return 1;
    }
    fprintf(stderr, "[supervisor] up — resolver + connect() trap\n");

    for (;;) {
        struct seccomp_notif req;
        memset(&req, 0, sizeof req);
        if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_RECV, &req) != 0) {
            if (errno == EINTR) continue;
            break;
        }
        struct seccomp_notif_resp resp;
        memset(&resp, 0, sizeof resp);
        resp.id = req.id;

        if (req.data.nr != __NR_connect) {
            resp.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE;
            ioctl(nfd, SECCOMP_IOCTL_NOTIF_SEND, &resp);
            continue;
        }

        // read the child's target
        struct sockaddr_in tgt;
        memset(&tgt, 0, sizeof tgt);
        unsigned long alen = req.data.args[2];
        if (alen > sizeof tgt) alen = sizeof tgt;
        struct iovec liov = {.iov_base = &tgt, .iov_len = alen};
        struct iovec riov = {.iov_base = (void *)req.data.args[1], .iov_len = alen};
        uint32_t iph = 0;
        uint16_t port = 0;
        if (process_vm_readv(req.pid, &liov, 1, &riov, 1, 0) > 0 && tgt.sin_family == AF_INET) {
            iph = ntohl(tgt.sin_addr.s_addr);
            port = ntohs(tgt.sin_port);
        }

        if (iph >> 24 == 127) {
            // loopback (incl. our DNS on 127.0.0.1:53) — let it run for real
            resp.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE;
        } else if ((iph & FAKE_MASK) == FAKE_BASE) {
            char name[256] = "?";
            lookup_fake(iph, name, sizeof name);
            fprintf(stderr, "[supervisor] Go connect → fake %u.%u.%u.%u:%u  ⇒  cluster name '%s'  (→ SOCKS domain=%s:%u)\n",
                    (iph >> 24) & 0xff, (iph >> 16) & 0xff, (iph >> 8) & 0xff, iph & 0xff, port, name, name, port);
            struct sockaddr_in be;
            memset(&be, 0, sizeof be);
            be.sin_family = AF_INET;
            be.sin_port = htons(REDIRECT_PORT);
            inet_pton(AF_INET, REDIRECT_IP, &be.sin_addr);
            resp.error = splice_onto(nfd, req.id, (unsigned int)req.data.args[0], &be) == 0 ? 0 : -ECONNREFUSED;
        } else {
            fprintf(stderr, "[supervisor] Go connect → %u.%u.%u.%u:%u (literal, passthrough)\n",
                    (iph >> 24) & 0xff, (iph >> 16) & 0xff, (iph >> 8) & 0xff, iph & 0xff, port);
            resp.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE;
        }
        if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_SEND, &resp) != 0 && errno != ENOENT) {
            // child gone; ignore
        }
    }

    int status;
    waitpid(pid, &status, 0);
    fprintf(stderr, "[supervisor] child exited\n");
    return 0;
}
