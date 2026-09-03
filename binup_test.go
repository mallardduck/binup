package binup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mallardduck/binup"
	"github.com/mallardduck/binup/internal/mocksource"
)

func TestCheck(t *testing.T) {
	ctx := context.Background()

	t.Run("update available", func(t *testing.T) {
		src := &mocksource.Source{Release: &binup.Release{Version: "v2.0.0"}}
		u, err := binup.New(src, binup.WithVersionProvider(binup.StaticVersion("v1.0.0")))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		res, err := u.Check(ctx)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !res.UpdateAvailable {
			t.Fatal("expected UpdateAvailable=true")
		}
		if res.Latest != "v2.0.0" {
			t.Fatalf("Latest=%q, want v2.0.0", res.Latest)
		}
		if res.Release == nil || res.Release.Version != "v2.0.0" {
			t.Fatalf("Release=%+v, want Version v2.0.0", res.Release)
		}
		if res.Current.Version != "v1.0.0" {
			t.Fatalf("Current.Version = %q, want v1.0.0", res.Current.Version)
		}
	})

	t.Run("already up to date", func(t *testing.T) {
		src := &mocksource.Source{Release: &binup.Release{Version: "v1.0.0"}}
		u, err := binup.New(src, binup.WithVersionProvider(binup.StaticVersion("v1.0.0")))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		res, err := u.Check(ctx)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if res.UpdateAvailable {
			t.Fatal("expected UpdateAvailable=false for equal versions")
		}
	})

	t.Run("source error propagates", func(t *testing.T) {
		wantErr := errors.New("network down")
		src := &mocksource.Source{LatestFunc: func(context.Context) (*binup.Release, error) {
			return nil, wantErr
		}}
		u, err := binup.New(src, binup.WithVersionProvider(binup.StaticVersion("v1.0.0")))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := u.Check(ctx); !errors.Is(err, wantErr) {
			t.Fatalf("Check err = %v, want %v", err, wantErr)
		}
	})

	t.Run("version provider error propagates", func(t *testing.T) {
		wantErr := errors.New("version lookup failed")
		src := &mocksource.Source{Release: &binup.Release{Version: "v2.0.0"}}
		u, err := binup.New(src, binup.WithVersionProvider(binup.VersionProviderFunc(
			func() (binup.VersionInfo, error) { return binup.VersionInfo{}, wantErr },
		)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := u.Check(ctx); !errors.Is(err, wantErr) {
			t.Fatalf("Check err = %v, want %v", err, wantErr)
		}
	})

	t.Run("comparator error propagates", func(t *testing.T) {
		src := &mocksource.Source{Release: &binup.Release{Version: "not-semver"}}
		u, err := binup.New(src, binup.WithVersionProvider(binup.StaticVersion("v1.0.0")))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := u.Check(ctx); err == nil {
			t.Fatal("expected an error for a non-semver candidate")
		}
	})
}

func TestNew_NilSource(t *testing.T) {
	if _, err := binup.New(nil); err == nil {
		t.Fatal("expected an error for a nil Source")
	}
}
