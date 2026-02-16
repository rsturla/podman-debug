# podman-debug

> **Proof of concept.** This project was built with heavy use of AI. It should
> be judged on functionality and the ideas it demonstrates, not on code quality
> or production-readiness.

A standalone tool that gives you a debug shell into **any** container or image
managed by Podman -- even shell-less / distroless ones.  It works by overlaying
a [Nix](https://nixos.org/) toolbox image onto the target's filesystem, giving
you access to thousands of debugging tools (`curl`, `strace`, `gdb`, `vim`,
`tcpdump`, ...) without modifying the target.


## How it works

1. Mounts the `nixos/nix` image to get a `/nix` store with tools.
2. Joins the target container's namespaces (mount, PID, network, IPC, UTS).
3. Creates a tmpfs-backed overlay on `/` so changes are discarded by default.
4. Bind-mounts `/nix` from the toolbox image into the overlay.
5. Chroots into the merged view and starts a shell.

The container sees no changes. The `/nix` directory only exists inside the
debug session's overlay and is never visible to the actual container.

### Three modes

| Mode | Target | What happens |
|------|--------|-------------|
| **Live** | Running or paused container | Joins container namespaces; you see its processes, network, and filesystem. |
| **Snapshot** | Stopped / exited / created container | Mounts the container's filesystem read-only; overlays on top. |
| **Image** | Any OCI image | Mounts the image layers; overlays on top. No container needed. |

The mode is selected automatically based on what you pass:

```
podman-debug my-running-container    # live
podman-debug my-stopped-container    # snapshot
podman-debug nginx:latest            # image
```

### Writable mode

By default all changes are discarded when you exit.  Pass `--writable` (`-w`)
to write through to a running container's real filesystem:

```
podman-debug -w my-container
```

In writable mode a `/nix` directory is temporarily created on the container's
real filesystem and removed on exit.  This is the only trace left behind.

Writable mode is only supported for running containers.  It will fail (by
design) on read-only containers.

## Requirements

- **Linux** (x86_64 or aarch64)
- **Podman** installed and working (rootful or rootless)
- **Kernel 5.2+** (for `open_tree()` / `move_mount()` syscalls)
- The `nixos/nix:latest` image (pulled automatically on first use)

## Installation

### From source

```
git clone https://github.com/rsturla/podman-debug.git
cd podman-debug
make build
make install PREFIX=$HOME/.local   # installs to ~/.local/bin/
```

The build runs inside a Podman container (`golang:1.26`), so you don't need
Go installed locally -- just Podman.  The output is a statically linked binary
(`CGO_ENABLED=0`) with no external library dependencies (~4.5 MB).

You can override the container engine or Go version if needed:

```
make build CONTAINER_ENGINE=docker GO_VERSION=1.26
```

## Usage

```
podman-debug [options] {CONTAINER|IMAGE} [COMMAND [ARG...]]
```

### Examples

```bash
# Interactive debug shell into a running container
podman-debug my-container

# Run a one-off command
podman-debug -c "cat /etc/os-release" my-container

# Debug a distroless image directly
podman-debug cgr.dev/chainguard/python:latest

# Use a specific shell
podman-debug --shell sh my-container

# Write changes through to the container
podman-debug -w my-container

# Use a custom toolbox image
podman-debug --image my-toolbox:v1 my-container
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--shell` | | `auto` | Shell to use: `bash`, `sh`, `auto` |
| `--command` | `-c` | | Execute a command instead of interactive shell |
| `--image` | | `nixos/nix:latest` | Debug toolbox image |
| `--pull` | | `missing` | Pull policy: `always`, `missing`, `never` |
| `--interactive` | `-i` | `true` | Keep STDIN open |
| `--tty` | `-t` | `true` | Allocate a pseudo-TTY |
| `--writable` | `-w` | `false` | Write changes through to the container |

## Builtin commands

Inside every debug session, the following commands are available on `PATH`:

### `install <package> [package...]`

Install packages from [nixpkgs](https://search.nixos.org/packages) into the
debug session.  Packages are session-only and discarded on exit.

```bash
install curl
install nmap strace tcpdump
```

### `uninstall <package> [package...]`

Remove a previously installed package from the session.

```bash
uninstall curl
```

### `entrypoint`

Inspect the ENTRYPOINT and CMD of the container or image being debugged.

```bash
entrypoint            # Show details + lint
entrypoint --print    # Print effective command only
entrypoint --lint     # Lint the entrypoint configuration
entrypoint --run      # Execute the entrypoint
entrypoint --json     # Print raw JSON metadata
```

### `builtins`

List all available builtin commands.

## Rootless support

Rootless Podman is fully supported.  The binary automatically re-execs itself
through `podman unshare` to enter Podman's user namespace, which provides the
capabilities needed for overlay mounts, chroot, and namespace joins without
real root privileges.

## Limitations

- **Linux only.** The implementation uses Linux-specific syscalls (`setns`,
  `unshare`, `mount`, `chroot`, `open_tree`, `move_mount`).
- **No remote Podman.** The binary shells out to the local `podman` CLI and
  accesses `/proc/<pid>/ns/*` directly.  It does not work with `podman --remote`
  or Podman machine VMs on macOS/Windows.
- **Writable mode + read-only containers.** Writable mode requires the
  container's root filesystem to be writable.  This is by design.
- **`/nix` conflicts.** If the target container already has a `/nix` directory
  the overlay will shadow it during the debug session.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
