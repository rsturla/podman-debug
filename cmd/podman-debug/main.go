package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rsturla/podman-debug/pkg/debug"
	"github.com/rsturla/podman-debug/pkg/podman"
	"github.com/spf13/cobra"
	xterm "golang.org/x/term"
)

var (
	flagShell       string
	flagCommand     string
	flagImage       string
	flagPull        string
	flagInteractive bool
	flagTTY         bool
	flagWritable    bool
)

func main() {
	// Init-proc mode: when invoked as "podman-debug --init-proc <shell> [args...]",
	// mount a fresh /proc and exec the shell.  Used by snapshot/image mode to
	// provide an isolated PID namespace — this process runs as PID 1 inside
	// a CLONE_NEWPID child, so the fresh /proc only shows debug session processes.
	if len(os.Args) >= 3 && os.Args[1] == "--init-proc" {
		initProc(os.Args[2], os.Args[3:])
		return
	}

	// Rootless re-exec: when not running as root (uid 0), we need to
	// be inside podman's user namespace so that podman image/container
	// mount operations work and we have CAP_SYS_ADMIN for overlays,
	// chroot, and namespace joins.
	if os.Getuid() != 0 && os.Getenv("_PODMAN_DEBUG_UNSHARED") == "" {
		reexecViaPodmanUnshare()
		return
	}

	rootCmd := &cobra.Command{
		Use:   "podman-debug [options] {CONTAINER|IMAGE} [COMMAND [ARG...]]",
		Short: "Get a shell into any container or image",
		Long: `Get a debug shell into any container or image, even if it has no shell.

Uses a toolbox image with Nix to provide debugging tools without modifying the target.
The /nix directory is never visible to the actual container or image.

By default, all filesystem changes are discarded when leaving the shell.
Use --writable to make changes visible to a running or paused container.`,
		Args:                  cobra.MinimumNArgs(1),
		RunE:                  debugRun,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Example: `  podman-debug my-container
  podman-debug --writable my-container
  podman-debug --shell sh my-container
  podman-debug -c "cat /etc/os-release" my-container
  podman-debug --image my-toolbox:v1 my-container
  podman-debug nginx:latest
  podman-debug my-stopped-container`,
	}

	flags := rootCmd.Flags()
	flags.SetInterspersed(false)

	flags.StringVar(&flagShell, "shell", "auto", "Shell to use: bash, sh, auto")
	flags.StringVarP(&flagCommand, "command", "c", "", "Execute command instead of interactive shell")
	flags.StringVar(&flagImage, "image", podman.DefaultDebugImage, "Debug toolbox image")
	flags.StringVar(&flagPull, "pull", "missing", `Pull policy: "always", "missing", "never"`)
	flags.BoolVarP(&flagInteractive, "interactive", "i", true, "Keep STDIN open")
	flags.BoolVarP(&flagTTY, "tty", "t", true, "Allocate a pseudo-TTY")
	flags.BoolVarP(&flagWritable, "writable", "w", false, "Make filesystem changes visible to the container")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(125)
	}
}

func debugRun(cmd *cobra.Command, args []string) error {
	nameOrID := args[0]

	// Handle positional command arguments.
	if len(args) > 1 && flagCommand == "" {
		cmdArgs := args[1:]
		if len(cmdArgs) >= 2 && cmdArgs[0] == "-c" {
			cmdArgs = cmdArgs[1:]
		}
		flagCommand = strings.Join(cmdArgs, " ")
	}

	// Pull and mount the nix debug image.
	debugImage := flagImage
	if err := podman.PullImage(debugImage, flagPull); err != nil {
		return fmt.Errorf("pulling debug image: %w", err)
	}

	nixMountPoint, err := podman.MountImage(debugImage)
	if err != nil {
		return fmt.Errorf("mounting debug image: %w", err)
	}
	defer podman.UnmountImage(debugImage)

	nixPath := filepath.Join(nixMountPoint, "nix")
	if _, err := os.Stat(nixPath); err != nil {
		return fmt.Errorf("nix store not found in debug image at %s: %w", nixPath, err)
	}

	shell := debug.DetectShell(flagShell)
	var shellArgs []string
	if flagCommand != "" {
		shellArgs = []string{"-c", flagCommand}
	}

	streams := resolveStreams()

	// Try as a container first, fall back to image.
	exitCode, err := tryContainerDebug(nameOrID, nixPath, shell, shellArgs, streams)
	if err == nil {
		os.Exit(exitCode)
	}

	if !isNotFound(err) {
		return err
	}

	exitCode, err = tryImageDebug(nameOrID, nixPath, shell, shellArgs, streams)
	if err != nil {
		return fmt.Errorf("no container or image found for %q: %w", nameOrID, err)
	}

	os.Exit(exitCode)
	return nil
}

