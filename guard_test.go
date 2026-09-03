package binup

import "testing"

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
