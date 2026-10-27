// Package github provides a binup.Source backed by GitHub Releases, using the
// ghreleases library (github.com/mallardduck/ghreleases, package name
// "ghrelease") for release lookup, download, checksum verification, and
// archive extraction.
//
// It lives in a subpackage so binup core has ZERO dependency on ghreleases —
// consumers who bring their own Source don't pull in a dep they don't use.
//
// Dependency direction (no import cycle): github -> binup, github -> ghreleases.
// Core binup never imports this package.
//
// Asset matching assumes a goreleaser-style naming convention, configurable
// via Options: the asset name is rendered from a template (default
// "{name}_{version}_{os}_{arch}.{ext}") and a checksum manifest asset (default
// "checksums.txt", GNU-coreutils "<hash>  <filename>" format) is required and
// verified before extraction — see the Source security contract in
// ../source.go. Use WithoutChecksumVerification only if you accept that risk.
//
// Checksums prove integrity, not authenticity. WithManifestVerifier and
// WithArtifactVerifier additionally authenticate the checksum manifest and/or
// the release asset against detached signatures (e.g. keyless Sigstore).
//
// Every non-trivial ghreleases knob is exposed via an Option rather than
// hardcoded — asset/checksum naming, template strictness, HTTP client,
// monorepo tag-prefix filtering, archive format — because different release
// pipelines configure ghreleases differently, and baking in one shape here
// just moves the problem to whoever's pipeline doesn't match it.
package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"

	"github.com/mallardduck/binup"
	ghrelease "github.com/mallardduck/ghreleases"
)

// defaultAssetTemplate is the ghrelease.Render pattern used to compute the
// expected release asset name for the current platform. Override with
// WithAssetTemplate if your release process uses a different layout.
const defaultAssetTemplate = "{name}_{version}_{os}_{arch}.{ext}"

// defaultChecksumAsset is the ghrelease.Render pattern used to compute the
// checksum manifest asset name, assumed to hold a GNU-coreutils-style
// checksum manifest ("<hash>  <filename>" per line). Override with
// WithChecksumAsset if yours is named differently (e.g. goreleaser's
// versioned "{name}_{version}_checksums.txt").
const defaultChecksumAsset = "checksums.txt"

// defaultTemplateMode is used for both asset-name and checksum-name
// rendering unless overridden with WithTemplateMode. Permissive matches
// ghreleases' own default and keeps a custom template with an unrecognized
// token from hard-failing; WithTemplateMode(TemplateStrict) trades that for
// surfacing template typos immediately instead of producing a silently-wrong
// asset name that only fails later as "asset not found".
const defaultTemplateMode = TemplatePermissive

// Source implements binup.Source against a single "owner/repo" (or GitHub URL
// — see ghrelease.ParseSource) backed by GitHub Releases.
type Source struct {
	parseErr         error
	manifestVerifier SignatureVerifier
	artifactVerifier SignatureVerifier
	client           *ghrelease.Client
	httpClient       *http.Client
	extFunc          func(goos string) string
	binaryName       func(goos string) string
	appName          string
	owner            string
	repoName         string
	repo             string
	assetTemplate    string
	checksumAsset    string
	token            string
	tagPrefix        string
	extractFormat    ArchiveFormat
	templateMode     TemplateMode

	allowNoChecksum     bool
	prereleases         bool
	manifestVerifierSet bool
	artifactVerifierSet bool
}

// Option configures the github Source.
type Option func(*Source)

// WithToken sets a GitHub API token, avoiding anonymous rate limits. If never
// set, ghreleases falls back to the GITHUB_TOKEN environment variable.
func WithToken(token string) Option {
	return func(s *Source) { s.token = token }
}

// WithHTTPClient overrides the *http.Client ghreleases uses for all requests
// (asset/checksum downloads included), for custom transports, proxies, or
// timeouts. Combine with WithToken if you need both; WithToken alone is
// enough for the common case.
func WithHTTPClient(hc *http.Client) Option {
	return func(s *Source) { s.httpClient = hc }
}

// WithAppName overrides the "{name}" asset-template variable and the default
// expected binary file name inside the release archive. Defaults to the repo
// name (the part of "owner/repo" after the slash).
func WithAppName(name string) Option {
	return func(s *Source) { s.appName = name }
}

