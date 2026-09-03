package binup

import (
	"fmt"

	"golang.org/x/mod/semver"
)

// Comparator decides whether a candidate version should replace the current one.
//
// It returns a bool, not an ordering (-1/0/1), because the real question binup
// asks is "should we offer this update?" — not "which is bigger?". This lets an
// implementation encode whatever policy it wants: SemVer precedence, date
// stamps, monotonic build numbers, channel awareness ("only stable over
// stable"), or pinned-rollback behavior.
//
// The Comparator is also the single place allowed to veto a downgrade. binup
// does NOT enforce "never go backwards" independently — if you want that, encode
// it here (the default SemVerComparator does).
type Comparator interface {
	// ShouldUpdate reports whether candidate should replace current. current is
	// the running binary's version; candidate is what the Source is offering.
	ShouldUpdate(current, candidate string) (bool, error)
}

// ComparatorFunc adapts a plain function to the Comparator interface.
type ComparatorFunc func(current, candidate string) (bool, error)

// ShouldUpdate implements Comparator.
func (f ComparatorFunc) ShouldUpdate(current, candidate string) (bool, error) {
	return f(current, candidate)
}

// SemVerComparator is the default. It offers an update only when candidate is a
// strictly higher semantic version than current (so downgrades and equal
// versions are refused). A missing "v" prefix is tolerated on both sides.
type SemVerComparator struct {
	// DevBuildOffersUpdate controls what happens when the CURRENT version is not
	// valid semver (typical of a local `go build` / dev binary). If true, any
	// valid-semver candidate is offered (dev builds always see updates). If
	// false, dev builds are never updated. Default false.
	DevBuildOffersUpdate bool
}

// ShouldUpdate implements Comparator.
func (c SemVerComparator) ShouldUpdate(current, candidate string) (bool, error) {
	cur := canonical(current)
	cand := canonical(candidate)

	if !semver.IsValid(cand) {
		return false, fmt.Errorf("binup: candidate version %q is not valid semver", candidate)
	}
	if !semver.IsValid(cur) {
		// Current isn't semver — almost certainly a dev/dirty build.
		return c.DevBuildOffersUpdate, nil
	}
	return semver.Compare(cand, cur) > 0, nil
}

// canonical ensures a leading "v" so golang.org/x/mod/semver accepts it.
func canonical(v string) string {
	if v == "" {
		return v
	}
	if v[0] != 'v' {
		return "v" + v
	}
	return v
}