func tryContainerDebug(nameOrID, nixPath, shell string, shellArgs []string, streams debug.Streams) (int, error) {
	ctr, err := podman.InspectContainer(nameOrID)
	if err != nil {
		return 0, err
	}

	// Resolve entrypoint metadata (best-effort, non-fatal).
	ep, _ := podman.InspectContainerEntrypoint(nameOrID)

	restoreTerminal := setupTerminal()
	defer restoreTerminal()

	switch ctr.State {
	case "running":
		return runLiveDebug(ctr.PID, nixPath, shell, shellArgs, streams, ep)
	case "paused":
		fmt.Fprintln(os.Stderr, "Note: Container is paused. Processes are frozen but filesystem is accessible.")
		return runLiveDebug(ctr.PID, nixPath, shell, shellArgs, streams, ep)
	case "stopped", "exited", "created", "configured":
		fmt.Fprintln(os.Stderr, "Note: Container is not running. Changes will be discarded on exit.")
		return runSnapshotDebug(nameOrID, nixPath, shell, shellArgs, streams, ep)
	default:
		return 0, fmt.Errorf("container %s is in unsupported state: %s", nameOrID, ctr.State)
	}
}

func tryImageDebug(nameOrID, nixPath, shell string, shellArgs []string, streams debug.Streams) (int, error) {
	fmt.Fprintln(os.Stderr, "Note: Debugging an image. Changes will be discarded on exit.")

	if err := podman.PullImage(nameOrID, "missing"); err != nil {
		return 0, fmt.Errorf("pulling image %s: %w", nameOrID, err)
	}

	// Resolve entrypoint metadata (best-effort, non-fatal).
	ep, _ := podman.InspectImageEntrypoint(nameOrID)

	mountPoint, err := podman.MountImage(nameOrID)
	if err != nil {
		return 0, fmt.Errorf("mounting image %s: %w", nameOrID, err)
	}
	defer podman.UnmountImage(nameOrID)

	restoreTerminal := setupTerminal()
	defer restoreTerminal()

	opts := &debug.Options{
		Mode:           debug.ModeImage,
		HostMountpoint: mountPoint,
		Entrypoint:     ep,
	}

	return debug.ExecSnapshot(nixPath, mountPoint, shell, shellArgs, streams, opts)
}

func runLiveDebug(pid int, nixPath, shell string, shellArgs []string, streams debug.Streams, ep *podman.EntrypointInfo) (int, error) {
	opts := &debug.Options{
		Mode:       debug.ModeLive,
		Writable:   flagWritable,
		Entrypoint: ep,
	}
	return debug.ExecLive(pid, nixPath, shell, shellArgs, streams, opts)
}

func runSnapshotDebug(nameOrID, nixPath, shell string, shellArgs []string, streams debug.Streams, ep *podman.EntrypointInfo) (int, error) {
	mountPoint, err := podman.MountContainer(nameOrID)
	if err != nil {
		return 0, err
	}
	defer podman.UnmountContainer(nameOrID)

	opts := &debug.Options{
		Mode:           debug.ModeSnapshot,
		HostMountpoint: mountPoint,
		Entrypoint:     ep,
	}

	return debug.ExecSnapshot(nixPath, mountPoint, shell, shellArgs, streams, opts)
}

func setupTerminal() func() {
	// Only enter raw mode for interactive sessions (no -c command).
	// Raw mode disables output processing (\n -> \r\n translation),
	// which corrupts output from non-interactive commands.
	if flagTTY && flagInteractive && flagCommand == "" {
		if xterm.IsTerminal(int(os.Stdin.Fd())) {
			oldState, err := xterm.MakeRaw(int(os.Stdin.Fd()))
			if err == nil {
				return func() {
					_ = xterm.Restore(int(os.Stdin.Fd()), oldState)
				}
			}
		}
	}
	return func() {}
}

func resolveStreams() debug.Streams {
	s := debug.Streams{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if flagInteractive {
		s.Stdin = os.Stdin
	}
	return s
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no container with name or ID") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "inspecting container")
}
