// Command example shows how a consumer wires binup into its own CLI.
//
// It runs entirely offline: it uses mocksource.Source instead of a real
// backend, and binup.WithTargetPath to point Apply at a scratch file instead
// of this process's own executable. That makes it safe to actually run to
// completion (including the real minio/selfupdate atomic swap) — no network
// access, and it never touches the compiled example binary itself.
//
// To wire this against a real GitHub-releases-backed tool instead, replace
// the mocksource.Source below with github.New("owner/repo", ...) — see
// github/github.go for the available Options if your release assets don't
// follow the default goreleaser-style naming.
//
// Build with a version injected:
//
//	go build -ldflags "-X main.version=v1.0.0" ./example
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mallardduck/binup"
	"github.com/mallardduck/binup/internal/mocksource"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// A scratch file standing in for "the installed binary". A real consumer
	// doesn't do this — Apply targets os.Executable() by default — but it lets
	// this example actually run the full pipeline, including the real
	// minio/selfupdate swap, without touching the compiled example binary.
	target, cleanup, err := scratchTarget()
	if err != nil {
		return err
	}
	defer cleanup()

	src := &mocksource.Source{
		Release: &binup.Release{Version: "v9.9.9", Notes: "mock release for the offline demo"},
		Binary:  []byte("#!/bin/sh\necho mock updated binary\n"),
	}
	u, err := binup.New(src,
		binup.WithVersionProvider(binup.StaticVersion(version)),
		binup.WithTargetPath(target),
	)
	if err != nil {
		return err
	}

	// 1. Is there a newer version?
	res, err := u.Check(ctx)
	if err != nil {
		return err
	}
	if !res.UpdateAvailable {
		fmt.Println("already up to date:", version)
		return nil
	}

	// 2. Are we even allowed to touch this binary?
	if skip := u.CanApply(); skip != nil {
		fmt.Printf("%s is available — %s\n", res.Latest, skip.Hint)
		return nil
	}

	// 3. Does the user consent? (entirely the CLI's job)
	if !prompt(fmt.Sprintf("update to %s?", res.Latest)) {
		fmt.Println("skipped.")
		return nil
	}

	// 4. Do it.
	applied, err := u.Apply(ctx, res.Release)
	if err != nil {
		return err
	}
	if applied.Skipped != nil {
		fmt.Println("not applied:", applied.Skipped.Hint)
		return nil
	}
	fmt.Println("updated to", applied.Version, "(scratch file at", target, ")")
	return nil
}

func scratchTarget() (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "binup-example-target-*")
	if err != nil {
		return "", nil, err
	}
	path = f.Name()
	if _, err := f.WriteString("#!/bin/sh\necho original binary\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", nil, err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		os.Remove(path)
		return "", nil, err
	}
	return path, func() { os.Remove(path) }, nil
}

func prompt(q string) bool {
	fmt.Printf("%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
