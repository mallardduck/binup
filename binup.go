// Package binup lets single-binary CLI tools update themselves.
//
// binup owns the update WORKFLOW — check for a newer version, guard against
// package-manager installs, and (after the consumer prompts the user) atomically
// swap the running executable. It delegates the atomic swap to
// github.com/minio/selfupdate and delegates "where updates come from" to the
// pluggable Source interface.
//
// Scope: binup replaces exactly one executable file — the running binary.
// Multi-file installs are out of scope; use a real installer/package manager.
//
// Typical wiring (consumer owns the CLI and the prompt):
//
//	u, _ := binup.New(src, binup.WithVersionProvider(binup.StaticVersion(version)))
//
//	res, err := u.Check(ctx)
//	if err != nil || !res.UpdateAvailable {
//		return err
//	}
//	if skip := u.CanApply(); skip != nil {
//		fmt.Printf("v%s available — %s\n", res.Latest, skip.Hint)
//		return nil
//	}
//	if !prompt(res.Latest) {
//		return nil
//	}
//	_, err = u.Apply(ctx, res.Release)
//	return err
package binup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Updater is the configured, reusable entry point. Create one with New.
type Updater struct {
	source          Source
	comparator      Comparator
	versionProvider VersionProvider

	// installModeEnv overrides the env var name CanApply checks for an
	// explicit install-method declaration. Empty means derive it from the
	// running binary's file name (see installModeEnvName).
	installModeEnv string

	// resolvedTarget caches the symlink-resolved path to the running binary.
	resolvedTarget string
}

// Option configures an Updater. Wire these at compile time in the consumer.
type Option func(*Updater)

// WithVersionProvider sets how binup discovers the running binary's version.
// Defaults to BuildInfoVersion (works with zero wiring for go-install-distributed
// tools); ldflags-based tools should set WithVersionProvider(StaticVersion(v))
// or WithVersionProvider(StaticVersionInfo(...)).
func WithVersionProvider(p VersionProvider) Option {
	return func(u *Updater) { u.versionProvider = p }
}

// WithComparator overrides the default SemVerComparator.
func WithComparator(c Comparator) Option {
	return func(u *Updater) { u.comparator = c }
}

// WithInstallModeEnv overrides the env var name CanApply checks for an
// explicit install-method declaration (e.g. "MYTOOL_INSTALL_MODE").
//
// By default this is derived from the running binary's file name, which is
// almost always right and needs no configuration. Reach for this option only
// when the on-disk binary name isn't a stable identifier for your tool — e.g.
// it gets renamed per-target, or you want the env var name to survive a
// rename of the binary itself.
func WithInstallModeEnv(name string) Option {
	return func(u *Updater) { u.installModeEnv = name }
}

// WithTargetPath overrides the file binup treats as "the running binary" to
// check and replace, bypassing the default os.Executable()-based detection.
//
// This exists for tests and local demos that want to exercise the real
// Check -> CanApply -> Apply pipeline — including the actual minio/selfupdate
// atomic swap — against a scratch file instead of the process's own
// executable. Most consumers never need this.
func WithTargetPath(path string) Option {
	return func(u *Updater) { u.resolvedTarget = path }
}

// New builds an Updater from a Source and options. It fails only if source is
// nil; everything else has a default.
func New(source Source, opts ...Option) (*Updater, error) {
	if source == nil {
		return nil, errors.New("binup: Source must not be nil")
	}
	u := &Updater{
		source:          source,
		comparator:      SemVerComparator{},                    // default: strict semver, dev builds not updated
		versionProvider: VersionProviderFunc(BuildInfoVersion), // default: works with zero wiring for go-install-distributed tools
	}
	for _, opt := range opts {
		opt(u)
	}
	return u, nil
}

// CheckResult is what Check returns: whether an update is available and the
// release metadata behind that decision.
type CheckResult struct {
	Current         VersionInfo
	Release         *Release
	Latest          string
	UpdateAvailable bool
}

// Check asks the VersionProvider for the running binary's version and the
// Source for the latest release, then runs both through the Comparator. It
// has NO side effects — no download, no file changes. This is the first of
// the four steps; the consumer decides what to do with the result.
func (u *Updater) Check(ctx context.Context) (*CheckResult, error) {
	current, err := u.versionProvider.CurrentVersion()
	if err != nil {
		return nil, fmt.Errorf("binup: current version: %w", err)
	}

	rel, err := u.source.Latest(ctx)
	if err != nil {
		return nil, err
	}
	should, err := u.comparator.ShouldUpdate(current.Version, rel.Version)
	if err != nil {
		return nil, err
	}
	return &CheckResult{
		UpdateAvailable: should,
		Latest:          rel.Version,
		Release:         rel,
		Current:         current,
	}, nil
}

// targetPath returns the symlink-resolved absolute path of the running binary —
// the file that will actually be replaced. Resolving symlinks matters: Homebrew
// / asdf / etc. symlink into $PATH, and we must replace the real file, not
// clobber the symlink. Result is cached.
func (u *Updater) targetPath() (string, error) {
	if u.resolvedTarget != "" {
		return u.resolvedTarget, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Fall back to the unresolved path rather than failing outright.
		real = exe
	}
	u.resolvedTarget = real
	return real, nil
}
