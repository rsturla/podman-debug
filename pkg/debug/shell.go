//go:build linux

package debug

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// DetectShell determines which shell binary to use based on user
// preference.  "auto" defaults to bash from the nix profile.
func DetectShell(preference string) string {
	const nixBinPath = "/nix/var/nix/profiles/default/bin"

	if preference != "" && preference != "auto" {
		if filepath.IsAbs(preference) {
			return preference
		}
		return filepath.Join(nixBinPath, preference)
	}

	return filepath.Join(nixBinPath, "bash")
}

func runShell(cmd *exec.Cmd, streams Streams, interactive bool, ptyChan chan<- *os.File, doneChan chan struct{}) (int, error) {
	var exitCode int

	isInteractive := streams.Stdin != nil && interactive

	if isInteractive {
		ptmx, err := pty.Start(cmd)
		if err != nil {
			close(doneChan)
			return 125, err
		}
		defer ptmx.Close()

		if size, err := pty.GetsizeFull(streams.Stdin); err == nil {
			_ = pty.Setsize(ptmx, size)
		}

		ptyChan <- ptmx

		stdinDone := make(chan struct{})
		stdoutDone := make(chan struct{})

		go func() {
			_, _ = io.Copy(ptmx, streams.Stdin)
			close(stdinDone)
		}()

		go func() {
			_, _ = io.Copy(streams.Stdout, ptmx)
			close(stdoutDone)
		}()

		<-stdoutDone

		close(doneChan)

		err = cmd.Wait()

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				err = nil
			}
		}
	} else {
		cmd.Stdin = streams.Stdin
		cmd.Stdout = streams.Stdout
		cmd.Stderr = streams.Stderr

		err := cmd.Run()
		close(doneChan)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				err = nil
			} else {
				return 125, err
			}
		}
	}

	return exitCode, nil
}

func waitForResult(resChan <-chan result, ptyChan <-chan *os.File, doneChan <-chan struct{}, stdin *os.File) (int, error) {
	sigwinchChan := make(chan os.Signal, 1)
	signal.Notify(sigwinchChan, unix.SIGWINCH)
	defer signal.Stop(sigwinchChan)

	var ptmx *os.File
	select {
	case ptmx = <-ptyChan:
	case res := <-resChan:
		return res.exitCode, res.err
	}

	go func() {
		for {
			select {
			case <-sigwinchChan:
				if ptmx != nil && stdin != nil {
					if size, err := pty.GetsizeFull(stdin); err == nil {
						_ = pty.Setsize(ptmx, size)
					}
				}
			case <-doneChan:
				return
			}
		}
	}()

	res := <-resChan
	return res.exitCode, res.err
}

// initBinaryPath is where the podman-debug binary is placed inside
// the overlay for use as the --init-proc PID 1 helper.
const initBinaryPath = "/.podman-debug/bin/init"

// wrapWithPIDNS creates an exec.Cmd that runs the shell inside a new
// PID namespace.  The child process is the podman-debug binary invoked
// with --init-proc, which mounts a fresh /proc and then execs the
// actual shell.  This ensures ps/top only show the debug session's
// own processes.
func wrapWithPIDNS(shell string, shellArgs []string) *exec.Cmd {
	// The init binary mounts /proc and execs the shell.
	args := append([]string{initBinaryPath, "--init-proc", shell}, shellArgs...)
	cmd := exec.Command(initBinaryPath, args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
	}
	return cmd
}
