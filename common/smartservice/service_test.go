package smartservice

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/stretchr/testify/require"
)

func TestUpdateModelRejectsInvalidModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not a LightGBM model"))
	}))
	defer server.Close()

	modelPath := filepath.Join(t.TempDir(), "Model.bin")
	require.NoError(t, os.WriteFile(modelPath, []byte("current model"), 0o644))
	service := &Service{
		ctx:        context.Background(),
		modelPath:  modelPath,
		modelURL:   server.URL,
		httpClient: server.Client(),
	}

	require.Error(t, service.UpdateModel(context.Background()))
	content, err := os.ReadFile(modelPath)
	require.NoError(t, err)
	require.Equal(t, []byte("current model"), content)
}

func TestEnabledModelDownloadsInitiallyWithoutAutoUpdate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte("not a LightGBM model"))
	}))
	defer server.Close()

	service := &Service{
		ctx:        context.Background(),
		modelPath:  filepath.Join(t.TempDir(), "Model.bin"),
		modelURL:   server.URL,
		httpClient: server.Client(),
	}
	service.EnableModel()
	service.updateInitialModel(context.Background())

	require.Equal(t, int32(1), requests.Load())
}

func TestDownloadRejectsOversizedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("abc"))
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client()}
	_, err := service.download(context.Background(), server.URL, 2)
	require.ErrorContains(t, err, "maximum size")
}

func TestUpdateModelRejectsConcurrentUpdate(t *testing.T) {
	service := new(Service)
	service.modelUpdateAccess.Lock()
	defer service.modelUpdateAccess.Unlock()

	require.ErrorIs(t, service.UpdateModel(context.Background()), ErrModelUpdateInProgress)
}

func TestASNSourceDefaultsAndOverrides(t *testing.T) {
	defaults := NewService(context.Background(), nil, option.SmartOptions{})
	require.Equal(t, "MetaCubeX/meta-rules-dat", defaults.asnRepository)
	require.Equal(t, "sing", defaults.asnBranch)
	require.Equal(t, "asn", defaults.asnAssetPath)

	custom := NewService(context.Background(), nil, option.SmartOptions{ASN: option.SmartASNOptions{
		Repository: "example/rules",
		Branch:     "stable",
		AssetPath:  "sing/asn",
	}})
	require.Equal(t, "example/rules", custom.asnRepository)
	require.Equal(t, "stable", custom.asnBranch)
	require.Equal(t, "sing/asn", custom.asnAssetPath)
}

func TestASNAssetPath(t *testing.T) {
	require.True(t, isASNAssetPath("asn/AS1.srs", "asn"))
	require.True(t, isASNAssetPath("sing/asn/AS64512.srs", "sing/asn"))
	require.False(t, isASNAssetPath("asn/AS1.json", "asn"))
	require.False(t, isASNAssetPath("other/AS1.srs", "asn"))
	asn, loaded := asnAssetNumber(filepath.Base("asn/AS64512.srs"))
	require.True(t, loaded)
	require.Equal(t, "AS64512", asn)
}

