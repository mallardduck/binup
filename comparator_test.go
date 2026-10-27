package binup

import (
	"errors"
	"testing"
)

func TestSemVerComparator(t *testing.T) {
	cases := []struct {
		name      string
		dev       bool
		current   string
		candidate string
		want      bool
		wantErr   bool
	}{
		{name: "newer", current: "v1.0.0", candidate: "v1.1.0", want: true},
		{name: "older", current: "v1.1.0", candidate: "v1.0.0", want: false},
		{name: "equal", current: "v1.0.0", candidate: "v1.0.0", want: false},
		{name: "no v prefix", current: "1.0.0", candidate: "1.2.0", want: true},
		{name: "prerelease lower", current: "v1.0.0", candidate: "v1.0.0-rc1", want: false},
		{name: "dev current, offer off", current: "dev", candidate: "v1.0.0", want: false},
		{name: "dev current, offer on", dev: true, current: "dev", candidate: "v1.0.0", want: true},
		{name: "bad candidate", current: "v1.0.0", candidate: "not-a-version", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := SemVerComparator{DevBuildOffersUpdate: tc.dev}
			got, err := c.ShouldUpdate(tc.current, tc.candidate)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ShouldUpdate(%q,%q)=%v, want %v", tc.current, tc.candidate, got, tc.want)
			}
		})
	}
}

func TestComparatorFunc(t *testing.T) {
	wantErr := errors.New("boom")
	var gotCurrent, gotCandidate string
	f := ComparatorFunc(func(current, candidate string) (bool, error) {
		gotCurrent, gotCandidate = current, candidate
		return true, wantErr
	})

	// Used through the interface, as Updater does.
	var c Comparator = f
	ok, err := c.ShouldUpdate("v1", "v2")
	if !ok || !errors.Is(err, wantErr) {
		t.Fatalf("ShouldUpdate = (%v, %v), want (true, %v)", ok, err, wantErr)
	}
	if gotCurrent != "v1" || gotCandidate != "v2" {
		t.Fatalf("func saw (%q, %q), want (v1, v2) in that order", gotCurrent, gotCandidate)
	}
}
