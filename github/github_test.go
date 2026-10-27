package github

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mallardduck/binup"
	ghrelease "github.com/mallardduck/ghreleases"
)

// --- pure helper tests (no network) ---

func TestDefaultExt(t *testing.T) {
	if got := defaultExt("windows"); got != "zip" {
		t.Fatalf("defaultExt(windows) = %q, want zip", got)
	}
	if got := defaultExt("linux"); got != "tar.gz" {
		t.Fatalf("defaultExt(linux) = %q, want tar.gz", got)
	}
}

func TestFindAssetURL(t *testing.T) {
	assets := []ghrelease.Asset{
		{Name: "a.tar.gz", URL: "https://example/a"},
		{Name: "b.tar.gz", URL: "https://example/b"},
	}
	url, err := findAssetURL(assets, "b.tar.gz")
	if err != nil {
		t.Fatalf("findAssetURL: %v", err)
	}
	if url != "https://example/b" {
		t.Fatalf("url = %q, want https://example/b", url)
	}
	if _, err := findAssetURL(assets, "missing"); err == nil {
		t.Fatal("expected an error for a missing asset")
	}
}

func TestNewDefaults(t *testing.T) {
	s := New("owner/repo")
	if s.parseErr != nil {
		t.Fatalf("parseErr = %v, want nil", s.parseErr)
	}
	if s.owner != "owner" || s.repoName != "repo" {
		t.Fatalf("owner/repoName = %q/%q, want owner/repo", s.owner, s.repoName)
	}
	if s.appName != "repo" {
		t.Fatalf("appName = %q, want repo (default: repo name)", s.appName)
	}
	if s.assetTemplate != defaultAssetTemplate {
		t.Fatalf("assetTemplate = %q, want default", s.assetTemplate)
	}
	if s.checksumAsset != defaultChecksumAsset {
		t.Fatalf("checksumAsset = %q, want default", s.checksumAsset)
	}
	if got := s.binaryName("linux"); got != "repo" {
		t.Fatalf("binaryName(linux) = %q, want repo", got)
	}
	if got := s.binaryName("windows"); got != "repo.exe" {
		t.Fatalf("binaryName(windows) = %q, want repo.exe", got)
	}
}

func TestNewOptions(t *testing.T) {
	s := New("owner/repo",
		WithAppName("myapp"),
		WithAssetTemplate("{name}-{version}.{ext}"),
		WithChecksumAsset("SHA256SUMS"),
		WithExtFunc(func(string) string { return "tgz" }),
		WithBinaryName(func(goos string) string { return "custom-" + goos }),
	)
	if s.appName != "myapp" {
		t.Fatalf("appName = %q, want myapp", s.appName)
	}
	if s.assetTemplate != "{name}-{version}.{ext}" {
		t.Fatalf("assetTemplate = %q", s.assetTemplate)
	}
	if s.checksumAsset != "SHA256SUMS" {
		t.Fatalf("checksumAsset = %q", s.checksumAsset)
	}
	if got := s.extFunc("anything"); got != "tgz" {
		t.Fatalf("extFunc override = %q, want tgz", got)
	}
	if got := s.binaryName("plan9"); got != "custom-plan9" {
		t.Fatalf("binaryName override = %q, want custom-plan9", got)
	}
}

func TestSource_ParseErrPropagates(t *testing.T) {
	s := New("not-a-valid-repo-string")
	ctx := context.Background()
	if _, err := s.Latest(ctx); err == nil {
		t.Fatal("expected Latest to return the parse error")
	}
	if _, err := s.Fetch(ctx, &binup.Release{Version: "v1.0.0"}); err == nil {
		t.Fatal("expected Fetch to return the parse error")
	}
}

// --- fully local, offline integration test of Latest + Fetch ---
//
// ghreleases hardcodes its API base URL to https://api.github.com with no
// override hook, so the release-metadata calls are redirected to a local
// httptest server via a custom http.RoundTripper. Asset and checksum URLs are
// entirely ours to control (they come from our fake release JSON), so those
// point straight at the test server without needing any redirect.

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectGitHubAPI rewrites any request to api.github.com so it lands on the
// given local test server instead, leaving path/query untouched.
func redirectGitHubAPI(serverURL string) http.RoundTripper {
	target, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	base := http.DefaultTransport
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.github.com" {
			req = req.Clone(req.Context())
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		}
		return base.RoundTrip(req)
	})
}

func buildTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildDefaultArchive builds the fake asset in whatever format defaultExt
// says the current GOOS uses (zip on Windows, tar.gz elsewhere), so the
// fixture matches what Source actually requests via {ext} — it must, since
// several tests below rely on the default asset template with no
// WithExtFunc/WithExtractFormat override.
func buildDefaultArchive(t *testing.T, name string, content []byte) (archive []byte, ext string) {
	t.Helper()
	ext = defaultExt(runtime.GOOS)
	if ext == "zip" {
		return buildZip(t, name, content), ext
	}
	return buildTarGz(t, name, content), ext
}

// fakeRelease wires up an httptest server serving a single fake GitHub
// release ("v1.2.3") for "owner/repo", with one platform archive asset
// (containing a single binary named binName with content binContent) and,
// unless omitChecksums, a checksums.txt asset covering it.
type fakeRelease struct {
	server     *httptest.Server
	assetName  string
	binContent []byte
	archive    []byte
	checksums  []byte
	// extra holds additional release assets (e.g. detached signatures) keyed
	// by asset name; set before the first request. Served and listed
	// alongside the archive and checksums.
	extra map[string][]byte
	// opaqueIDs serves every asset at /assets/by-id/<id> instead of
	// /assets/<name>, like GitHub's API, whose asset URLs end in an ID: a
	// sibling signature URL cannot be derived from the asset's.
	opaqueIDs bool

	mu       sync.Mutex
	paths    []string // request paths, in order
	authzHdr []string // Authorization header of each request
}

func assetID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:6])
}

// assetURL is the download URL advertised for the named asset.
func (fr *fakeRelease) assetURL(name string) string {
	if fr.opaqueIDs {
		return fr.server.URL + "/assets/by-id/" + assetID(name)
	}
	return fr.server.URL + "/assets/" + name
}

