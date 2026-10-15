package hubstate

import (
	"os"
	"path/filepath"
	"strings"
)

// Filesystem layout for a Capri host's persisted state. Shared so the host
// process, its tray/app supervisors and the shared transport client all agree
// on one location instead of each deriving it (and drifting apart silently).
const (
	// DirName is the per-user state directory name.
	DirName = ".capri-host"
	// FileName is the hand-written / GUI config file name.
	FileName = "config.json"
	// HomeEnv overrides the state directory (tests, portable installs).
	HomeEnv = "CAPRI_HOME"
)

// Dir is the state directory: $CAPRI_HOME or ~/.capri-host.
func Dir() string {
	if v := strings.TrimSpace(os.Getenv(HomeEnv)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return DirName
	}
	return filepath.Join(home, DirName)
}

// Path is the absolute path of config.json.
func Path() string {
	return filepath.Join(Dir(), FileName)
}

// HubStatePath is the absolute path of the pairing credential hub.json. Three
// programs touch it — the host (reconnect without a code), the tray and the
// GUI app (distinguish "already paired" from "needs a code") — so it is
// derived once here.
func HubStatePath() string {
	return filepath.Join(Dir(), "hub.json")
}
