//go:build linux

package debug

import (
	"os"
	"path/filepath"
)

// writeNixConfig writes a single-user nix.conf into the merged
// filesystem so nix commands work without a daemon.
func writeNixConfig(mergedDir string) {
	nixConfigDir := mergedDir + "/etc/nix"
	if err := os.MkdirAll(nixConfigDir, 0755); err == nil {
		nixConfig := []byte(`# Podman debug single-user mode config
build-users-group =
sandbox = false
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
experimental-features = nix-command flakes
`)
		_ = os.WriteFile(nixConfigDir+"/nix.conf", nixConfig, 0644)
	}
}

// setupEnvironment configures PATH, HOME, TERM, SSL certs, and other
// environment variables for the debug shell.
func setupEnvironment(shell string) {
	os.Setenv("HOME", "/root")

	nixProfilePath := filepath.Join("/nix", "var", "nix", "profiles", "default")
	nixBinPath := filepath.Join(nixProfilePath, "bin")
	userProfileBin := "/root/.nix-profile/bin"
	containerPath := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	os.Setenv("PATH", builtinsDir+":"+userProfileBin+":"+nixBinPath+":"+containerPath)

	if os.Getenv("TERM") == "" {
		os.Setenv("TERM", "xterm-256color")
	}

	// Set NIX_SSL_CERT_FILE so nix-built tools (curl, wget, git, etc.)
	// can verify TLS connections.  We use the nix-specific variable
	// rather than SSL_CERT_FILE to avoid influencing the container's
	// own tools which may have their own CA cert configuration.
	if os.Getenv("NIX_SSL_CERT_FILE") == "" {
		nixCACert := filepath.Join(nixProfilePath, "etc", "ssl", "certs", "ca-bundle.crt")
		if _, err := os.Stat(nixCACert); err == nil {
			os.Setenv("NIX_SSL_CERT_FILE", nixCACert)
		}
	}

	os.Setenv("SHELL", shell)
	os.Setenv("PS1", "debug> ")
}