// requested reports whether any request path contained substr.
func (fr *fakeRelease) requested(substr string) bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for _, p := range fr.paths {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

func newFakeRelease(t *testing.T, binName string, binContent []byte, corruptChecksum, omitChecksums bool) *fakeRelease {
	t.Helper()
	return newFakeReleaseWithChecksumName(t, binName, binContent, corruptChecksum, omitChecksums, "checksums.txt")
}

func newFakeReleaseWithChecksumName(t *testing.T, binName string, binContent []byte, corruptChecksum, omitChecksums bool, checksumName string) *fakeRelease {
	t.Helper()
	archive, ext := buildDefaultArchive(t, binName, binContent)
	assetName := fmt.Sprintf("myapp_v1.2.3_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)

	sum := sha256.Sum256(archive)
	hash := hex.EncodeToString(sum[:])
	if corruptChecksum {
		hash = "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	}
	checksums := []byte(hash + "  " + assetName + "\n")

	mux := http.NewServeMux()
	fr := &fakeRelease{assetName: assetName, binContent: binContent, archive: archive, checksums: checksums, extra: map[string][]byte{}}

	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	})
	mux.HandleFunc("/repos/owner/repo/releases/tags/v1.2.3", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{
			{"name": assetName, "url": fr.assetURL(assetName), "size": len(archive)},
		}
		if !omitChecksums {
			assets = append(assets, map[string]any{
				"name": checksumName, "url": fr.assetURL(checksumName), "size": len(checksums),
			})
		}
		for name, body := range fr.extra {
			assets = append(assets, map[string]any{
				"name": name, "url": fr.assetURL(name), "size": len(body),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "tag_name": "v1.2.3", "name": "v1.2.3", "assets": assets,
		})
	})
	mux.HandleFunc("/assets/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/assets/"+checksumName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fr.checksums)
	})

	mux.HandleFunc("/assets/by-id/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/assets/by-id/")
		bodies := map[string][]byte{assetName: archive, checksumName: fr.checksums}
		for name, body := range fr.extra {
			bodies[name] = body
		}
		for name, body := range bodies {
			if assetID(name) == id {
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := fr.extra[strings.TrimPrefix(r.URL.Path, "/assets/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})

	fr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		fr.paths = append(fr.paths, r.URL.Path)
		fr.authzHdr = append(fr.authzHdr, r.Header.Get("Authorization"))
		fr.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(fr.server.Close)
	return fr
}

func (fr *fakeRelease) newSourceClient(s *Source) {
	s.httpClient = &http.Client{Transport: redirectGitHubAPI(fr.server.URL)}
	s.token = "test-token"
	s.client = s.newClient()
}

func TestSource_LatestAndFetch_LocalServer(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	content := []byte("hello from the mock binary")
	fr := newFakeRelease(t, binName, content, false, false)

	s := New("owner/repo", WithAppName("myapp"))
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.2.3" {
		t.Fatalf("Version = %q, want v1.2.3", rel.Version)
	}

	rc, err := s.Fetch(ctx, rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read fetched stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("fetched content = %q, want %q", got, content)
	}
}

func TestSource_Fetch_ChecksumMismatch(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	fr := newFakeRelease(t, binName, []byte("content"), true /* corrupt checksum */, false)

	s := New("owner/repo", WithAppName("myapp"))
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := s.Fetch(ctx, rel); err == nil {
		t.Fatal("expected Fetch to fail on a checksum mismatch")
	}
}

func TestSource_Fetch_MissingChecksumsRequiredByDefault(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	fr := newFakeRelease(t, binName, []byte("content"), false, true /* omit checksums asset */)

	s := New("owner/repo", WithAppName("myapp"))
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := s.Fetch(ctx, rel); err == nil {
		t.Fatal("expected Fetch to fail when no checksum manifest is published (security contract default)")
	}
}

func TestSource_Fetch_TemplatedChecksumAsset(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	content := []byte("hello from the mock binary")
	// Mirrors goreleaser's versioned checksum manifest naming.
	fr := newFakeReleaseWithChecksumName(t, binName, content, false, false, "myapp_v1.2.3_checksums.txt")

	s := New("owner/repo", WithAppName("myapp"), WithChecksumAsset("{name}_{version}_checksums.txt"))
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	rc, err := s.Fetch(ctx, rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read fetched stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("fetched content = %q, want %q", got, content)
	}
}

func TestSource_Fetch_WithoutChecksumVerification(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	content := []byte("content")
	fr := newFakeRelease(t, binName, content, false, true /* omit checksums asset */)

	s := New("owner/repo", WithAppName("myapp"), WithoutChecksumVerification())
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	rc, err := s.Fetch(ctx, rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read fetched stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("fetched content = %q, want %q", got, content)
	}
}

// --- config knobs beyond checksum-asset naming: HTTP client, template mode,
// tag-prefix filtering, extract format ---

func TestSource_WithHTTPClient(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	content := []byte("hello via WithHTTPClient")
	fr := newFakeRelease(t, binName, content, false, false)

	// Deliberately built via the public WithHTTPClient Option, not the
	// white-box fr.newSourceClient helper the other tests use — this
	// exercises New()'s own httpClient-based client construction.
	s := New("owner/repo", WithAppName("myapp"),
		WithHTTPClient(&http.Client{Transport: redirectGitHubAPI(fr.server.URL)}),
	)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	rc, err := s.Fetch(ctx, rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read fetched stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("fetched content = %q, want %q", got, content)
	}
}

func TestSource_WithTemplateMode_Strict(t *testing.T) {
	fr := newFakeRelease(t, "myapp", []byte("x"), false, false)

	s := New("owner/repo",
		WithAppName("myapp"),
		WithAssetTemplate("{name}_{bogus}.{ext}"), // {bogus} is not a recognized variable
		WithTemplateMode(TemplateStrict),
	)
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	_, err = s.Fetch(ctx, rel)
	if err == nil {
		t.Fatal("expected Fetch to fail fast on an unknown template variable under Strict mode")
	}
	if !errors.Is(err, ghrelease.ErrUnknownVariable) {
		t.Fatalf("err = %v, want it to wrap ghrelease.ErrUnknownVariable", err)
	}
}

func TestSource_TemplateMode_DefaultPermissive(t *testing.T) {
	fr := newFakeRelease(t, "myapp", []byte("x"), false, false)

	// Same bogus template as the Strict test above, but no WithTemplateMode —
	// default Permissive keeps {bogus} as literal text instead of erroring,
	// so this fails later as "asset not found", not at the render step.
	s := New("owner/repo", WithAppName("myapp"), WithAssetTemplate("{name}_{bogus}.{ext}"))
	fr.newSourceClient(s)

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	_, err = s.Fetch(ctx, rel)
	if err == nil {
		t.Fatal("expected Fetch to fail (asset not found) since the rendered name won't match any real asset")
	}
	if errors.Is(err, ghrelease.ErrUnknownVariable) {
		t.Fatal("Permissive mode should not surface ErrUnknownVariable — that's Strict-mode behavior")
	}
}

func TestSource_WithTagPrefix(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "tag_name": "cli/v2.0.0", "name": "cli/v2.0.0", "draft": false, "prerelease": false, "published_at": "2024-01-02T00:00:00Z", "assets": []any{}},
			{"id": 2, "tag_name": "v9.9.9", "name": "v9.9.9", "draft": false, "prerelease": false, "published_at": "2024-01-01T00:00:00Z", "assets": []any{}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := New("owner/repo", WithTagPrefix("cli/"))
	s.client = ghrelease.NewClientWithHTTP(&http.Client{Transport: redirectGitHubAPI(server.URL)}, "test-token")

	rel, err := s.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "cli/v2.0.0" {
		t.Fatalf("Version = %q, want cli/v2.0.0 (the tag matching the configured prefix)", rel.Version)
	}
}

func TestSource_WithExtractFormat(t *testing.T) {
	binName := "myapp"
	if runtime.GOOS == "windows" {
		binName = "myapp.exe"
	}
	content := []byte("hello from an oddly-named asset")
	archive := buildTarGz(t, binName, content)

	// ".bin" defeats ghreleases' filename-suffix auto-detection (it falls
	// back to FormatPlain, i.e. "don't touch it"), so without an explicit
	// WithExtractFormat override this asset would come back as raw
	// still-compressed tar.gz bytes instead of the extracted binary.
	assetName := fmt.Sprintf("myapp_v1.2.3_%s_%s.bin", runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	})
	mux.HandleFunc("/repos/owner/repo/releases/tags/v1.2.3", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "tag_name": "v1.2.3", "name": "v1.2.3",
			"assets": []map[string]any{
				{"name": assetName, "url": server.URL + "/assets/" + assetName, "size": len(archive)},
			},
		})
	})
	mux.HandleFunc("/assets/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	s := New("owner/repo",
		WithAppName("myapp"),
		WithAssetTemplate("{name}_{version}_{os}_{arch}.bin"),
		WithExtractFormat(FormatTarGz),
		WithoutChecksumVerification(),
	)
	s.client = ghrelease.NewClientWithHTTP(&http.Client{Transport: redirectGitHubAPI(server.URL)}, "test-token")

	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	rc, err := s.Fetch(ctx, rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read fetched stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("fetched content = %q, want %q (extraction should have unpacked the tar.gz despite the .bin name)", got, content)
	}
}