// WithAssetTemplate overrides the ghrelease.Render pattern used to compute the
// release asset name for the current platform. See ghrelease.Render for the
// supported {variable} / {variable|modifier} syntax. Default:
// "{name}_{version}_{os}_{arch}.{ext}".
func WithAssetTemplate(tmpl string) Option {
	return func(s *Source) { s.assetTemplate = tmpl }
}

// WithTemplateMode overrides the TemplateMode used to render both the
// asset-name and checksum-asset-name templates. Default TemplatePermissive;
// pass TemplateStrict to fail fast on an unrecognized {variable} instead of
// silently keeping it as literal text.
func WithTemplateMode(mode TemplateMode) Option {
	return func(s *Source) { s.templateMode = mode }
}

// WithExtFunc overrides how the "{ext}" asset-template variable is computed
// from runtime.GOOS. The default maps "windows" to "zip" and everything else
// to "tar.gz".
func WithExtFunc(f func(goos string) string) Option {
	return func(s *Source) { s.extFunc = f }
}

// WithBinaryName overrides the file name looked up inside the downloaded
// archive (ghrelease.ExtractOptions.ExtractPath), keyed by runtime.GOOS. The
// default is the app name, with ".exe" appended on Windows. Only needed when
// the binary inside the archive isn't named after the app.
func WithBinaryName(f func(goos string) string) Option {
	return func(s *Source) { s.binaryName = f }
}

// WithExtractFormat forces the archive format used to extract the binary,
// overriding filename-suffix auto-detection. Only needed when your asset
// naming doesn't carry a recognizable extension (see the Format* constants
// for supported values).
func WithExtractFormat(format ArchiveFormat) Option {
	return func(s *Source) { s.extractFormat = format }
}

// WithChecksumAsset overrides the ghrelease.Render pattern used to compute
// the checksum manifest asset name, expected to hold a GNU-coreutils-style
// checksum manifest. Supports the same {name}/{version}/{os}/{arch}/{ext}
// template syntax as WithAssetTemplate — e.g. goreleaser's versioned
// "{name}_{version}_checksums.txt". Default "checksums.txt".
func WithChecksumAsset(pattern string) Option {
	return func(s *Source) { s.checksumAsset = pattern }
}

// WithoutChecksumVerification disables the checksum-manifest lookup, letting
// Fetch return the downloaded bytes unverified.
//
// DANGEROUS: this violates the Source security contract (see ../source.go) —
// binup treats whatever Fetch returns as trusted and swaps it directly into
// the running binary. Only reach for this if your release process genuinely
// publishes no checksum manifest and you accept the supply-chain risk.
func WithoutChecksumVerification() Option {
	return func(s *Source) { s.allowNoChecksum = true }
}

// WithTagPrefix restricts release lookup to tags starting with prefix, for
// repos that publish multiple release lines under different prefixes (e.g.
// "cli/v1.2.3" alongside "v1.2.3" for a library in the same repo — see
// ghrelease.WithTagPrefix). Empty (the default) preserves plain "latest
// release" behavior.
func WithTagPrefix(prefix string) Option {
	return func(s *Source) { s.tagPrefix = prefix }
}

// WithPrereleases makes Latest consider pre-releases (e.g. v1.2.0-rc1) and
// return the highest semver release, rather than GitHub's "latest" (which is
// always stable). Drafts are still ignored and non-semver tags are skipped.
// Pre-release assets go through the same checksum and signature verification
// as stable ones. Off by default.
func WithPrereleases() Option {
	return func(s *Source) { s.prereleases = true }
}

// WithManifestVerifier authenticates the checksum manifest against a detached
// signature before any hash in it is trusted, so a compromised release or
// maintainer token can no longer swap both the binary and its checksums. The
// release must carry the signature asset named by v.SignatureName; a missing
// or invalid signature fails Fetch (ErrSignatureMissing / ErrSignatureInvalid)
// and never falls back to the unsigned checksums. A nil verifier is an error
// (ErrInvalidVerifier), never a silent downgrade. Off by default.
//
// A verified manifest is mandatory: WithoutChecksumVerification does not
// relax it, so a release with no checksum manifest, or no entry for the asset,
// fails rather than being fetched unverified.
func WithManifestVerifier(v SignatureVerifier) Option {
	return func(s *Source) { s.manifestVerifier, s.manifestVerifierSet = v, true }
}

