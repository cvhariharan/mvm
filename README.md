<a href="https://zerodha.tech">
  <img src="https://zerodha.tech/static/images/github-badge.svg" align="right" />
</a>

# mvm

mvm runs lightweight microVMs as development sandboxes. Point it at any container image and it boots a real Linux VM in seconds.

Your project directory gets mounted into the VM. You SSH in, do your work, and disconnect. The VM stays running in the background until you stop it.

## How it works

mvm uses QEMU's `microvm` machine type with KVM. It pulls a Linux kernel from an OCI registry, converts a container image into an immutable ext4 rootfs, and layers a writable data disk on top using overlayfs. An embedded init process detects the distro's package manager and sets up users, networking, mounts, and an OpenSSH server.

The host communicates with the guest over a virtio-serial control channel and SSH. Filesystems are shared via 9p.

## Why

- Boots in seconds
- Works with any container image (Alpine, Ubuntu, Fedora, Arch, etc.)
- Real VM isolation, not just namespaces
- Your files stay on the host, shared into the VM via 9p
- SSH access means VS Code Remote and Zed work out of the box
- Snapshots let you checkpoint and rollback the VM disk
- Port forwarding, services, and GUI app forwarding via waypipe (wip)
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

The `.mvm` file lives in your project root. You can also set global defaults in `~/.config/mvm/config.toml`. Project config overrides global config.

```toml
[vm]
image = "alpine:latest"
kernel = "ghcr.io/cvhariharan/mvm-kernel:latest"
memory = "512M"
cpus = 2
shell = "bash"
disk = "4G"

[network]
mode = "slirp"  # "slirp" (default) or "none"

[[network.ports]]
host = 8080
guest = 80
protocol = "tcp"  # optional, defaults to "tcp"

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

# Run services inside the VM
[[services]]
name = "dockerd"
cmd = "/usr/bin/dockerd --userland-proxy=false"
restart = true

# [[services]]
# name = "setup"
# cmd = "/usr/local/bin/setup.sh"
# oneshot = true          # run once and exit
# after = "dockerd"       # start after another service
```

## Commands

| Command                            | Description                                            |
| ---------------------------------- | ------------------------------------------------------ |
| `mvm init`                         | Create a `.mvm` config file                            |
| `mvm up`                           | Start or attach to the workspace VM (create if needed) |
| `mvm stop`                         | Stop the workspace VM                                  |
| `mvm destroy`                      | Stop and remove the workspace VM                       |
| `mvm rebuild`                      | Recreate the VM with a fresh rootfs and current config |
| `mvm attach`                       | Attach to a running VM's console                       |
| `mvm ssh`                          | SSH into a running VM                                  |
| `mvm ssh --code`                   | Open VS Code connected to the VM                       |
| `mvm ssh --zed`                    | Open Zed connected to the VM                           |
| `mvm ps`                           | List VMs                                               |
| `mvm create`                       | Create a VM without starting it                        |
| `mvm start <id>`                   | Start a created/stopped VM                             |
| `mvm rm <id>`                      | Remove a stopped VM                                    |
| `mvm build`                        | Pre-cache kernel and rootfs without running a VM       |
| `mvm resize <id> <size>`           | Resize the VM disk (e.g. `10G` or `+5G`)               |
| `mvm snapshot create <id> <name>`  | Snapshot the VM disk                                   |
| `mvm snapshot restore <id> <name>` | Restore a snapshot                                     |
| `mvm snapshot list <id>`           | List snapshots                                         |
| `mvm snapshot delete <id> <name>`  | Delete a snapshot                                      |
| `mvm kernel pull <ref>`            | Pull a kernel from an OCI registry                     |
| `mvm kernel list`                  | List cached kernels                                    |
| `mvm waypipe <app> [args]`         | Run a GUI app in the VM, forwarded to the host         |