// --- signature verification ---

// fakeVerifier accepts content only when the signature is exactly "good". It
// reads the content to EOF (as the SignatureVerifier contract requires) and
// records what it was asked to authenticate.
type fakeVerifier struct {
	suffix  string
	content []byte
	calls   int
}

func (v *fakeVerifier) SignatureName(name string) string { return name + v.suffix }

func (v *fakeVerifier) Verify(_ context.Context, content io.Reader, sig []byte) error {
	v.calls++
	b, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	v.content = b
	if string(sig) != "good" {
		return errors.New("bad signature")
	}
	return nil
}

func testBinName() string {
	if runtime.GOOS == "windows" {
		return "myapp.exe"
	}
	return "myapp"
}

// fetchWith builds a Source over fr with opts and runs Latest then Fetch,
// returning Fetch's results.
func fetchWith(t *testing.T, fr *fakeRelease, opts ...Option) (io.ReadCloser, error) {
	t.Helper()
	s := New("owner/repo", append([]Option{WithAppName("myapp")}, opts...)...)
	fr.newSourceClient(s)
	ctx := context.Background()
	rel, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	return s.Fetch(ctx, rel)
}

func TestSource_WithManifestVerifier(t *testing.T) {
	tests := []struct {
		name        string
		sig         []byte // nil = release publishes no signature asset
		noChecksums bool   // release publishes no checksum manifest
		opts        func(v *fakeVerifier) []Option
		wantErr     error // nil = want success; errAny = any error
	}{
		{name: "valid signature", sig: []byte("good")},
		{name: "invalid signature", sig: []byte("evil"), wantErr: ErrSignatureInvalid},
		{name: "release lacks signature asset", wantErr: ErrSignatureMissing},
		{name: "nil verifier fails closed", sig: []byte("good"), wantErr: ErrInvalidVerifier,
			opts: func(*fakeVerifier) []Option { return []Option{WithManifestVerifier(nil)} }},
		// WithoutChecksumVerification must not turn a verifier into a no-op.
		{name: "signature still checked under WithoutChecksumVerification", sig: []byte("evil"), wantErr: ErrSignatureInvalid,
			opts: func(v *fakeVerifier) []Option {
				return []Option{WithManifestVerifier(v), WithoutChecksumVerification()}
			}},
		{name: "valid signature under WithoutChecksumVerification", sig: []byte("good"),
			opts: func(v *fakeVerifier) []Option {
				return []Option{WithManifestVerifier(v), WithoutChecksumVerification()}
			}},
		{name: "no manifest fails closed under WithoutChecksumVerification", sig: []byte("good"), noChecksums: true, wantErr: errAny,
			opts: func(v *fakeVerifier) []Option {
				return []Option{WithManifestVerifier(v), WithoutChecksumVerification()}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRelease(t, testBinName(), []byte("payload"), false, tc.noChecksums)
			if tc.sig != nil {
				fr.extra["checksums.txt.fakesig"] = tc.sig
			}
			v := &fakeVerifier{suffix: ".fakesig"}
			opts := []Option{WithManifestVerifier(v)}
			if tc.opts != nil {
				opts = tc.opts(v)
			}
			rc, err := fetchWith(t, fr, opts...)
			assertFetchErr(t, rc, err, tc.wantErr)
		})
	}
}

