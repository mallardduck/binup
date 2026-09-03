package binup

import (
	"testing"
	"time"
)

func TestStaticVersion(t *testing.T) {
	p := StaticVersion("v1.2.3")
	info, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if info != (VersionInfo{Version: "v1.2.3"}) {
		t.Fatalf("info = %+v, want only Version set", info)
	}
}

func TestStaticVersionInfo(t *testing.T) {
	want := VersionInfo{Version: "v1.2.3", Commit: "abc123", BuildDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	p := StaticVersionInfo(want)
	got, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if got != want {
		t.Fatalf("info = %+v, want %+v", got, want)
	}
}

func TestVersionProviderFunc(t *testing.T) {
	called := false
	p := VersionProviderFunc(func() (VersionInfo, error) {
		called = true
		return VersionInfo{Version: "v9.9.9"}, nil
	})
	info, err := p.CurrentVersion()
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if !called {
		t.Fatal("underlying func was not called")
	}
	if info.Version != "v9.9.9" {
		t.Fatalf("Version = %q, want v9.9.9", info.Version)
	}
}

func TestBuildInfoVersion(t *testing.T) {
	// `go test` builds a binary via a plain `go build` equivalent, not
	// `go install module@version`, so Main.Version is "(devel)" — normalized
	// to "". This just pins that normalization; it doesn't assert on Commit/
	// BuildDate since VCS stamping availability depends on the build
	// environment (e.g. CI checkouts without a .git dir won't have it).
	info, err := BuildInfoVersion()
	if err != nil {
		t.Fatalf("BuildInfoVersion: %v", err)
	}
	if info.Version != "" {
		t.Fatalf("Version = %q, want empty for a `go test`-built binary", info.Version)
	}
}
