# binup

Self-update for single-binary Go CLIs. `binup` owns the update **workflow**
(check → guard → prompt → atomic swap); it delegates the swap to
[`minio/selfupdate`](https://github.com/minio/selfupdate) and delegates *where
updates come from* to a pluggable `Source` interface.

## Scope

- Replaces **exactly one executable file** — the running binary. Multi-file
  installs are out of scope; use a real installer/package manager.
- Only updates a **user-owned, user-writable** binary. Managed installs
  (Homebrew, apt, root-owned paths) are **soft-skipped with a reason**, never
  clobbered.

## The flow (consumer owns the prompt)

```go
u, _ := binup.New(src, binup.WithVersionProvider(binup.StaticVersion(version)))

res, err := u.Check(ctx)                 // 1. newer version? (no side effects)
if err != nil || !res.UpdateAvailable { return err }

if skip := u.CanApply(); skip != nil {   // 2. allowed to touch this binary?
    fmt.Printf("v%s available — %s\n", res.Latest, skip.Hint)
    return nil
}

if !prompt(res.Latest) { return nil }    // 3. user consent (your CLI)

_, err = u.Apply(ctx, res.Release)       // 4. download, verify, swap
return err
```

`CanApply` runs **before** the prompt on purpose, so a managed install never
gets a dead-end "install it? [y/N]".

## Layout

```
binup/                    core: Updater, Source, Comparator, guard, Apply
binup/github/              Source backed by ghreleases
binup/example/             runnable consumer example (fully offline, see below)
binup/internal/mocksource/ in-memory test/demo Source — not importable outside this module
```

Dependency direction is one-way — `github → binup`, never the reverse — so core
carries no dependency on `ghreleases`.

## How binup finds "the current version"

Different tools track their own version differently — an ldflags-injected
build-time constant, the module version Go embeds for `go install
module@version`, a VCS tag, a VERSION file — so this is a pluggable
`VersionProvider`, not a single blessed mechanism:

```go
type VersionProvider interface {
    CurrentVersion() (VersionInfo, error)
}

type VersionInfo struct {
    Version   string    // required; what Comparator compares
    Commit    string    // optional, for richer Comparator implementations or CLI display
    BuildDate time.Time
}
```

- **`binup.StaticVersion(v)`** — the common case, for a version injected via
  `-ldflags "-X main.version=v1.2.3"`.
- **`binup.StaticVersionInfo(info)`** — same, but also carries `Commit`/`BuildDate`
  (e.g. from `-X main.commit=... -X main.date=...`) through to
  `CheckResult.Current` for display.
- **`binup.BuildInfoVersion`** — the default when `WithVersionProvider` isn't
  set. Reads `runtime/debug.ReadBuildInfo()`, which Go populates with the real
  module version (and VCS commit/time) when a tool was installed via `go
  install module@version`. Normalizes local `go build`'s `"(devel)"` to `""`,
  which the default `SemVerComparator` treats as a dev build.
- Anything else: implement `VersionProvider` directly, or wrap a closure with
  `binup.VersionProviderFunc`.

## Configuring the github Source

`github.Source`'s defaults assume a goreleaser-style pipeline
(`{name}_{version}_{os}_{arch}.{ext}` assets, a `checksums.txt` manifest), but
every part of that is a `github.Option` because real release pipelines vary:

| Option | Overrides |
|---|---|
| `WithAppName` | the `{name}` template variable / expected binary filename |
| `WithAssetTemplate` | the asset-name template (any `ghrelease.Render` pattern) |
| `WithChecksumAsset` | the checksum-manifest asset name (also a template — e.g. goreleaser's versioned `{name}_{version}_checksums.txt`) |
| `WithTemplateMode` | `github.TemplateStrict` to fail fast on an unrecognized `{variable}` instead of the default `TemplatePermissive`, which silently keeps it as literal text |
| `WithExtFunc` | how the `{ext}` variable is derived from `runtime.GOOS` (default: `zip` on Windows, `tar.gz` elsewhere) |
| `WithBinaryName` | the file looked up inside the downloaded archive, keyed by `runtime.GOOS` |
| `WithExtractFormat` | forces the archive format instead of relying on filename-suffix auto-detection |
| `WithTagPrefix` | restricts release lookup to a tag prefix, for repos publishing multiple release lines (e.g. `cli/v1.2.3`) |
| `WithToken` / `WithHTTPClient` | GitHub API token / a custom `*http.Client` for proxies, timeouts, custom transports |
| `WithManifestVerifier` | authenticate the checksum manifest against a detached signature before any hash in it is trusted; see [Signature verification](#signature-verification) |
| `WithArtifactVerifier` | authenticate the downloaded release asset itself against a detached signature; see [Signature verification](#signature-verification) |
| `WithoutChecksumVerification` | **dangerous** — skips the checksum-manifest requirement; see its doc comment |

`WithTemplateMode` and `WithExtractFormat` take config-value types
(`github.TemplateMode`/`TemplateStrict`/`TemplatePermissive`/`TemplateFailsafe`,
`github.ArchiveFormat`/`FormatTarGz`/etc.) that are re-exported as type
aliases of the underlying `ghreleases` types in `github/aliases.go` — so
`ghreleases` stays an *indirect* dependency. Consumers who only import
`binup` and `binup/github` never need to import `ghreleases` directly.

## Testing without a real release backend

`internal/mocksource` is a fully in-memory `binup.Source` — no network calls,
no filesystem access — used by:

- **This repo's own tests** (`binup_test.go`, `apply_test.go`) to exercise
  `Check` → `CanApply` → `Apply`, including the real `minio/selfupdate` atomic
  swap, against a scratch file (via `binup.WithTargetPath`) instead of a live
  release or the test binary itself.
- **`example/main.go`**, which runs entirely offline for the same reason: no
  real GitHub repo to go stale, no network access needed to try it.
- **The `github` adapter's own tests** (`github/github_test.go`) go one step
  further and exercise the *real* `ghreleases` `Latest`/`Fetch` pipeline —
  asset-name templating, checksum verification, archive extraction — against
  a local `httptest` server standing in for the GitHub API, so there's no
  live-repo dependency there either.

It lives under `internal/` specifically so it can't leak into consumers as an
accidental public API — the Go compiler enforces that. See its doc comment for
why it isn't a `_test.go` file (short version: `example/` needs it as an
ordinary build dependency, not just under `go test`).

## Getting started

```sh
go get github.com/mallardduck/binup
```

Point `github.New("owner/repo", ...)` at your repo. If your release assets
don't follow `{name}_{version}_{os}_{arch}.{ext}` with a `checksums.txt`
manifest, use the `github.With*` options to match your layout (see
"Configuring the github Source" above).

## Signature verification

A checksum file proves a download is intact, not that it is authentic: anyone
who can replace the release can replace the binary *and* `checksums.txt`.
Signature verification closes that gap, and is **off by default**:

```go
v, err := sigstore.NewVerifier(sigstore.Identity{ /* pin your release workflow's identity */ })
if err != nil { /* ... */ }

src := github.New("owner/repo",
    github.WithManifestVerifier(v), // checksums.txt is signed (e.g. goreleaser)
    github.WithArtifactVerifier(v), // and/or each archive is signed
)
```

- Use either option or both; they are independent.
- It **fails closed**. A release without the signature asset fails `Fetch` with
  `github.ErrSignatureMissing`, a rejected signature with
  `github.ErrSignatureInvalid`, and a nil verifier with
  `github.ErrInvalidVerifier`. Nothing falls back to the unsigned path.
- `WithManifestVerifier` makes the manifest mandatory: `WithoutChecksumVerification`
  does not relax it.
- The signature asset is located by `v.SignatureName(assetName)`.
- `github.SignatureVerifier` is a two-method interface, so you can implement it
  without importing `ghreleases`. Ready-made verifiers are separate modules, so
  you only pay for the one you import:

| Module | Verifies | Cost |
|---|---|---|
| `github.com/mallardduck/ghreleases/pgp` | detached OpenPGP `.asc` signatures (e.g. Helm) | small (`ProtonMail/go-crypto`) |
| `github.com/mallardduck/ghreleases/sigstore` | keyless Sigstore `.sigstore.json` bundles (goreleaser, syft, flux) | large (`sigstore-go`, ~24 MB binary) |

Both of those modules declare `go 1.25.8`, so importing one raises your
minimum Go toolchain above binup's own.

## Verification note

The security model puts checksum/signature verification **inside the Source**
(your `ghreleases` already does this), so core hands minio an empty
`Options{}` and treats the fetched stream as trusted. Any custom Source **must**
verify what it returns — this is a documented contract the type system can't
enforce.
