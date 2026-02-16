//go:build linux

package debug

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/rsturla/podman-debug/pkg/podman"
	"golang.org/x/sys/unix"
)

// ExecLive joins a running/paused container's namespaces and executes
// a debug shell.  The container PID is used to locate namespace files.
func ExecLive(pid int, nixPath, shell string, shellArgs []string, streams Streams, opts *Options) (int, error) {
	resChan := make(chan result, 1)
	ptyChan := make(chan *os.File, 1)
	doneChan := make(chan struct{})

	go func() {
		runtime.LockOSThread()

		_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0)
		_ = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)

		// No user namespace setup needed here: when running rootless
		// the binary has already been re-exec'd via "podman unshare",
		// which puts us in podman's user namespace (same one the
		// container uses) with CAP_SYS_ADMIN.

		nixTreeFD, err := unix.OpenTree(unix.AT_FDCWD, nixPath,
			unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE)
		if err != nil {
			resChan <- result{125, fmt.Errorf("open_tree(%s): %w (requires Linux 5.2+)", nixPath, err)}
			return
		}
		defer unix.Close(nixTreeFD)

		mergedDir, err := setupLiveMode(pid, nixTreeFD, opts.Writable)
		if err != nil {
			resChan <- result{125, err}
			return
		}

		writeNixConfig(mergedDir)
		writeBuiltins(mergedDir, opts.Entrypoint)

		if err := unix.Chroot(mergedDir); err != nil {
			resChan <- result{125, fmt.Errorf("chroot to overlay: %w", err)}
			return
		}
		if err := unix.Chdir("/"); err != nil {
			resChan <- result{125, fmt.Errorf("chdir to /: %w", err)}
			return
		}

		setupEnvironment(shell)

		cmd := exec.Command(shell, shellArgs...)
		cmd.Dir = "/"
		cmd.Env = os.Environ()

		exitCode, err := runShell(cmd, streams, len(shellArgs) == 0, ptyChan, doneChan)

		if opts.Writable {
			_ = unix.Unmount("/nix", unix.MNT_DETACH)
			_ = os.Remove("/nix")
		}

		resChan <- result{exitCode, err}
	}()

	return waitForResult(resChan, ptyChan, doneChan, streams.Stdin)
}

func setupLiveMode(pid int, nixTreeFD int, writable bool) (string, error) {
	nsPaths := map[string]int{
		podman.NamespacePath(pid, "mnt"): unix.CLONE_NEWNS,
		podman.NamespacePath(pid, "pid"): unix.CLONE_NEWPID,
		podman.NamespacePath(pid, "net"): unix.CLONE_NEWNET,
		podman.NamespacePath(pid, "ipc"): unix.CLONE_NEWIPC,
		podman.NamespacePath(pid, "uts"): unix.CLONE_NEWUTS,
	}

	mountNSPath := podman.NamespacePath(pid, "mnt")
	mountFD, err := os.Open(mountNSPath)
	if err != nil {
		return "", fmt.Errorf("opening mount namespace %s: %w", mountNSPath, err)
	}
	defer mountFD.Close()

	type nsFD struct {
		fd    *os.File
		clone int
	}
	var optionalNS []nsFD

	for path, clone := range nsPaths {
		if clone == unix.CLONE_NEWNS {
			continue
		}
		fd, err := os.Open(path)
		if err != nil {
			continue
		}
		optionalNS = append(optionalNS, nsFD{fd, clone})
	}
	defer func() {
		for _, ns := range optionalNS {
			ns.fd.Close()
		}
	}()

	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return "", fmt.Errorf("unshare mount namespace: %w", err)
	}

	// Join PID namespace first (affects children).
	for _, ns := range optionalNS {
		if ns.clone == unix.CLONE_NEWPID {
			_ = unix.Setns(int(ns.fd.Fd()), ns.clone)
			break
		}
	}

	if err := unix.Setns(int(mountFD.Fd()), unix.CLONE_NEWNS); err != nil {
		return "", fmt.Errorf("joining mount namespace: %w", err)
	}

	// Unshare again for a private copy.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return "", fmt.Errorf("unshare mount namespace (private copy): %w", err)
	}

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return "", fmt.Errorf("making / private: %w", err)
	}

	// Join remaining namespaces.
	for _, ns := range optionalNS {
		if ns.clone == unix.CLONE_NEWPID {
			continue
		}
		_ = unix.Setns(int(ns.fd.Fd()), ns.clone)
	}

	mergedDir, err := createOverlay("/", writable)
	if err != nil {
		return "", err
	}

	if writable {
		nixMountPoint := mergedDir + "/nix"
		if err := os.MkdirAll(nixMountPoint, 0755); err != nil {
			return "", fmt.Errorf("creating /nix: %w", err)
		}
		if err := mountNixStore(nixTreeFD, nixMountPoint, overlayBasePath); err != nil {
			return "", err
		}
	} else {
		nixMountPoint := mergedDir + "/nix"
		if err := os.MkdirAll(nixMountPoint, 0755); err != nil {
			return "", fmt.Errorf("creating /nix in overlay: %w", err)
		}
		if err := mountNixStore(nixTreeFD, nixMountPoint, overlayBasePath); err != nil {
			return "", err
		}
		bindHostMounts(mergedDir)
	}

	return mergedDir, nil
}
