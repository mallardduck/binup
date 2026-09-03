package binup

import (
	"runtime/debug"
	"time"
)

// VersionInfo describes the running binary's version. Version is the only
// field Check relies on (it's what gets handed to the Comparator); Commit and
// BuildDate are optional metadata for richer Comparator implementations or
// CLI display (e.g. "current: v1.2.3, commit abc1234, built 2024-01-01").
type VersionInfo struct {
	BuildDate time.Time
	Version   string
	Commit    string
}

// VersionProvider reports the version of the currently running binary. Tools
// manage this differently — an ldflags-injected build-time constant, a
// go-install-time module version, a VCS tag, a VERSION file — so it's a
// pluggable interface rather than a single blessed mechanism.
type VersionProvider interface {
	CurrentVersion() (VersionInfo, error)
}

// VersionProviderFunc adapts a plain function to the VersionProvider interface.
type VersionProviderFunc func() (VersionInfo, error)

// CurrentVersion implements VersionProvider.
func (f VersionProviderFunc) CurrentVersion() (VersionInfo, error) { return f() }

// StaticVersion returns a VersionProvider that always reports version v —
// the common case for a version injected at build time via
// -ldflags "-X main.version=v1.2.3".
func StaticVersion(v string) VersionProvider {
	return VersionProviderFunc(func() (VersionInfo, error) {
		return VersionInfo{Version: v}, nil
	})
}

// StaticVersionInfo returns a VersionProvider that always reports info as-is
// — for tools that also inject commit/build-date via ldflags (e.g.
// -X main.commit=... -X main.date=...) and want them carried through to
// CheckResult.Current for display.
func StaticVersionInfo(info VersionInfo) VersionProvider {
	return VersionProviderFunc(func() (VersionInfo, error) { return info, nil })
}

// BuildInfoVersion reports VersionInfo via runtime/debug.ReadBuildInfo — the
// main module version and VCS stamping (commit, build time) Go embeds when
// the binary was built with `go install module@version` or `go build` inside
// a VCS checkout. For an ordinary local `go build`, Go reports "(devel)" for
// Version; BuildInfoVersion normalizes that (and missing/unreadable build
// info) to "", which the default SemVerComparator treats as a dev build.
//
// This is binup's default VersionProvider when none is configured, so
// go-install-distributed tools get working version detection with zero
// wiring; ldflags-based tools should set one explicitly via
// WithVersionProvider(StaticVersion(...)).
func BuildInfoVersion() (VersionInfo, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return VersionInfo{}, nil
	}

	v := bi.Main.Version
	if v == "(devel)" {
		v = ""
	}
	info := VersionInfo{Version: v}

	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				info.BuildDate = t
			}
		}
	}
	return info, nil
}
