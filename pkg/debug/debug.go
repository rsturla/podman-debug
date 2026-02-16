// Package debug implements the core debug session logic: namespace
// joining, overlay filesystem setup, and shell execution.
package debug

import (
	"os"

	"github.com/rsturla/podman-debug/pkg/podman"
)

// Mode indicates the filesystem strategy for the debug session.
type Mode int

const (
	ModeLive     Mode = iota // running/paused containers
	ModeSnapshot             // stopped containers
	ModeImage                // bare images
)

// Options configures a debug session.
type Options struct {
	Mode           Mode
	HostMountpoint string // for snapshot/image modes
	Writable       bool
	Entrypoint     *podman.EntrypointInfo // image/container entrypoint metadata
}

// result holds the outcome of a debug session goroutine.
type result struct {
	exitCode int
	err      error
}

// Streams bundles the I/O file descriptors for a debug session.
type Streams struct {
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File
}
