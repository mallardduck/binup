package binup

import (
	"context"
	"io"
)

// Source is the pluggable backend that tells binup where updates come from and
// retrieves them. The default implementation lives in the ./github subpackage;
// end users can supply their own (private server, S3 bucket, etc.).
//
// SECURITY CONTRACT — READ THIS:
//
// Implementations MUST verify the integrity (checksum and/or signature) of the
// bytes returned by Fetch. binup treats the returned stream as TRUSTED and does
// NOT re-verify it before swapping it into place. A Source that returns
// unverified bytes is a supply-chain hole binup cannot catch for you.
//
// Fetch always targets the platform binup is currently running on. There is no
// GOOS/GOARCH parameter: implementations read runtime.GOOS / runtime.GOARCH
// themselves, because you are always updating the executable on this machine.
type Source interface {
	// Latest returns metadata about the newest release WITHOUT downloading the
	// binary. It is used only for the version comparison, so it should be cheap.
	Latest(ctx context.Context) (*Release, error)

	// Fetch retrieves, verifies, and (if the asset is an archive) extracts the
	// asset matching the current platform, returning the executable as a stream.
	//
	// The caller closes the returned ReadCloser. Verify-then-extract: verify the
	// downloaded asset first, then unpack the inner binary.
	Fetch(ctx context.Context, rel *Release) (io.ReadCloser, error)
}

// Release is the metadata binup needs to decide whether to offer an update and
// to present it to the user. Sources may carry richer data of their own, but
// this is all the core relies on.
type Release struct {
	// Version is the release's version string, as the Source reports it (e.g.
	// "v1.4.2"). It is handed to the Comparator as-is; the comparator owns any
	// normalization.
	Version string

	// Notes is optional human-readable release notes / changelog, for the CLI
	// to display. May be empty.
	Notes string
}
