// supervisor.c — proof that seccomp-unotify intercepts a *Go* binary's raw
// connect() syscall (which bypasses libc → LD_PRELOAD can't see it) and
// REDIRECTS it. The child installs a seccomp filter that traps connect() and
// hands each one to us over a notification fd; we splice a socket we control
// onto the child's fd (SECCOMP_IOCTL_NOTIF_ADDFD) and answer "success". So the
// child ends up talking to wherever WE connected — transparently, at the kernel
// boundary → works for Go, static-C, anything.
//
// Prototype: every intercepted connect is redirected to a fixed local backend,
// to isolate the novel claim (seccomp catches Go's connect + ADDFD redirect).
// The real thing would SOCKS-connect to the intended target instead.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stddef.h>
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

// Fake-IP pool (like the libc hook): a cluster name resolves to one of these
// (here via /etc/hosts, standing in for plug's resolver); trapping the
// connect() to it lets us recover the NAME to hand to SOCKS (remote DNS).
#define FAKE_BASE 0xF0000000u
#define FAKE_MASK 0xF0000000u

#if defined(__x86_64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_X86_64
#elif defined(__aarch64__)
#define MY_AUDIT_ARCH AUDIT_ARCH_AARCH64
#else
#error "unsupported arch"
#endif

static int seccomp(unsigned int op, unsigned int flags, void *args) {
    return syscall(SYS_seccomp, op, flags, args);
}

// Trap connect(), allow everything else. Returns the notify fd.
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
    struct sock_fprog prog = {
        .len = sizeof(filter) / sizeof(filter[0]),
        .filter = filter,
    };
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

// reverse_hosts recovers the cluster name a fake IP maps to, by reading
// /etc/hosts (plug's resolver owns this map for real; /etc/hosts stands in).
static int reverse_hosts(const char *ip, char *name, size_t n) {
    FILE *f = fopen("/etc/hosts", "r");
    if (!f) return 0;
    char line[512], hip[64], hname[256];
    int found = 0;
    while (fgets(line, sizeof line, f)) {
        if (sscanf(line, "%63s %255s", hip, hname) == 2 && strcmp(hip, ip) == 0) {
            snprintf(name, n, "%s", hname);
            found = 1;
            break;
        }
    }
    fclose(f);
    return found;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <cmd> [args...]\n", argv[0]);
        return 2;
    }
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
        if (send_fd(sp[1], nfd) != 0) {
            perror("send_fd");
            _exit(127);
        }
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
    fprintf(stderr, "[supervisor] up — trapping connect(), redirecting to %s:%d\n", REDIRECT_IP, REDIRECT_PORT);

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

        if (req.data.nr == __NR_connect) {
            // Read the child's target; if it's a fake IP, recover the cluster
            // name — what the real path would send to SOCKS as ATYP=domain.
            struct sockaddr_in tgt;
            memset(&tgt, 0, sizeof tgt);
            unsigned long alen = req.data.args[2];
            if (alen > sizeof tgt) alen = sizeof tgt;
            struct iovec liov = {.iov_base = &tgt, .iov_len = alen};
            struct iovec riov = {.iov_base = (void *)req.data.args[1], .iov_len = alen};
            char ipstr[64] = "?";
            uint16_t port = 0;
            uint32_t iph = 0;
            if (process_vm_readv(req.pid, &liov, 1, &riov, 1, 0) > 0 && tgt.sin_family == AF_INET) {
                inet_ntop(AF_INET, &tgt.sin_addr, ipstr, sizeof ipstr);
                port = ntohs(tgt.sin_port);
                iph = ntohl(tgt.sin_addr.s_addr);
            }
            if ((iph & FAKE_MASK) == FAKE_BASE) {
                char name[256] = "?";
                reverse_hosts(ipstr, name, sizeof name);
                fprintf(stderr, "[supervisor] Go connect → fake %s:%u  ⇒  recovered cluster name '%s'  (→ SOCKS domain=%s:%u)\n",
                        ipstr, port, name, name, port);
            } else {
                fprintf(stderr, "[supervisor] Go connect → %s:%u (literal)\n", ipstr, port);
            }
            int s = socket(AF_INET, SOCK_STREAM, 0);
            struct sockaddr_in be;
            memset(&be, 0, sizeof be);
            be.sin_family = AF_INET;
            be.sin_port = htons(REDIRECT_PORT);
            inet_pton(AF_INET, REDIRECT_IP, &be.sin_addr);
            if (s >= 0 && connect(s, (struct sockaddr *)&be, sizeof be) == 0) {
                int fl = fcntl(s, F_GETFL, 0); // match Go's non-blocking expectation
                if (fl != -1) fcntl(s, F_SETFL, fl | O_NONBLOCK);
                struct seccomp_notif_addfd add;
                memset(&add, 0, sizeof add);
                add.id = req.id;
                add.flags = SECCOMP_ADDFD_FLAG_SETFD;
                add.srcfd = s;
                add.newfd = (unsigned int)req.data.args[0];
                if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_ADDFD, &add) < 0) {
                    perror("[supervisor] ADDFD");
                    resp.error = -ECONNREFUSED;
                } else {
                    resp.error = 0;
                    resp.val = 0; // connect() returns success; child fd is now ours
                }
            } else {
                resp.error = -ECONNREFUSED;
            }
            if (s >= 0) close(s);
        } else {
            resp.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE;
        }
        if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_SEND, &resp) != 0 && errno != ENOENT) {
            // ENOENT = child already gone for that id; ignore
        }
    }

    int status;
    waitpid(pid, &status, 0);
    fprintf(stderr, "[supervisor] child exited\n");
    return 0;
}