// WithArtifactVerifier authenticates the downloaded release asset itself
// against a detached signature, for projects that sign their archives rather
// than (or as well as) a checksum manifest. Independent of
// WithManifestVerifier: use either or both. A nil verifier is an error
// (ErrInvalidVerifier). Off by default.
func WithArtifactVerifier(v SignatureVerifier) Option {
	return func(s *Source) { s.artifactVerifier, s.artifactVerifierSet = v, true }
}

// New builds a github-backed Source for "owner/repo". Parsing errors surface
// from the first call to Latest or Fetch, not from New, so it's safe to call
// at package-init time with a literal.
func New(repo string, opts ...Option) *Source {
	s := &Source{repo: repo, templateMode: defaultTemplateMode}
	s.owner, s.repoName, s.parseErr = ghrelease.ParseSource(repo)

	for _, opt := range opts {
		opt(s)
	}

	if s.appName == "" {
		s.appName = s.repoName
	}
	if s.client == nil {
		s.client = s.newClient()
	}
	if s.assetTemplate == "" {
		s.assetTemplate = defaultAssetTemplate
	}
	if s.checksumAsset == "" {
		s.checksumAsset = defaultChecksumAsset
	}
	if s.extFunc == nil {
		s.extFunc = defaultExt
	}
	if s.binaryName == nil {
		appName := s.appName
		s.binaryName = func(goos string) string {
			if goos == "windows" {
				return appName + ".exe"
			}
			return appName
		}
	}
	return s
}

// newClient builds the ghreleases client from the Source's options. It is the
// single place verifiers are wired into the client, so tests that need a
// redirected transport go through it rather than constructing their own.
func (s *Source) newClient() *ghrelease.Client {
	var copts []ghrelease.Option
	// Wire on "was set", not "is non-nil": a nil verifier must reach the
	// client, which fails closed with ErrInvalidVerifier rather than
	// silently dropping to unverified.
	if s.manifestVerifierSet {
		copts = append(copts, ghrelease.WithManifestVerifier(s.manifestVerifier))
	}
	if s.artifactVerifierSet {
		copts = append(copts, ghrelease.WithArtifactVerifier(s.artifactVerifier))
	}
	if s.httpClient != nil {
		return ghrelease.NewClientWithHTTP(s.httpClient, s.token, copts...)
	}
	return ghrelease.NewClient(s.token, copts...)
}

