//go:build linux

package debug

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// ExecSnapshot executes a debug shell using a host-side mount point.
// Used for stopped containers and images.
func ExecSnapshot(nixPath, hostMountpoint, shell string, shellArgs []string, streams Streams, opts *Options) (int, error) {
	resChan := make(chan result, 1)
	ptyChan := make(chan *os.File, 1)
	doneChan := make(chan struct{})

	go func() {
		runtime.LockOSThread()

		_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0)
		_ = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)

		// No user namespace setup needed here: when running rootless
		// the binary has already been re-exec'd via "podman unshare",
		// which puts us in podman's user namespace with CAP_SYS_ADMIN.

		nixTreeFD, err := unix.OpenTree(unix.AT_FDCWD, nixPath,
			unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE)
		if err != nil {
			resChan <- result{125, fmt.Errorf("open_tree(%s): %w (requires Linux 5.2+)", nixPath, err)}
			return
		}
		defer unix.Close(nixTreeFD)

		if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
			resChan <- result{125, fmt.Errorf("unshare mount namespace: %w", err)}
			return
		}
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			resChan <- result{125, fmt.Errorf("making / private: %w", err)}
			return
		}

		mergedDir, err := setupSnapshotMode(hostMountpoint, nixTreeFD)
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

		// Run the shell in a new PID namespace so /proc only shows
		// the debug session's own processes, not the host.  The
		// wrapper mounts a fresh /proc from within the new namespace
		// before exec'ing the actual shell.
		cmd := wrapWithPIDNS(shell, shellArgs)
		cmd.Dir = "/"
		cmd.Env = os.Environ()

		exitCode, err := runShell(cmd, streams, len(shellArgs) == 0, ptyChan, doneChan)
		resChan <- result{exitCode, err}
	}()

	return waitForResult(resChan, ptyChan, doneChan, streams.Stdin)
}

func setupSnapshotMode(hostMountpoint string, nixTreeFD int) (string, error) {
	mergedDir, err := createOverlay(hostMountpoint, false)
	if err != nil {
		return "", err
	}

	nixMountPoint := mergedDir + "/nix"
	if err := os.MkdirAll(nixMountPoint, 0755); err != nil {
		return "", fmt.Errorf("creating /nix in overlay: %w", err)
	}

	if err := mountNixStore(nixTreeFD, nixMountPoint, overlayBasePath); err != nil {
		return "", err
	}

	bindSnapshotMounts(mergedDir)

	return mergedDir, nil
}
