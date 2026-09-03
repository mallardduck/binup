package binup

import (
	"context"
	"fmt"

	"github.com/minio/selfupdate"
)

// ApplyResult reports the outcome of Apply.
type ApplyResult struct {
	// Skipped is set (non-nil) when binup declined to update because the install
	// is managed or not user-writable. When Skipped != nil, the returned error
	// is nil — a skip is not a failure.
	Skipped *SkipReason

	// Version is the version that was installed (only when Updated).
	Version string

	// Updated is true when the binary was successfully replaced.
	Updated bool
}

// Apply downloads, verifies (via the Source), and atomically swaps in the new
// binary. Call it only after the consumer has confirmed with the user.
//
// Apply re-checks CanApply internally, so it is safe to call directly — but it
// returns the SkipReason as a RESULT, not an error, so a managed install won't
// trip your `if err != nil` path.
//
// On a hard failure, the error is wrapped. Because the underlying swap distinguishes
// "update failed, binary intact" from "rollback also failed, binary broken", the
// returned error text reflects which case occurred.
func (u *Updater) Apply(ctx context.Context, rel *Release) (*ApplyResult, error) {
	if rel == nil {
		return nil, fmt.Errorf("binup: Apply called with nil Release")
	}
	if skip := u.CanApply(); skip != nil {
		return &ApplyResult{Skipped: skip}, nil
	}

	target, err := u.targetPath()
	if err != nil {
		return nil, fmt.Errorf("binup: resolve target: %w", err)
	}

	// The Source returns an already-verified, already-extracted binary stream.
	// Core does NOT re-verify (see the Source security contract).
	stream, err := u.source.Fetch(ctx, rel)
	if err != nil {
		return nil, fmt.Errorf("binup: fetch: %w", err)
	}
	defer stream.Close()

	// Hand off the atomic swap to minio/selfupdate. Checksum is left empty on
	// purpose — verification already happened in the Source. TargetPath is our
	// symlink-resolved real path; TargetMode is left 0 so minio defaults to 0755.
	err = selfupdate.Apply(stream, selfupdate.Options{
		TargetPath: target,
	})
	if err != nil {
		// Distinguish a failed rollback (binary may be broken) from a failed
		// update (binary intact). Always call RollbackError on a non-nil error.
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return nil, fmt.Errorf(
				"binup: update failed AND rollback failed (%w); the binary may be broken, reinstall manually: %w",
				rerr, err,
			)
		}
		return nil, fmt.Errorf("binup: update failed (binary left intact): %w", err)
	}

	return &ApplyResult{Updated: true, Version: rel.Version}, nil
}
