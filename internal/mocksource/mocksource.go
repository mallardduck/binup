// Package mocksource is TEST/DEMO SUPPORT CODE, not a production binup.Source
// implementation. It lives under internal/ specifically so it CANNOT be
// imported outside this module — the Go compiler enforces that, so nothing
// downstream can accidentally ship it as a real update backend.
//
// It's a plain (non-"_test.go") file rather than a real test file because
// example/ needs to import it as an ordinary build dependency, not just
// during `go test`.
//
// What it provides: a fully in-memory binup.Source — no network calls, no
// filesystem access, no dependency on a real release backend — so both this
// module's own tests and its runnable example can exercise the full binup
// workflow (Check -> CanApply -> Apply, including the real minio/selfupdate
// atomic swap when combined with binup.WithTargetPath) without depending on
// GitHub, a real repository, or any other external state that can go stale or
// flake in CI.
package mocksource

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/mallardduck/binup"
)

// errNoRelease is returned by Latest when neither Release nor LatestFunc is set.
var errNoRelease = errors.New("mocksource: Release is nil and LatestFunc is not set")

// Source is a binup.Source backed entirely by in-memory fields. The zero
// value's Latest returns errNoRelease until Release or LatestFunc is set;
// Fetch returns an empty stream until Binary or FetchFunc is set.
type Source struct {
	Release    *binup.Release
	LatestFunc func(ctx context.Context) (*binup.Release, error)
	FetchFunc  func(ctx context.Context, rel *binup.Release) (io.ReadCloser, error)
	Binary     []byte
}

// Latest implements binup.Source.
func (s *Source) Latest(ctx context.Context) (*binup.Release, error) {
	if s.LatestFunc != nil {
		return s.LatestFunc(ctx)
	}
	if s.Release == nil {
		return nil, errNoRelease
	}
	return s.Release, nil
}

// Fetch implements binup.Source. It does not verify anything, unlike a real
// Source — that's fine here because binup never re-verifies (see the Source
// security contract in binup's source.go), and this package exists to
// exercise the workflow around Fetch, not to model a supply chain.
func (s *Source) Fetch(ctx context.Context, rel *binup.Release) (io.ReadCloser, error) {
	if s.FetchFunc != nil {
		return s.FetchFunc(ctx, rel)
	}
	return io.NopCloser(bytes.NewReader(s.Binary)), nil
}

// Compile-time assertion that *Source satisfies binup.Source.
var _ binup.Source = (*Source)(nil)
