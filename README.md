# mvm

mvm runs lightweight microVMs as development sandboxes. Point it at any container image and it boots a real Linux VM in about a second.

Your project directory gets mounted into the VM. You SSH in, do your work, and disconnect. The VM stays running in the background until you stop it.

## How it works

mvm uses QEMU's `microvm` machine type with KVM. It pulls a Linux kernel from an OCI registry, converts a container image into an ext4 rootfs, and boots the VM with an embedded init process that sets up users, networking, mounts, and an OpenSSH server.

The host communicates with the guest over a virtio-serial control channel and SSH. Filesystems are shared via 9p.

## Why

- Boots in ~1 second with KVM
- Works with any container image (Alpine, Ubuntu, Fedora, etc.)
- Real VM isolation, not just namespaces
- Your files stay on the host, shared into the VM via 9p
- SSH access means VS Code Remote and Zed work out of the box
- Snapshots let you checkpoint and rollback the VM disk
- No root required

## Requirements

- QEMU (`qemu-system-x86_64`)
- Docker or Podman (for pulling container images)

## Build

```
make
```

This compiles the init binary, packs it into an initrd, and builds the `mvm` CLI at `bin/mvm`.

## Quick start

Create a config file in your project:

```
mvm init
```

Edit `.mvm` to pick an image:

```toml
[vm]
image = "alpine:latest"
memory = "512M"
cpus = 2
```

Start the VM:

```
mvm up
```

This creates the VM, boots it, and drops you into an SSH session. Press Ctrl+D to disconnect. The VM keeps running.

Reattach:

```
mvm attach
```

Stop it:

```
mvm stop
```

## Config

The `.mvm` file lives in your project root.

```toml
[vm]
image = "alpine:latest"
kernel = "ghcr.io/mvm/kernel:latest"
memory = "512M"
cpus = 2

[network]
mode = "slirp"

[mounts]
mount_workspace = true

# Mount a directory
[[mounts.entries]]
src = "/home/user/.config/git"
dest = "/home/user/.config/git"

# Mount a single file
[[mounts.entries]]
src = "/home/user/.gitconfig"
dest = "/home/user/.gitconfig"
is_file = true

[env]
EDITOR = "vim"
```

## Commands

| Command                            | Description                         |
| ---------------------------------- | ----------------------------------- |
| `mvm up`                           | Start or attach to the workspace VM |
| `mvm stop`                         | Stop the workspace VM               |
| `mvm destroy`                      | Stop and remove the workspace VM    |
| `mvm rebuild`                      | Rebuild the VM with current config  |
| `mvm attach`                       | SSH into a running VM               |
| `mvm ssh`                          | Connect via system SSH client       |
| `mvm ssh --code`                   | Open VS Code connected to the VM    |
| `mvm ssh --zed`                    | Open Zed connected to the VM        |
| `mvm run -- <cmd>`                 | Run a command in an ephemeral VM    |
| `mvm ps`                           | List VMs                            |
| `mvm create`                       | Create a VM without starting it     |
| `mvm start <id>`                   | Start a created/stopped VM          |
| `mvm rm <id>`                      | Remove a stopped VM                 |
| `mvm snapshot create <id> <name>`  | Snapshot the VM disk                |
| `mvm snapshot restore <id> <name>` | Restore a snapshot                  |
| `mvm resize <id> <size>`           | Resize the VM disk                  |
