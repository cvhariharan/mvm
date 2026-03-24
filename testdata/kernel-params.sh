#!/bin/bash
set -euo pipefail

ARCH=x86_64
KERNEL_SRC="${1:?Usage: $0 <kernel-src>}"

cd "$KERNEL_SRC"

echo ">>> Generating base config..."
make ARCH=$ARCH defconfig
make ARCH=$ARCH kvm_guest.config

SC="scripts/config"

echo ">>> Applying microVM + Docker configs..."
# ---- Virtio / microVM ----
$SC --enable VIRTIO
$SC --enable VIRTIO_PCI
$SC --enable VIRTIO_BLK
$SC --enable VIRTIO_NET
$SC --enable VIRTIO_CONSOLE
$SC --enable VIRTIO_MMIO

# ---- Namespaces ----
$SC --enable NAMESPACES
$SC --enable UTS_NS
$SC --enable IPC_NS
$SC --enable USER_NS
$SC --enable PID_NS
$SC --enable NET_NS

# ---- Cgroups ----
$SC --enable CGROUPS
$SC --enable CGROUP_SCHED
$SC --enable CGROUP_PIDS
$SC --enable CGROUP_FREEZER
$SC --enable CGROUP_HUGETLB
$SC --enable CGROUP_DEVICE
$SC --enable CGROUP_CPUACCT
$SC --enable CGROUP_BPF
$SC --enable CGROUP_NET_PRIO
$SC --enable CGROUP_NET_CLASSID
$SC --enable CPUSETS
$SC --enable MEMCG
$SC --enable BLK_CGROUP
$SC --enable FAIR_GROUP_SCHED

# ---- Seccomp ----
$SC --enable SECCOMP
$SC --enable SECCOMP_FILTER

# ---- OverlayFS ----
$SC --enable OVERLAY_FS
$SC --enable OVERLAY_FS_REDIRECT_DIR
$SC --enable OVERLAY_FS_INDEX
$SC --enable OVERLAY_FS_METACOPY

# ---- Filesystems ----
$SC --enable EXT4_FS
$SC --enable EXT4_FS_POSIX_ACL
$SC --enable EXT4_FS_SECURITY
$SC --enable TMPFS
$SC --enable TMPFS_XATTR
$SC --enable TMPFS_POSIX_ACL
$SC --enable DEVTMPFS
$SC --enable DEVTMPFS_MOUNT
$SC --enable FHANDLE
$SC --enable POSIX_MQUEUE

# ---- Networking core ----
$SC --enable NET
$SC --enable INET
$SC --enable IPV6
$SC --enable PACKET
$SC --enable UNIX
$SC --enable NET_SCHED
$SC --enable NET_CLS_CGROUP
$SC --enable NET_L3_MASTER_DEV
$SC --enable IP_ROUTE_MULTIPATH

# ---- Netfilter core ----
$SC --enable NETFILTER
$SC --enable NETFILTER_ADVANCED
$SC --enable NF_CONNTRACK
$SC --enable NF_NAT
$SC --enable NF_NAT_MASQUERADE

# ---- iptables legacy stack ----
$SC --enable NETFILTER_XTABLES
$SC --enable IP_NF_IPTABLES_LEGACY
$SC --enable IP6_NF_IPTABLES_LEGACY
$SC --enable IP_NF_IPTABLES
$SC --enable IP_NF_FILTER
$SC --enable IP_NF_MANGLE
$SC --enable IP_NF_RAW
$SC --enable IP_NF_NAT
$SC --enable IP_NF_TARGET_MASQUERADE
$SC --enable IP_NF_TARGET_REJECT
$SC --enable IP6_NF_IPTABLES
$SC --enable IP6_NF_FILTER
$SC --enable IP6_NF_MANGLE
$SC --enable IP6_NF_NAT
$SC --enable IP6_NF_TARGET_REJECT

# ---- Netfilter xt matches / targets ----
$SC --enable NETFILTER_XT_MATCH_ADDRTYPE
$SC --enable NETFILTER_XT_MATCH_CONNTRACK
$SC --enable NETFILTER_XT_MATCH_MULTIPORT
$SC --enable NETFILTER_XT_MATCH_COMMENT
$SC --enable NETFILTER_XT_MATCH_CONNMARK
$SC --enable NETFILTER_XT_MATCH_STATE
$SC --enable NETFILTER_XT_MATCH_POLICY
$SC --enable NETFILTER_XT_NAT
$SC --enable NETFILTER_XT_TARGET_MASQUERADE
$SC --enable NETFILTER_XT_TARGET_MARK
$SC --enable NETFILTER_XT_TARGET_CHECKSUM
$SC --enable NETFILTER_XT_TARGET_REDIRECT
$SC --enable NETFILTER_XT_TARGET_TCPMSS
$SC --enable NETFILTER_XT_TARGET_NFLOG

# ---- Conntrack helpers ----
$SC --enable NF_CONNTRACK_FTP
$SC --enable NF_NAT_FTP

# ---- nftables ----
$SC --enable NF_TABLES
$SC --enable NF_TABLES_IPV4
$SC --enable NF_TABLES_IPV6
$SC --enable NF_TABLES_BRIDGE
$SC --enable NF_TABLES_INET
$SC --enable NF_TABLES_NETDEV
$SC --enable NFT_NAT
$SC --enable NFT_MASQ
$SC --enable NFT_COMPAT
$SC --enable NFT_CT
$SC --enable NFT_LOG
$SC --enable NFT_REJECT
$SC --enable NFT_REDIR

# ---- Bridge / veth networking ----
$SC --enable BRIDGE
$SC --enable BRIDGE_NETFILTER
$SC --enable VETH
$SC --enable MACVLAN
$SC --enable IPVLAN
$SC --enable DUMMY

# ---- Overlay / tunnel networking ----
$SC --enable VXLAN
$SC --enable TUN
$SC --enable XFRM_USER
$SC --enable XFRM_ALGO

# ---- Device mapper ----
$SC --enable DM_MIRROR
$SC --enable DM_ZERO

# ---- BPF ----
$SC --enable BPF
$SC --enable BPF_SYSCALL
$SC --enable BPF_JIT
$SC --enable CGROUP_BPF

# ---- Security ----
$SC --enable SECURITY
$SC --enable SECURITY_NETWORK
$SC --enable SECURITY_SELINUX
$SC --enable KEYS
$SC --enable INTEGRITY

# ---- QEMU microVM ----
$SC --enable ACPI
$SC --enable PCI
$SC --enable SERIAL_8250
$SC --enable SERIAL_8250_CONSOLE
$SC --enable NO_HZ_IDLE
$SC --enable HIGH_RES_TIMERS
$SC --set-val HZ 1000


echo ">>> Resolving dependencies..."
make ARCH=$ARCH olddefconfig

echo ""
echo ">>> Building kernel..."
make ARCH=$ARCH -j"$(nproc)"

echo ""
echo ">>> Done! Kernel at: arch/$ARCH/boot/bzImage"