func TestSource_WithManifestVerifier_AuthenticatesTheManifest(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	fr.extra["checksums.txt.fakesig"] = []byte("good")
	v := &fakeVerifier{suffix: ".fakesig"}

	rc, err := fetchWith(t, fr, WithManifestVerifier(v))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	if v.calls != 1 {
		t.Fatalf("verifier called %d times, want 1", v.calls)
	}
	if !bytes.Equal(v.content, fr.checksums) {
		t.Fatalf("verifier saw %q, want the checksum manifest %q", v.content, fr.checksums)
	}
}

func TestSource_WithManifestVerifier_HonoursChecksumAssetTemplate(t *testing.T) {
	fr := newFakeReleaseWithChecksumName(t, testBinName(), []byte("payload"), false, false, "myapp_v1.2.3_checksums.txt")
	fr.extra["myapp_v1.2.3_checksums.txt.fakesig"] = []byte("evil")

	_, err := fetchWith(t, fr,
		WithChecksumAsset("{name}_{version}_checksums.txt"),
		WithManifestVerifier(&fakeVerifier{suffix: ".fakesig"}),
	)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("Fetch error = %v, want ErrSignatureInvalid", err)
	}
}

func TestSource_WithArtifactVerifier(t *testing.T) {
	tests := []struct {
		name    string
		sig     []byte
		opts    func(v *fakeVerifier) []Option
		wantErr error
	}{
		{"valid signature", []byte("good"), nil, nil},
		{"invalid signature", []byte("evil"), nil, ErrSignatureInvalid},
		{"release lacks signature asset", nil, nil, ErrSignatureMissing},
		{"nil verifier fails closed", []byte("good"), func(*fakeVerifier) []Option {
			return []Option{WithArtifactVerifier(nil)}
		}, ErrInvalidVerifier},
		{"works with WithoutChecksumVerification", []byte("good"), func(v *fakeVerifier) []Option {
			return []Option{WithArtifactVerifier(v), WithoutChecksumVerification()}
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
			if tc.sig != nil {
				fr.extra[fr.assetName+".fakesig"] = tc.sig
			}
			v := &fakeVerifier{suffix: ".fakesig"}
			opts := []Option{WithArtifactVerifier(v)}
			if tc.opts != nil {
				opts = tc.opts(v)
			}
			rc, err := fetchWith(t, fr, opts...)
			assertFetchErr(t, rc, err, tc.wantErr)
		})
	}
}

func TestSource_WithArtifactVerifier_AuthenticatesTheArchive(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	fr.extra[fr.assetName+".fakesig"] = []byte("good")
	v := &fakeVerifier{suffix: ".fakesig"}

	rc, err := fetchWith(t, fr, WithArtifactVerifier(v))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	if v.calls != 1 {
		t.Fatalf("verifier called %d times, want 1", v.calls)
	}
	if !bytes.Equal(v.content, fr.archive) {
		t.Fatalf("verifier saw %d bytes, want the %d-byte release archive", len(v.content), len(fr.archive))
	}
}

