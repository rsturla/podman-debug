//go:build linux

package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rsturla/podman-debug/pkg/podman"
)

const builtinsDir = "/.podman-debug/bin"
const metadataDir = "/.podman-debug"

// writeBuiltins injects helper scripts into the merged overlay so
// they are available on PATH inside the debug shell.
func writeBuiltins(mergedDir string, ep *podman.EntrypointInfo) {
	binDir := mergedDir + builtinsDir
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return
	}

	writeScript(binDir, "install", installScript)
	writeScript(binDir, "uninstall", uninstallScript)
	writeScript(binDir, "builtins", builtinsScript)
	writeScript(binDir, "entrypoint", entrypointScript)

	// Copy our own binary into the overlay as "init" for --init-proc
	// PID namespace support in snapshot/image mode.
	copyBinary(binDir, "init")

	if ep != nil {
		writeEntrypointMetadata(mergedDir, ep)
	}
}

// copyBinary copies the current executable into the overlay directory.
func copyBinary(dir, name string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	src, err := os.Open(self)
	if err != nil {
		return
	}
	defer src.Close()

	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return
	}
	defer dst.Close()

	_, _ = io.Copy(dst, src)
}

func writeScript(dir, name, content string) {
	_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0755)
}

func writeEntrypointMetadata(mergedDir string, ep *podman.EntrypointInfo) {
	metaDir := mergedDir + metadataDir
	_ = os.MkdirAll(metaDir, 0755)

	// Write JSON for --json mode.
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(metaDir, "entrypoint.json"), data, 0644)

	// Write individual plain-text files so the shell script can read
	// them with cat — no JSON parsing required (python3/jq not available).
	if len(ep.Entrypoint) > 0 {
		_ = os.WriteFile(filepath.Join(metaDir, "ep_bin"), []byte(strings.Join(ep.Entrypoint, " ")), 0644)
	}
	if len(ep.Cmd) > 0 {
		_ = os.WriteFile(filepath.Join(metaDir, "ep_cmd"), []byte(strings.Join(ep.Cmd, " ")), 0644)
	}
	if ep.WorkingDir != "" {
		_ = os.WriteFile(filepath.Join(metaDir, "ep_workdir"), []byte(ep.WorkingDir), 0644)
	}
	var effective []string
	effective = append(effective, ep.Entrypoint...)
	effective = append(effective, ep.Cmd...)
	if len(effective) > 0 {
		_ = os.WriteFile(filepath.Join(metaDir, "ep_effective"), []byte(strings.Join(effective, " ")), 0644)
	}

	// Write a human-readable summary for the entrypoint script.
	var summary strings.Builder

	if len(ep.Entrypoint) > 0 {
		summary.WriteString(fmt.Sprintf("ENTRYPOINT %s\n", formatArgs(ep.Entrypoint)))
	} else {
		summary.WriteString("ENTRYPOINT (not set)\n")
	}

	if len(ep.Cmd) > 0 {
		summary.WriteString(fmt.Sprintf("CMD %s\n", formatArgs(ep.Cmd)))
	} else {
		summary.WriteString("CMD (not set)\n")
	}

	if ep.WorkingDir != "" {
		summary.WriteString(fmt.Sprintf("WORKDIR %s\n", ep.WorkingDir))
	}

	if len(effective) > 0 {
		summary.WriteString(fmt.Sprintf("\nEffective command:\n  %s\n", strings.Join(effective, " ")))
	} else {
		summary.WriteString("\nEffective command: (none)\n")
	}

	_ = os.WriteFile(filepath.Join(metaDir, "entrypoint.txt"), []byte(summary.String()), 0644)
}

func formatArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'\\") {
			quoted[i] = fmt.Sprintf("%q", a)
		} else {
			quoted[i] = a
		}
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

const installScript = `#!/nix/var/nix/profiles/default/bin/sh
set -e

if [ $# -eq 0 ]; then
    echo "Usage: install <package> [package...]"
    echo ""
    echo "Install packages from nixpkgs into the debug session."
    echo "Browse available packages at: https://search.nixos.org/packages"
    echo ""
    echo "Examples:"
    echo "  install curl"
    echo "  install nmap strace tcpdump"
    echo ""
    echo "Note: installed packages only persist for this debug session."
    exit 1
fi

for pkg in "$@"; do
    echo "Installing $pkg..."
    nix-env -iA "nixpkgs.$pkg"
done
`

