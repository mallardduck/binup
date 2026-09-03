package binup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/minio/selfupdate"
)

// installModeEnvSuffix is appended to the derived (or overridden) app name to
// build the env var packages you produce should set to declare a managed
// install, e.g. "MYTOOL_INSTALL_MODE".
const installModeEnvSuffix = "_INSTALL_MODE"

// installModeEnvName returns the env var binup checks for an explicit
// install-method declaration. It is scoped to the CONSUMING BINARY, not to
// binup itself — a single shared name (e.g. a hypothetical
// "BINUP_INSTALL_METHOD") would let one binup-linked tool's packaging leak
// into another binup-linked tool on the same machine. Precedence:
//
//  1. WithInstallModeEnv override, if set.
//  2. Derived from the running executable's file name: sanitize to
//     [A-Z0-9_], uppercase, suffix "_INSTALL_MODE". "mytool" (or
//     "mytool.exe") becomes "MYTOOL_INSTALL_MODE".
func (u *Updater) installModeEnvName() string {
	if u.installModeEnv != "" {
		return u.installModeEnv
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return envNameFromBinaryPath(exe)
}

// envNameFromBinaryPath derives the install-mode env var name from an
// executable path. Split out from installModeEnvName so it can be exercised
// with an arbitrary path in tests, independent of the actual test binary's
// on-disk name.
func envNameFromBinaryPath(exe string) string {
	base := filepath.Base(exe)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return ""
	}
	return sanitizeEnvName(base) + installModeEnvSuffix
}

// sanitizeEnvName uppercases name and replaces any character that isn't
// [A-Z0-9_] with "_", so arbitrary binary names become valid env var names.
func sanitizeEnvName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if r == '_' || unicode.IsDigit(r) || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// SkipCode categorizes why an update was not applied. It is deliberately NOT an
// error — a managed install is a normal, expected state, not a failure.
type SkipCode int

const (
	// SkipManaged: an install-method env var declared a package manager.
	SkipManaged SkipCode = iota + 1
	// SkipNotWritable: the target binary (or its directory) is not writable by
	// the current user — a system install, root-owned, etc.
	SkipNotWritable
)

// SkipReason explains why binup declined to update. The consumer's CLI decides
// how to present Hint to the user.
type SkipReason struct {
	Hint string
	Code SkipCode
}

func (s *SkipReason) String() string { return s.Hint }

// CanApply reports whether binup may replace this binary. A nil return means an
// update can proceed. Call it BEFORE prompting the user, so a managed install
// never gets a dead-end "install it? [y/N]".
//
// Apply calls CanApply internally too, so it is always safe to call standalone.
func (u *Updater) CanApply() *SkipReason {
	// 1. Explicit env var wins — packages you produce set this, scoped to this
	//    binary (see installModeEnvName) so it can't collide with another
	//    binup-linked tool on the same machine.
	if name := u.installModeEnvName(); name != "" {
		if method := strings.TrimSpace(os.Getenv(name)); method != "" {
			return &SkipReason{Code: SkipManaged, Hint: managedHint(method)}
		}
	}

	// 2. Permission check. Delegated to minio's own CheckPermissions so the
	//    guard uses the EXACT test the swap will later run — it opens a temp
	//    file in the target's directory. If that fails, this is a system/managed
	//    install we shouldn't touch. We point it at our symlink-resolved path.
	target, err := u.targetPath()
	if err != nil {
		return &SkipReason{
			Code: SkipNotWritable,
			Hint: fmt.Sprintf("cannot locate the running binary: %v", err),
		}
	}
	opts := selfupdate.Options{TargetPath: target}
	if err := opts.CheckPermissions(); err != nil {
		hint := "this binary isn't writable by your user; it's likely a system " +
			"or package-manager install — update it through that channel"
		if m := methodFromPath(target); m != "" {
			hint = managedHint(m)
		}
		return &SkipReason{Code: SkipNotWritable, Hint: hint}
	}

	return nil
}

// methodFromPath guesses a package manager from where the binary lives, purely
// to produce a friendlier hint. Returns "" if unknown.
func methodFromPath(path string) string {
	p := filepath.ToSlash(path)
	switch {
	case strings.Contains(p, "/Cellar/"),
		strings.HasPrefix(p, "/opt/homebrew/"),
		strings.HasPrefix(p, "/home/linuxbrew/"):
		return "homebrew"
	default:
		return ""
	}
}

// managedHint renders the user-facing message for a known install method.
func managedHint(method string) string {
	switch strings.ToLower(method) {
	case "homebrew", "brew":
		return "installed via Homebrew; run `brew upgrade` instead"
	case "apt", "dpkg", "deb":
		return "installed via apt; run `apt upgrade` instead"
	case "scoop":
		return "installed via Scoop; run `scoop update` instead"
	default:
		return fmt.Sprintf("installed via %s; update through that instead", method)
	}
}
