// Package podman provides a client that shells out to the podman CLI
// for container and image operations (inspect, mount, pull, etc.).
package podman

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultDebugImage is the default nix toolbox image.
const DefaultDebugImage = "docker.io/nixos/nix:latest"

// ContainerInfo holds the subset of container metadata needed for
// debug sessions.
type ContainerInfo struct {
	ID    string
	State string // "running", "paused", "stopped", "exited", "created", "configured"
	PID   int    // Only valid when running/paused
}

// inspectResult is the subset of podman inspect JSON we care about.
type inspectResult struct {
	ID    string `json:"Id"`
	State struct {
		Status string `json:"Status"`
		PID    int    `json:"Pid"`
	} `json:"State"`
}

// InspectContainer shells out to `podman container inspect` and
// returns the container's ID, state, and PID.  Using "container
// inspect" (not bare "inspect") ensures we only match containers,
// so image references correctly fall through to image mode.
func InspectContainer(nameOrID string) (*ContainerInfo, error) {
	out, err := exec.Command("podman", "container", "inspect", "--format", "json", nameOrID).Output()
	if err != nil {
		return nil, fmt.Errorf("inspecting container %s: %w", nameOrID, err)
	}

	var results []inspectResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing inspect output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no inspect data for %s", nameOrID)
	}

	return &ContainerInfo{
		ID:    results[0].ID,
		State: results[0].State.Status,
		PID:   results[0].State.PID,
	}, nil
}

// MountContainer shells out to `podman mount` and returns the
// host-side root filesystem path.
func MountContainer(nameOrID string) (string, error) {
	out, err := exec.Command("podman", "mount", nameOrID).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("mounting container %s: %s", nameOrID, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("mounting container %s: %w", nameOrID, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// UnmountContainer shells out to `podman unmount`.
func UnmountContainer(nameOrID string) error {
	return exec.Command("podman", "unmount", nameOrID).Run()
}

// PullImage shells out to `podman pull` according to the given policy.
func PullImage(image, pullPolicy string) error {
	switch pullPolicy {
	case "always":
		return exec.Command("podman", "pull", image).Run()
	case "never":
		if err := exec.Command("podman", "image", "exists", image).Run(); err != nil {
			return fmt.Errorf("image %s not found and pull policy is 'never'", image)
		}
		return nil
	default: // "missing"
		if err := exec.Command("podman", "image", "exists", image).Run(); err != nil {
			return exec.Command("podman", "pull", image).Run()
		}
		return nil
	}
}

// MountImage shells out to `podman image mount` and returns the
// host-side path to the image's root filesystem.
func MountImage(image string) (string, error) {
	out, err := exec.Command("podman", "image", "mount", image).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("mounting image %s: %s", image, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("mounting image %s: %w", image, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// UnmountImage shells out to `podman image unmount`.
func UnmountImage(image string) error {
	return exec.Command("podman", "image", "unmount", image).Run()
}

// EntrypointInfo holds the ENTRYPOINT, CMD, and WorkingDir metadata
// from a container or image configuration.
type EntrypointInfo struct {
	Entrypoint []string `json:"entrypoint"`
	Cmd        []string `json:"cmd"`
	WorkingDir string   `json:"working_dir"`
}

// containerConfigResult is the subset of podman container inspect
// JSON needed for entrypoint metadata.
type containerConfigResult struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"Config"`
}

// imageConfigResult is the subset of podman image inspect JSON
// needed for entrypoint metadata.
type imageConfigResult struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"Config"`
}

// InspectContainerEntrypoint returns the entrypoint/cmd metadata for
// a container.
func InspectContainerEntrypoint(nameOrID string) (*EntrypointInfo, error) {
	out, err := exec.Command("podman", "container", "inspect", "--format", "json", nameOrID).Output()
	if err != nil {
		return nil, fmt.Errorf("inspecting container %s: %w", nameOrID, err)
	}

	var results []containerConfigResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing container inspect output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no inspect data for %s", nameOrID)
	}

	return &EntrypointInfo{
		Entrypoint: results[0].Config.Entrypoint,
		Cmd:        results[0].Config.Cmd,
		WorkingDir: results[0].Config.WorkingDir,
	}, nil
}

// InspectImageEntrypoint returns the entrypoint/cmd metadata for
// an image.
func InspectImageEntrypoint(image string) (*EntrypointInfo, error) {
	out, err := exec.Command("podman", "image", "inspect", "--format", "json", image).Output()
	if err != nil {
		return nil, fmt.Errorf("inspecting image %s: %w", image, err)
	}

	var results []imageConfigResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing image inspect output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no inspect data for %s", image)
	}

	return &EntrypointInfo{
		Entrypoint: results[0].Config.Entrypoint,
		Cmd:        results[0].Config.Cmd,
		WorkingDir: results[0].Config.WorkingDir,
	}, nil
}

// NamespacePath returns /proc/<pid>/ns/<nstype> for the given PID.
func NamespacePath(pid int, nstype string) string {
	return fmt.Sprintf("/proc/%d/ns/%s", pid, nstype)
}