const uninstallScript = `#!/nix/var/nix/profiles/default/bin/sh
set -e

if [ $# -eq 0 ]; then
    echo "Usage: uninstall <package> [package...]"
    echo ""
    echo "Uninstall packages from the debug session."
    echo ""
    echo "Examples:"
    echo "  uninstall curl"
    echo "  uninstall nmap strace tcpdump"
    exit 1
fi

for pkg in "$@"; do
    echo "Uninstalling $pkg..."
    nix-env -e "$pkg"
done
`

const builtinsScript = `#!/nix/var/nix/profiles/default/bin/sh
echo "podman-debug builtin commands:"
echo ""
echo "  install <pkg> [pkg...]   Install nix packages (https://search.nixos.org/packages)"
echo "  uninstall <pkg> [pkg...] Uninstall nix packages"
echo "  entrypoint               Show, lint, or run the container/image entrypoint"
echo "  builtins                 Show this help"
`

const entrypointScript = `#!/nix/var/nix/profiles/default/bin/sh
META_DIR="/.podman-debug"
EP_JSON="$META_DIR/entrypoint.json"
EP_TEXT="$META_DIR/entrypoint.txt"

usage() {
    echo "Usage: entrypoint [--print|--lint|--run|--json]"
    echo ""
    echo "Inspect the ENTRYPOINT and CMD of the container or image."
    echo ""
    echo "Options:"
    echo "  (no args)   Show entrypoint details and lint results"
    echo "  --print     Print only the effective command"
    echo "  --lint      Lint the entrypoint configuration"
    echo "  --run       Execute the entrypoint"
    echo "  --json      Print raw JSON metadata"
}

if [ ! -f "$EP_JSON" ]; then
    echo "Error: no entrypoint metadata found."
    echo "This can happen if the container or image has no entrypoint configured."
    exit 1
fi

# Read pre-rendered plain-text files written by podman-debug (no JSON parsing needed).
ENTRYPOINT=""
CMD=""
WORKDIR=""
EFFECTIVE=""
[ -f "$META_DIR/ep_bin" ] && ENTRYPOINT=$(cat "$META_DIR/ep_bin")
[ -f "$META_DIR/ep_cmd" ] && CMD=$(cat "$META_DIR/ep_cmd")
[ -f "$META_DIR/ep_workdir" ] && WORKDIR=$(cat "$META_DIR/ep_workdir")
[ -f "$META_DIR/ep_effective" ] && EFFECTIVE=$(cat "$META_DIR/ep_effective")

do_lint() {
    echo "Lint results:"
    PASS=true

    if [ -n "$ENTRYPOINT" ]; then
        EP_BIN="${ENTRYPOINT%% *}"
        if [ -x "$EP_BIN" ] || command -v "$EP_BIN" >/dev/null 2>&1; then
            echo "  PASS: '$EP_BIN' found"
        else
            echo "  WARN: '$EP_BIN' not found in PATH or filesystem"
            PASS=false
        fi
    else
        echo "  INFO: no ENTRYPOINT set (using CMD only)"
    fi

    if [ -z "$ENTRYPOINT" ] && [ -z "$CMD" ]; then
        echo "  WARN: neither ENTRYPOINT nor CMD is set"
        PASS=false
    fi

    if [ "$PASS" = true ]; then
        echo ""
        echo "No issues found."
    fi
}

case "${1:-}" in
    --print)
        if [ -z "$EFFECTIVE" ]; then
            echo "(no entrypoint or cmd configured)"
            exit 1
        fi
        echo "$EFFECTIVE"
        ;;
    --json)
        cat "$EP_JSON"
        ;;
    --run)
        if [ -z "$EFFECTIVE" ]; then
            echo "Error: no entrypoint or cmd to run."
            exit 1
        fi
        echo "Running: $EFFECTIVE"
        echo "---"
        if [ -n "$WORKDIR" ] && [ -d "$WORKDIR" ]; then
            cd "$WORKDIR"
        fi
        exec $EFFECTIVE
        ;;
    --lint)
        do_lint
        ;;
    --help|-h)
        usage
        ;;
    "")
        # Default: show details + lint.
        echo "Entrypoint and CMD configuration:"
        echo ""
        if [ -f "$EP_TEXT" ]; then
            cat "$EP_TEXT"
        fi
        echo ""
        do_lint
        ;;
    *)
        echo "Error: unknown option '$1'"
        echo ""
        usage
        exit 1
        ;;
esac
`
