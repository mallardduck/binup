package binup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mallardduck/binup"
	"github.com/mallardduck/binup/internal/mocksource"
)

// newScratchTarget creates a small executable file standing in for "the
// installed binary" and returns its path, paired with binup.WithTargetPath so
// Apply's real minio/selfupdate swap can be exercised without touching the
// test binary itself.
func newScratchTarget(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target-binary")
	if err := os.WriteFile(path, []byte("original content"), 0o755); err != nil {
		t.Fatalf("write scratch target: %v", err)
	}
	return path
}

func TestApply_Success(t *testing.T) {
	target := newScratchTarget(t)
	newContent := []byte("updated content")

	src := &mocksource.Source{
		Release: &binup.Release{Version: "v2.0.0"},
		Binary:  newContent,
	}
	u, err := binup.New(src,
		binup.WithVersionProvider(binup.StaticVersion("v1.0.0")),
		binup.WithTargetPath(target),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatal("expected UpdateAvailable=true")
	}

	applied, err := u.Apply(context.Background(), res.Release)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.Skipped != nil {
		t.Fatalf("Apply unexpectedly skipped: %+v", applied.Skipped)
	}
	if !applied.Updated || applied.Version != "v2.0.0" {
		t.Fatalf("ApplyResult=%+v, want Updated=true Version=v2.0.0", applied)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after Apply: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("target content = %q, want %q", got, newContent)
	}
}

func TestApply_SkippedByInstallModeEnv(t *testing.T) {
	target := newScratchTarget(t)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target before Apply: %v", err)
	}

	envName := "TESTBINUP_INSTALL_MODE"
	t.Setenv(envName, "homebrew")

	src := &mocksource.Source{
		Release: &binup.Release{Version: "v2.0.0"},
		Binary:  []byte("should never be written"),
	}
	u, err := binup.New(src,
		binup.WithVersionProvider(binup.StaticVersion("v1.0.0")),
		binup.WithTargetPath(target),
		binup.WithInstallModeEnv(envName),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	applied, err := u.Apply(context.Background(), res.Release)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.Updated {
		t.Fatal("expected Apply to skip, not update, when the install-mode env var is set")
	}
	if applied.Skipped == nil || applied.Skipped.Code != binup.SkipManaged {
		t.Fatalf("Skipped=%+v, want SkipManaged", applied.Skipped)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after Apply: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("target file was modified despite the managed-install skip")
	}
}

func TestApply_NilRelease(t *testing.T) {
	target := newScratchTarget(t)
	src := &mocksource.Source{Release: &binup.Release{Version: "v2.0.0"}}
	u, err := binup.New(src, binup.WithTargetPath(target))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := u.Apply(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil Release")
	}
}
