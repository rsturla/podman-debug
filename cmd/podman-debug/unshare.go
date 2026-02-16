package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// reexecViaPodmanUnshare re-execs the current binary under "podman unshare"
// so that we run inside podman's user namespace with full subordinate
// UID/GID mappings and CAP_SYS_ADMIN.  This makes podman mount, overlay,
// chroot, and namespace operations work in rootless mode.
func reexecViaPodmanUnshare() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine own executable path: %v\n", err)
		os.Exit(125)
	}

	// Build: podman unshare -- <self> <original args...>
	// The "--" prevents podman unshare from parsing our flags.
	// argv[0] must be the program name for the exec'd binary.
	args := append([]string{"podman", "unshare", "--", self}, os.Args[1:]...)

	podmanBin, err := exec.LookPath("podman")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: podman not found in PATH: %v\n", err)
		os.Exit(125)
	}

	env := append(os.Environ(), "_PODMAN_DEBUG_UNSHARED=1")

	// Use exec (replaces the process) to preserve TTY, signals, exit code.
	if err := syscall.Exec(podmanBin, args, env); err != nil {
		fmt.Fprintf(os.Stderr, "Error: exec podman unshare: %v\n", err)
		os.Exit(125)
	}
}

// initProc is the --init-proc handler.  It runs as PID 1 inside a new
// PID namespace (created by CLONE_NEWPID in the parent).  It mounts a
// fresh /proc so that ps/top only show processes in this namespace,
// then execs the shell.
func initProc(shell string, args []string) {
	// Mount a fresh /proc for the new PID namespace.
	_ = unix.Mount("proc", "/proc", "proc", 0, "")

	// Exec the shell (replaces this process).
	argv := append([]string{shell}, args...)
	if err := syscall.Exec(shell, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: exec %s: %v\n", shell, err)
		os.Exit(125)
	}
}