func defaultExt(goos string) string {
	if goos == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// Latest returns the newest release's metadata without downloading the binary.
// Notes, Prerelease and PublishedAt come from the same single request that
// resolves the version.
func (s *Source) Latest(ctx context.Context) (*binup.Release, error) {
	if s.parseErr != nil {
		return nil, fmt.Errorf("binup/github: %w", s.parseErr)
	}

	if s.tagPrefix != "" {
		ctx = ghrelease.WithTagPrefix(ctx, s.tagPrefix)
	}

	if s.prereleases {
		ctx = ghrelease.WithPrereleases(ctx, true)
	}

	rel, err := s.client.LatestReleaseInfo(ctx, s.owner, s.repoName)
	if err != nil {
		return nil, fmt.Errorf("binup/github: latest release for %s: %w", s.repo, err)
	}

	return &binup.Release{
		Version:     rel.TagName,
		Notes:       rel.Body,
		Prerelease:  rel.Prerelease,
		PublishedAt: rel.PublishedAt,
	}, nil
}

// Fetch downloads, verifies (checksum, via ghreleases), and extracts the
// binary matching the current platform, returning it as a stream. binup
// treats the returned bytes as trusted, so verification happens here
// (verify-then-extract) unless WithoutChecksumVerification was set.
func (s *Source) Fetch(ctx context.Context, rel *binup.Release) (io.ReadCloser, error) {
	if s.parseErr != nil {
		return nil, fmt.Errorf("binup/github: %w", s.parseErr)
	}
	if rel == nil {
		return nil, fmt.Errorf("binup/github: Fetch called with nil Release")
	}

	// binup.Release only carries Version/Notes, not source-specific asset
	// metadata, so re-fetch the release by tag to get its asset list.
	ghRel, err := s.client.GetRelease(ctx, s.owner, s.repoName, rel.Version)
	if err != nil {
		return nil, fmt.Errorf("binup/github: get release %s: %w", rel.Version, err)
	}

	tmplVars := ghrelease.TemplateVars{
		Name:    s.appName,
		Version: rel.Version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Ext:     s.extFunc(runtime.GOOS),
	}

	assetName, err := ghrelease.Render(s.assetTemplate, tmplVars, s.templateMode)
	if err != nil {
		return nil, fmt.Errorf("binup/github: render asset name: %w", err)
	}

	assetURL, err := findAssetURL(ghRel.Assets, assetName)
	if err != nil {
		return nil, fmt.Errorf("binup/github: %w", err)
	}

	expectedHash, err := s.expectedHash(ctx, ghRel, assetName, tmplVars)
	if err != nil {
		return nil, err
	}

	dlOpts := ghrelease.DownloadOptions{ExpectedHash: expectedHash, Context: ctx}
	if s.artifactVerifier != nil {
		var sigURL string
		if sigURL, err = signatureURL(ghRel, s.artifactVerifier, assetName); err != nil {
			return nil, err
		}
		dlOpts.SignatureURL = sigURL
	}

	data, _, err := s.client.DownloadToBytes(assetURL, dlOpts)
	if err != nil {
		return nil, fmt.Errorf("binup/github: download %s: %w", assetName, err)
	}

	binary, err := ghrelease.Extract(data, assetName, ghrelease.ExtractOptions{
		ExtractPath: s.binaryName(runtime.GOOS),
		Format:      s.extractFormat,
	})
	if err != nil {
		return nil, fmt.Errorf("binup/github: extract %s from %s: %w", s.binaryName(runtime.GOOS), assetName, err)
	}

	return io.NopCloser(bytes.NewReader(binary)), nil
}

// expectedHash renders the checksum-asset pattern and looks up assetName in
// that release asset's checksum manifest. An empty return with a nil error
// means "skip verification", which only happens when WithoutChecksumVerification
// was set.
func (s *Source) expectedHash(ctx context.Context, rel *ghrelease.Release, assetName string, tmplVars ghrelease.TemplateVars) (string, error) {
	checksumAssetName, err := ghrelease.Render(s.checksumAsset, tmplVars, s.templateMode)
	if err != nil {
		return "", fmt.Errorf("binup/github: render checksum asset name: %w", err)
	}

	// WithoutChecksumVerification never relaxes a manifest verifier.
	lenient := s.allowNoChecksum && !s.manifestVerifierSet

	checksumURL, err := findAssetURL(rel.Assets, checksumAssetName)
	if err != nil {
		if lenient {
			return "", nil
		}
		return "", fmt.Errorf(
			"binup/github: no checksum manifest asset %q (required by the Source security "+
				"contract; see WithoutChecksumVerification if this is intentional): %w",
			checksumAssetName, err,
		)
	}

	if s.manifestVerifier != nil {
		var sigURL string
		if sigURL, err = signatureURL(rel, s.manifestVerifier, checksumAssetName); err != nil {
			return "", err
		}
		ctx = ghrelease.WithSignatureURL(ctx, sigURL)
	}

	checksums, err := s.client.FetchChecksums(ctx, checksumURL)
	if err != nil {
		return "", fmt.Errorf("binup/github: fetch checksum manifest %s: %w", checksumAssetName, err)
	}

	hash, ok := checksums[assetName]
	if !ok {
		if lenient {
			return "", nil
		}
		return "", fmt.Errorf("binup/github: checksum manifest %s has no entry for %q", checksumAssetName, assetName)
	}
	return hash, nil
}

// signatureURL finds the detached-signature asset for assetName in the
// release. GitHub API asset URLs are opaque (they end in a numeric ID), so
// ghreleases cannot derive the signature's URL from the asset's; it has to
// come from the release's asset list. A release without it wraps
// ErrSignatureMissing so enabling a verifier can never degrade to unverified.
func signatureURL(rel *ghrelease.Release, v SignatureVerifier, assetName string) (string, error) {
	sigName := v.SignatureName(assetName)
	url, err := findAssetURL(rel.Assets, sigName)
	if err != nil {
		return "", fmt.Errorf("binup/github: release %s has no signature asset %q for %s: %w",
			rel.TagName, sigName, assetName, ErrSignatureMissing)
	}
	return url, nil
}

func findAssetURL(assets []ghrelease.Asset, name string) (string, error) {
	for _, a := range assets {
		if a.Name == name {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("%w: %q (checked %d assets)", ghrelease.ErrAssetNotFound, name, len(assets))
}

// Compile-time assertion that *Source satisfies binup.Source.
var _ binup.Source = (*Source)(nil)