// Manifest and artifact verification are independent options: each must be
// enforced even when the other passes.
func TestSource_BothVerifiers(t *testing.T) {
	tests := []struct {
		name                  string
		manifestSig, assetSig string
		wantErr               error
	}{
		{"both valid", "good", "good", nil},
		{"manifest invalid", "evil", "good", ErrSignatureInvalid},
		{"artifact invalid", "good", "evil", ErrSignatureInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
			fr.extra["checksums.txt.m"] = []byte(tc.manifestSig)
			fr.extra[fr.assetName+".a"] = []byte(tc.assetSig)
			mv, av := &fakeVerifier{suffix: ".m"}, &fakeVerifier{suffix: ".a"}

			rc, err := fetchWith(t, fr, WithManifestVerifier(mv), WithArtifactVerifier(av))
			assertFetchErr(t, rc, err, tc.wantErr)
		})
	}
}

// errAny marks a table row that wants some error, without pinning which.
var errAny = errors.New("any error")

func assertFetchErr(t *testing.T, rc io.ReadCloser, err, want error) {
	t.Helper()
	switch {
	case want == nil && err != nil:
		t.Fatalf("Fetch: unexpected error: %v", err)
	case want == nil:
		rc.Close()
	case err == nil:
		rc.Close()
		t.Fatalf("Fetch succeeded, want error %v", want)
	case !errors.Is(want, errAny) && !errors.Is(err, want):
		t.Fatalf("Fetch error = %v, want errors.Is %v", err, want)
	}
}

// --- error paths and wiring ---

func TestSource_Latest_ErrorPropagates(t *testing.T) {
	// No /releases/latest route is registered on this server.
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	s := New("owner/repo", WithHTTPClient(&http.Client{Transport: redirectGitHubAPI(srv.URL)}))

	if _, err := s.Latest(context.Background()); !errors.Is(err, ghrelease.ErrReleaseNotFound) {
		t.Fatalf("Latest error = %v, want ErrReleaseNotFound", err)
	}
}

func TestSource_Fetch_NilRelease(t *testing.T) {
	if _, err := New("owner/repo").Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil Release")
	}
}

func TestSource_Fetch_UnknownTag(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	s := New("owner/repo", WithAppName("myapp"))
	fr.newSourceClient(s)

	_, err := s.Fetch(context.Background(), &binup.Release{Version: "v9.9.9"})
	if !errors.Is(err, ghrelease.ErrReleaseNotFound) {
		t.Fatalf("Fetch error = %v, want ErrReleaseNotFound", err)
	}
}

func TestSource_Fetch_BinaryNotInArchive(t *testing.T) {
	fr := newFakeRelease(t, "something-else", []byte("payload"), false, false)

	_, err := fetchWith(t, fr)
	if !errors.Is(err, ghrelease.ErrFileNotFound) {
		t.Fatalf("Fetch error = %v, want ErrFileNotFound", err)
	}
}

func TestSource_Fetch_ChecksumTemplateStrict(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)

	_, err := fetchWith(t, fr, WithTemplateMode(TemplateStrict), WithChecksumAsset("{nope}.txt"))
	if !errors.Is(err, ghrelease.ErrUnknownVariable) {
		t.Fatalf("Fetch error = %v, want ErrUnknownVariable", err)
	}
}

func TestSource_WithToken_SentToGitHub(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	s := New("owner/repo", WithToken("s3cret"),
		WithHTTPClient(&http.Client{Transport: redirectGitHubAPI(fr.server.URL)}))

	if _, err := s.Latest(context.Background()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.authzHdr) == 0 || fr.authzHdr[0] != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want %q", fr.authzHdr, "Bearer s3cret")
	}
}

// A manifest that does not list the asset cannot vouch for it.
func TestSource_Fetch_ManifestMissingEntry(t *testing.T) {
	tests := []struct {
		name    string
		opts    func(v *fakeVerifier) []Option
		wantErr bool
	}{
		{"default fails", nil, true},
		{"WithoutChecksumVerification skips", func(*fakeVerifier) []Option { return []Option{WithoutChecksumVerification()} }, false},
		// A signed manifest with no entry must not degrade to unverified.
		{"fails closed under a manifest verifier", func(v *fakeVerifier) []Option {
			return []Option{WithManifestVerifier(v), WithoutChecksumVerification()}
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
			fr.checksums = []byte(strings.Repeat("0", 64) + "  some-other-asset.tar.gz\n")
			fr.extra["checksums.txt.fakesig"] = []byte("good")

			var opts []Option
			if tc.opts != nil {
				opts = tc.opts(&fakeVerifier{suffix: ".fakesig"})
			}
			rc, err := fetchWith(t, fr, opts...)
			if tc.wantErr {
				assertFetchErr(t, rc, err, errAny)
				return
			}
			assertFetchErr(t, rc, err, nil)
		})
	}
}

