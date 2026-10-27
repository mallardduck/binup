package mocksource

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/mallardduck/binup"
)

func TestSource_Latest(t *testing.T) {
	ctx := context.Background()

	t.Run("zero value errors until configured", func(t *testing.T) {
		if _, err := (&Source{}).Latest(ctx); !errors.Is(err, errNoRelease) {
			t.Fatalf("Latest err = %v, want errNoRelease", err)
		}
	})

	t.Run("returns Release", func(t *testing.T) {
		want := &binup.Release{Version: "v1.2.3"}
		got, err := (&Source{Release: want}).Latest(ctx)
		if err != nil || got != want {
			t.Fatalf("Latest = (%v, %v), want (%v, nil)", got, err, want)
		}
	})

	t.Run("LatestFunc wins over Release", func(t *testing.T) {
		fromFunc := &binup.Release{Version: "v9.9.9"}
		s := &Source{
			Release:    &binup.Release{Version: "v1.0.0"},
			LatestFunc: func(context.Context) (*binup.Release, error) { return fromFunc, nil },
		}
		if got, _ := s.Latest(ctx); got != fromFunc {
			t.Fatalf("Latest = %v, want the LatestFunc result", got)
		}
	})
}

func TestSource_Fetch(t *testing.T) {
	ctx := context.Background()
	rel := &binup.Release{Version: "v1.0.0"}

	t.Run("streams Binary", func(t *testing.T) {
		rc, err := (&Source{Binary: []byte("bin")}).Fetch(ctx, rel)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if b, _ := io.ReadAll(rc); string(b) != "bin" {
			t.Fatalf("stream = %q, want %q", b, "bin")
		}
	})

	t.Run("zero value is an empty stream", func(t *testing.T) {
		rc, err := (&Source{}).Fetch(ctx, rel)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if b, _ := io.ReadAll(rc); len(b) != 0 {
			t.Fatalf("stream = %q, want empty", b)
		}
	})

	t.Run("FetchFunc receives the release", func(t *testing.T) {
		var got *binup.Release
		s := &Source{FetchFunc: func(_ context.Context, r *binup.Release) (io.ReadCloser, error) {
			got = r
			return nil, errors.New("stop")
		}}
		if _, err := s.Fetch(ctx, rel); err == nil || got != rel {
			t.Fatalf("FetchFunc not used correctly: err=%v got=%v", err, got)
		}
	})
}