func TestUpdateASNExtractsCompleteArchive(t *testing.T) {
	archive := smartASNArchive(t, map[string][]byte{
		"asn/AS64512.srs":        smartSRS(t, "203.0.113.0/24"),
		"asn/AS64513.srs":        smartSRS(t, "203.0.113.128/25"),
		"asn/AS64514.json":       []byte(`{"version": 1}`),
		"asn/nested/AS64515.srs": smartSRS(t, "198.51.100.0/24"),
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/rules/git/ref/heads/sing":
			_, _ = writer.Write([]byte(`{"object":{"sha":"test-revision","type":"commit"}}`))
		case "/repos/example/rules/tarball/test-revision":
			writer.Header().Set("Content-Type", "application/x-gzip")
			_, _ = writer.Write(archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	service := &Service{
		ctx:           context.Background(),
		asnPath:       t.TempDir(),
		asnRepository: "example/rules",
		asnBranch:     "sing",
		asnAssetPath:  "asn",
		asnHTTPClient: server.Client(),
		githubAPIURL:  server.URL,
	}
	require.Empty(t, service.LookupASN(netip.MustParseAddr("203.0.113.130")))
	service.updateASN(context.Background())
	require.Equal(t, "AS64513", service.LookupASN(netip.MustParseAddr("203.0.113.130")))
	require.Equal(t, "AS64512", service.LookupASN(netip.MustParseAddr("203.0.113.1")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("198.51.100.1")))

	content, err := os.ReadFile(filepath.Join(service.asnPath, "manifest.json"))
	require.NoError(t, err)
	var manifest asnManifest
	require.NoError(t, json.Unmarshal(content, &manifest))
	require.Equal(t, []asnAsset{{Path: "asn/AS64512.srs"}, {Path: "asn/AS64513.srs"}}, manifest.Files)
	require.Equal(t, "test-revision", service.index.Load().revision)
}

func TestUpdateASNRetainsLastCompleteSnapshot(t *testing.T) {
	archives := map[string][]byte{
		"first":  smartASNArchive(t, map[string][]byte{"asn/AS64512.srs": smartSRS(t, "203.0.113.0/24")}),
		"second": smartASNArchive(t, map[string][]byte{"asn/AS64513.srs": []byte("invalid SRS")}),
	}
	revision := "first"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/rules/git/ref/heads/sing":
			_, _ = writer.Write([]byte(`{"object":{"sha":"` + revision + `","type":"commit"}}`))
		case "/repos/example/rules/tarball/first", "/repos/example/rules/tarball/second":
			_, _ = writer.Write(archives[revision])
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	service := &Service{
		ctx:           context.Background(),
		asnPath:       t.TempDir(),
		asnRepository: "example/rules",
		asnBranch:     "sing",
		asnAssetPath:  "asn",
		asnHTTPClient: server.Client(),
		githubAPIURL:  server.URL,
	}
	service.updateASN(context.Background())
	require.Equal(t, "AS64512", service.LookupASN(netip.MustParseAddr("203.0.113.1")))

	revision = "second"
	service.updateASN(context.Background())
	require.Equal(t, "AS64512", service.LookupASN(netip.MustParseAddr("203.0.113.1")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("198.51.100.1")))
	require.Equal(t, "first", service.index.Load().revision)
	require.DirExists(t, filepath.Join(service.asnPath, "snapshots", "first"))
}

func TestUpdateASNPrunesSnapshotsAfterSourceChange(t *testing.T) {
	archives := map[string][]byte{
		"example":     smartASNArchive(t, map[string][]byte{"asn/AS64512.srs": smartSRS(t, "203.0.113.0/24")}),
		"replacement": smartASNArchive(t, map[string][]byte{"asn/AS64513.srs": smartSRS(t, "198.51.100.0/24")}),
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/rules/git/ref/heads/sing":
			_, _ = writer.Write([]byte(`{"object":{"sha":"shared","type":"commit"}}`))
		case "/repos/example/rules/tarball/shared":
			_, _ = writer.Write(archives["example"])
		case "/repos/replacement/rules/git/ref/heads/sing":
			_, _ = writer.Write([]byte(`{"object":{"sha":"shared","type":"commit"}}`))
		case "/repos/replacement/rules/tarball/shared":
			_, _ = writer.Write(archives["replacement"])
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	service := &Service{
		ctx:           context.Background(),
		asnPath:       t.TempDir(),
		asnRepository: "example/rules",
		asnBranch:     "sing",
		asnAssetPath:  "asn",
		asnHTTPClient: server.Client(),
		githubAPIURL:  server.URL,
	}
	service.updateASN(context.Background())
	require.NoError(t, os.MkdirAll(filepath.Join(service.asnPath, "snapshots", "stale"), 0o755))
	service.asnRepository = "replacement/rules"
	service.updateASN(context.Background())

	require.Empty(t, service.LookupASN(netip.MustParseAddr("203.0.113.1")))
	require.Equal(t, "AS64513", service.LookupASN(netip.MustParseAddr("198.51.100.1")))
	require.NoDirExists(t, filepath.Join(service.asnPath, "snapshots", "stale"))
	require.DirExists(t, filepath.Join(service.asnPath, "snapshots", "shared"))
	content, err := os.ReadFile(filepath.Join(service.asnPath, "manifest.json"))
	require.NoError(t, err)
	var manifest asnManifest
	require.NoError(t, json.Unmarshal(content, &manifest))
	require.Equal(t, "replacement/rules", manifest.Source)
}

func smartSRS(t *testing.T, prefix string) []byte {
	t.Helper()
	var output bytes.Buffer
	require.NoError(t, srs.Write(&output, option.PlainRuleSet{Rules: []option.HeadlessRule{{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultHeadlessRule{
			IPCIDR: badoption.Listable[string]{prefix},
		},
	}}}, C.RuleSetVersionCurrent))
	return output.Bytes()
}

func smartASNArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	archive := tar.NewWriter(gzipWriter)
	for name, content := range files {
		require.NoError(t, archive.WriteHeader(&tar.Header{Name: "example-rules-test/" + name, Mode: 0o644, Size: int64(len(content))}))
		_, err := archive.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	require.NoError(t, gzipWriter.Close())
	return output.Bytes()
}