// partialVerifier accepts anything after reading only the first byte.
type partialVerifier struct{ fakeVerifier }

func (v *partialVerifier) Verify(_ context.Context, content io.Reader, _ []byte) error {
	_, err := content.Read(make([]byte, 1))
	return err
}

// A verifier that returns success without reading everything authenticated
// nothing; it must be rejected, not trusted.
func TestSource_Verifier_MustReadAllContent(t *testing.T) {
	tests := []struct {
		name string
		sig  func(fr *fakeRelease) string
		opt  func(v SignatureVerifier) Option
	}{
		{"manifest", func(*fakeRelease) string { return "checksums.txt.fakesig" }, WithManifestVerifier},
		{"artifact", func(fr *fakeRelease) string { return fr.assetName + ".fakesig" }, WithArtifactVerifier},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
			fr.extra[tc.sig(fr)] = []byte("good")

			rc, err := fetchWith(t, fr, tc.opt(&partialVerifier{fakeVerifier{suffix: ".fakesig"}}))
			assertFetchErr(t, rc, err, ErrSignatureInvalid)
		})
	}
}

// Verification is opt-in: without a verifier no signature is ever requested,
// even when the release happens to publish one.
func TestSource_NoVerifier_NeverFetchesSignature(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	fr.extra["checksums.txt.fakesig"] = []byte("good")
	fr.extra[fr.assetName+".fakesig"] = []byte("good")

	rc, err := fetchWith(t, fr)
	assertFetchErr(t, rc, err, nil)
	if fr.requested(".fakesig") {
		t.Fatal("a signature asset was downloaded although no verifier is configured")
	}
}

// GitHub's API asset URLs end in an opaque ID, so a signature cannot be found
// next to its asset by name; it must be looked up in the release's asset list.
func TestSource_Verifiers_WithOpaqueAssetURLs(t *testing.T) {
	fr := newFakeRelease(t, testBinName(), []byte("payload"), false, false)
	fr.opaqueIDs = true
	fr.extra["checksums.txt.m"] = []byte("good")
	fr.extra[fr.assetName+".a"] = []byte("good")
	mv, av := &fakeVerifier{suffix: ".m"}, &fakeVerifier{suffix: ".a"}

	rc, err := fetchWith(t, fr, WithManifestVerifier(mv), WithArtifactVerifier(av))
	assertFetchErr(t, rc, err, nil)
	if mv.calls != 1 || av.calls != 1 {
		t.Fatalf("verifier calls manifest=%d artifact=%d, want 1 and 1", mv.calls, av.calls)
	}
}

// A pre-release is only offered when WithPrereleases is set; by default Latest
// sticks to the newest stable release.
func TestSource_WithPrereleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.0.0"})
	})
	mux.HandleFunc("/repos/owner/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "tag_name": "v1.1.0-rc1", "prerelease": true, "published_at": "2024-02-01T00:00:00Z", "assets": []any{}},
			{"id": 2, "tag_name": "v1.0.0", "published_at": "2024-01-01T00:00:00Z", "assets": []any{}},
			{"id": 3, "tag_name": "v2.0.0", "draft": true, "published_at": "2024-03-01T00:00:00Z", "assets": []any{}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, tc := range []struct {
		name string
		opts []Option
		want string
	}{
		{"default is stable only", nil, "v1.0.0"},
		{"opt in picks highest, skips drafts", []Option{WithPrereleases()}, "v1.1.0-rc1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("owner/repo", tc.opts...)
			s.client = ghrelease.NewClientWithHTTP(&http.Client{Transport: redirectGitHubAPI(server.URL)}, "test-token")
			rel, err := s.Latest(context.Background())
			if err != nil {
				t.Fatalf("Latest: %v", err)
			}
			if rel.Version != tc.want {
				t.Fatalf("Version = %q, want %q", rel.Version, tc.want)
			}
		})
	}
}
