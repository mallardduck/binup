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
	fr := &fakeRelease{assetName: assetName, binContent: binContent, archive: archive}

	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	})
	mux.HandleFunc("/repos/owner/repo/releases/tags/v1.2.3", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{
			{"name": assetName, "url": fr.server.URL + "/assets/" + assetName, "size": len(archive)},
		}
		if !omitChecksums {
			assets = append(assets, map[string]any{
				"name": checksumName, "url": fr.server.URL + "/assets/" + checksumName, "size": len(checksums),
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
		_, _ = w.Write(checksums)
	})

	fr.server = httptest.NewServer(mux)
	t.Cleanup(fr.server.Close)
	return fr
}

func (fr *fakeRelease) newSourceClient(s *Source) {
	s.client = ghrelease.NewClientWithHTTP(&http.Client{Transport: redirectGitHubAPI(fr.server.URL)}, "test-token")
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
