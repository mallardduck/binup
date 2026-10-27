package binup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeEnvName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "mytool", want: "MYTOOL"},
		{name: "hyphenated", in: "my-tool", want: "MY_TOOL"},
		{name: "dotted", in: "my.tool", want: "MY_TOOL"},
		{name: "already upper", in: "MYTOOL", want: "MYTOOL"},
		{name: "mixed digits", in: "tool2go", want: "TOOL2GO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeEnvName(tc.in); got != tc.want {
				t.Fatalf("sanitizeEnvName(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEnvNameFromBinaryPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "tool one", path: "/usr/local/bin/tool-one", want: "TOOL_ONE_INSTALL_MODE"},
		{name: "tool two, different name", path: "/opt/homebrew/bin/tool-two", want: "TOOL_TWO_INSTALL_MODE"},
		{name: "exe suffix", path: "/opt/tools/mytool.exe", want: "MYTOOL_INSTALL_MODE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envNameFromBinaryPath(tc.path); got != tc.want {
				t.Fatalf("envNameFromBinaryPath(%q)=%q, want %q", tc.path, got, tc.want)
			}
		})
	}

	// Two differently-named binaries must never derive the same env var —
	// that's the collision the per-binary scoping exists to prevent.
	a := envNameFromBinaryPath("/usr/local/bin/tool-one")
	b := envNameFromBinaryPath("/usr/local/bin/tool-two")
	if a == b {
		t.Fatalf("distinct binaries derived the same env var name: %q", a)
	}
}

func TestInstallModeEnvName(t *testing.T) {
	// Explicit override wins over derivation.
	u := &Updater{installModeEnv: "MYTOOL_INSTALL_MODE"}
	if got := u.installModeEnvName(); got != "MYTOOL_INSTALL_MODE" {
		t.Fatalf("installModeEnvName()=%q, want override %q", got, "MYTOOL_INSTALL_MODE")
	}
}

func TestCanApply_ScopedEnvVar(t *testing.T) {
	u1 := &Updater{installModeEnv: "TOOL_ONE_INSTALL_MODE"}
	u2 := &Updater{installModeEnv: "TOOL_TWO_INSTALL_MODE"}

	t.Setenv("TOOL_ONE_INSTALL_MODE", "homebrew")

	skip := u1.CanApply()
	if skip == nil || skip.Code != SkipManaged {
		t.Fatalf("u1.CanApply() = %v, want SkipManaged", skip)
	}

	// A different tool's env var must NOT be affected by tool one's setting —
	// this is the collision the per-binary scoping exists to prevent. u2 will
	// fall through to the writability check against the test binary itself,
	// which is writable, so it should NOT be skipped as managed.
	if skip := u2.CanApply(); skip != nil && skip.Code == SkipManaged {
		t.Fatalf("u2.CanApply() leaked tool one's install-mode env var: %v", skip)
	}
}

func TestSkipReason_String(t *testing.T) {
	s := &SkipReason{Code: SkipManaged, Hint: "use brew"}
	if got := s.String(); got != "use brew" {
		t.Fatalf("String() = %q, want the hint", got)
	}
}

func TestManagedHint(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"homebrew", "installed via Homebrew; run `brew upgrade` instead"},
		{"brew", "installed via Homebrew; run `brew upgrade` instead"},
		{"HomeBrew", "installed via Homebrew; run `brew upgrade` instead"},
		{"apt", "installed via apt; run `apt upgrade` instead"},
		{"dpkg", "installed via apt; run `apt upgrade` instead"},
		{"deb", "installed via apt; run `apt upgrade` instead"},
		{"scoop", "installed via Scoop; run `scoop update` instead"},
		// Unknown methods keep the caller's spelling, not a lowercased one.
		{"Nix", "installed via Nix; update through that instead"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			if got := managedHint(tc.method); got != tc.want {
				t.Fatalf("managedHint(%q) = %q, want %q", tc.method, got, tc.want)
			}
		})
	}
}

func TestMethodFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/usr/local/Cellar/mytool/1.0/bin/mytool", "homebrew"},
		{"/opt/homebrew/bin/mytool", "homebrew"},
		{"/home/linuxbrew/.linuxbrew/bin/mytool", "homebrew"},
		{"/usr/local/bin/mytool", ""},
		{"/home/me/bin/mytool", ""},
		// A lookalike prefix is not Homebrew.
		{"/opt/homebrew-not/bin/mytool", ""},
	}
	for _, tc := range tests {
		if got := methodFromPath(tc.path); got != tc.want {
			t.Errorf("methodFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestInstallModeEnvName_DerivedFromExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	want := envNameFromBinaryPath(exe)
	if got := (&Updater{}).installModeEnvName(); got != want || !strings.HasSuffix(got, "_INSTALL_MODE") {
		t.Fatalf("installModeEnvName() = %q, want %q", got, want)
	}
}

func TestCanApply_Writability(t *testing.T) {
	const env = "BINUP_GUARD_TEST_INSTALL_MODE"

	t.Run("writable target may be updated", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "tool")
		if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		u := &Updater{installModeEnv: env, resolvedTarget: target}
		if skip := u.CanApply(); skip != nil {
			t.Fatalf("CanApply() = %+v, want nil", skip)
		}
	})

	t.Run("unwritable target is skipped with a hint", func(t *testing.T) {
		// A directory that does not exist can't hold minio's temp file, which
		// is exactly the permission probe CanApply delegates to. Unlike chmod
		// this behaves the same as root and on Windows.
		target := filepath.Join(t.TempDir(), "missing-dir", "tool")
		u := &Updater{installModeEnv: env, resolvedTarget: target}

		skip := u.CanApply()
		if skip == nil || skip.Code != SkipNotWritable {
			t.Fatalf("CanApply() = %+v, want SkipNotWritable", skip)
		}
		if !strings.Contains(skip.Hint, "isn't writable") {
			t.Fatalf("Hint = %q, want the generic not-writable hint", skip.Hint)
		}
	})

	t.Run("managed env var takes precedence over writability", func(t *testing.T) {
		t.Setenv(env, "scoop")
		target := filepath.Join(t.TempDir(), "missing-dir", "tool")
		u := &Updater{installModeEnv: env, resolvedTarget: target}

		skip := u.CanApply()
		if skip == nil || skip.Code != SkipManaged || !strings.Contains(skip.Hint, "Scoop") {
			t.Fatalf("CanApply() = %+v, want SkipManaged with the Scoop hint", skip)
		}
	})

	t.Run("blank env var is not a declaration", func(t *testing.T) {
		t.Setenv(env, "   ")
		target := filepath.Join(t.TempDir(), "tool")
		if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		u := &Updater{installModeEnv: env, resolvedTarget: target}
		if skip := u.CanApply(); skip != nil {
			t.Fatalf("CanApply() = %+v, want nil for a whitespace-only value", skip)
		}
	})
}
