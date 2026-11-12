// Package app holds what every part of opsie shares: the build's version,
// whether it's a development build, and where its files live.
package app

import (
	"io"
	"os"
	"path/filepath"
	"runtime/debug"

	"golang.org/x/mod/semver"

	"github.com/aloglu/opsie/internal/config"
)

// Repo is where opsie is published.
const Repo = "aloglu/opsie"

// Env is everything a command needs to run.
type Env struct {
	Version string
	// Dev is true for builds made from a checkout. They keep their files
	// apart from the installed opsie and never update themselves.
	Dev   bool
	Paths Paths

	Config    config.Config
	ConfigErr error

	Out, Err   io.Writer
	IsTerminal bool
}

// IsRelease reports whether version is a tagged release such as v0.3.1.
func IsRelease(version string) bool {
	return semver.IsValid(version) && semver.Prerelease(version) == "" && semver.Build(version) == ""
}

// ResolveVersion returns the version stamped in at build time, the module
// version for `go install` builds, or "dev".
func ResolveVersion(stamped string) string {
	if stamped != "" {
		return stamped
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// IsDev reports whether a build is a development build.
func IsDev(version, channel string) bool {
	return channel == "dev" || !IsRelease(version) || os.Getenv("OPSIE_DEV") == "1"
}

// Paths are the directories opsie keeps its files in.
type Paths struct {
	Config string
	State  string
	// Service is where the background service keeps its status and
	// history. Development builds run it by hand (make daemon) and keep
	// it in /run, so it never mixes with an installed one.
	Service string
	// Backups is where app backups go unless the settings say otherwise.
	Backups string
}

// NewPaths follows the XDG base directory spec. Development builds use
// "opsie-dev" so they never touch the installed opsie's settings or history.
func NewPaths(dev bool) Paths {
	name := "opsie"
	if dev {
		name = "opsie-dev"
	}
	home, _ := os.UserHomeDir()
	base := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	service := "/var/lib/opsie"
	if dev {
		service = "/run/opsie-dev"
	}
	return Paths{
		Config:  filepath.Join(base("XDG_CONFIG_HOME", ".config"), name),
		State:   filepath.Join(base("XDG_STATE_HOME", ".local/state"), name),
		Service: service,
		Backups: filepath.Join(home, name+"-backups"),
	}
}

// ConfigFile is the settings file.
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }

// BackupKey holds the secret key that opens encrypted backups, when it's
// kept on the server.
func (p Paths) BackupKey() string { return filepath.Join(p.Config, "backup-key.txt") }

// AuditLog records every action opsie takes on the server.
func (p Paths) AuditLog() string { return filepath.Join(p.State, "audit.log") }

// MetaFile remembers small things between runs, like the last update check.
func (p Paths) MetaFile() string { return filepath.Join(p.State, "meta.json") }
