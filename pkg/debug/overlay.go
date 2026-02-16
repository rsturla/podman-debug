//go:build linux

package debug

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const overlayBasePath = "/tmp/.podman-debug-overlay"

// createOverlay sets up a tmpfs-backed overlay on top of lowerDir.
// If writable is true, the overlay is replaced with a recursive bind
// mount of lowerDir (write-through).  Returns the merged directory path.
func createOverlay(lowerDir string, writable bool) (string, error) {
	if err := os.MkdirAll(overlayBasePath, 0755); err != nil {
		return "", fmt.Errorf("creating overlay base: %w", err)
	}
	if err := unix.Mount("tmpfs", overlayBasePath, "tmpfs", 0, "size=1G"); err != nil {
		return "", fmt.Errorf("mounting tmpfs: %w", err)
	}

	upperDir := overlayBasePath + "/upper"
	workDir := overlayBasePath + "/work"
	mergedDir := overlayBasePath + "/merged"
	for _, d := range []string{upperDir, workDir, mergedDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return "", fmt.Errorf("creating %s: %w", d, err)
		}
	}

	overlayOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upperDir, workDir)
	if err := unix.Mount("overlay", mergedDir, "overlay", 0, overlayOpts); err != nil {
		return "", fmt.Errorf("mounting overlay: %w", err)
	}

	if writable {
		if err := unix.Mount(lowerDir, mergedDir, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return "", fmt.Errorf("rebinding root into overlay: %w", err)
		}
	}

	return mergedDir, nil
}

// mountNixStore moves the cloned nix tree FD into a temporary mount
// point, then sets up a writable overlay on top so nix operations
// (profile installs, etc.) work inside the debug session.
func mountNixStore(nixTreeFD int, nixMountPoint, base string) error {
	nixTmpMount := base + "/nix-lower"
	if err := os.MkdirAll(nixTmpMount, 0755); err != nil {
		return fmt.Errorf("creating nix temp mount: %w", err)
	}
	if err := unix.MoveMount(nixTreeFD, "", unix.AT_FDCWD, nixTmpMount,
		unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("move_mount nix to temp: %w", err)
	}

	nixUpperDir := base + "/nix-upper"
	nixWorkDir := base + "/nix-work"
	for _, d := range []string{nixUpperDir, nixWorkDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}

	nixOverlayOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", nixTmpMount, nixUpperDir, nixWorkDir)
	if err := unix.Mount("overlay", nixMountPoint, "overlay", 0, nixOverlayOpts); err != nil {
		return fmt.Errorf("mounting nix overlay: %w", err)
	}
	return nil
}

// bindHostMounts bind-mounts /proc, /sys, /dev and network config
// files from the host (or container, depending on which mount namespace
// we are in) into the merged overlay directory.
//
// In live mode we are inside the container's mount namespace, so the
// bind-mounted /proc already reflects the container's PID namespace.
func bindHostMounts(mergedDir string) {
	for _, mp := range []string{"/proc", "/sys", "/dev"} {
		target := mergedDir + mp
		if _, err := os.Stat(mp); err != nil {
			continue
		}
		if err := os.MkdirAll(target, 0755); err != nil {
			continue
		}
		_ = unix.Mount(mp, target, "", unix.MS_BIND|unix.MS_REC, "")
	}

	bindNetworkConfig(mergedDir)
}

// bindSnapshotMounts sets up /sys, /dev, and network config in the
// overlay for snapshot/image mode.  /proc is NOT mounted here because
// snapshot mode uses CLONE_NEWPID on the shell process and mounts a
// fresh /proc from within the new PID namespace so that only the
// debug session's own processes are visible.
func bindSnapshotMounts(mergedDir string) {
	// Create an empty /proc mountpoint — the shell wrapper will mount
	// a fresh procfs from within the new PID namespace.
	_ = os.MkdirAll(mergedDir+"/proc", 0755)

	for _, mp := range []string{"/sys", "/dev"} {
		target := mergedDir + mp
		if _, err := os.Stat(mp); err != nil {
			continue
		}
		if err := os.MkdirAll(target, 0755); err != nil {
			continue
		}
		_ = unix.Mount(mp, target, "", unix.MS_BIND|unix.MS_REC, "")
	}

	bindNetworkConfig(mergedDir)
}

// bindNetworkConfig bind-mounts /etc/resolv.conf, /etc/hosts, and
// /etc/hostname into the overlay so DNS resolution works.
func bindNetworkConfig(mergedDir string) {
	for _, configFile := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"} {
		info, err := os.Stat(configFile)
		if err != nil || info.Size() == 0 {
			continue
		}
		target := mergedDir + configFile
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			continue
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			f, err := os.Create(target)
			if err != nil {
				continue
			}
			f.Close()
		}
		_ = unix.Mount(configFile, target, "", unix.MS_BIND, "")
	}
}
