package smartservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/option"
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
	require.Equal(t, defaultASNURL, defaults.asnURL)
	require.Equal(t, defaultASNInterval, defaults.asnInterval)
	require.True(t, filepath.IsAbs(defaults.asnPath))
	require.True(t, strings.HasSuffix(defaults.asnPath, filepath.FromSlash(defaultASNPath)))

	custom := NewService(context.Background(), nil, option.SmartOptions{ASN: option.SmartASNOptions{
		URL:  "https://example.com/asn.mmdb",
		Path: "smart/custom/GeoLite2-ASN.mmdb",
	}})
	require.Equal(t, "https://example.com/asn.mmdb", custom.asnURL)
	require.True(t, strings.HasSuffix(custom.asnPath, filepath.FromSlash("smart/custom/GeoLite2-ASN.mmdb")))
}

func TestLoadASNMirror(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	db := buildTestMMDB([]testASNNet{{prefix: netip.MustParsePrefix("1.0.0.0/24"), asn: 13335}})
	require.NoError(t, os.WriteFile(path, db, 0o644))
	require.NoError(t, os.WriteFile(path+".etag", []byte("deadbeef"), 0o644))

	service := &Service{ctx: context.Background(), asnPath: path}
	require.NoError(t, service.loadASNMirror())
	require.Equal(t, "AS13335", service.LookupASN(netip.MustParseAddr("1.0.0.1")))
	require.Equal(t, "AS13335", service.LookupASN(netip.MustParseAddr("1.0.0.255")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("1.0.1.1")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("8.8.8.8")))
	require.Equal(t, "deadbeef", service.asnEtag)
}

func TestLoadASNMirrorRejectsInvalidDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	require.NoError(t, os.WriteFile(path, []byte("not a mmdb"), 0o644))
	service := &Service{ctx: context.Background(), asnPath: path}
	require.Error(t, service.loadASNMirror())
	require.Empty(t, service.LookupASN(netip.MustParseAddr("1.0.0.1")))
}

func TestUpdateASNDownloadsAndSwaps(t *testing.T) {
	first := buildTestMMDB([]testASNNet{{prefix: netip.MustParsePrefix("1.0.0.0/24"), asn: 13335}})
	second := buildTestMMDB([]testASNNet{{prefix: netip.MustParsePrefix("8.8.8.0/24"), asn: 424242}})
	var downloads atomic.Int32
	var current atomic.Value
	current.Store(first)
	var etag atomic.Value
	etag.Store(`"v1"`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") == etag.Load().(string) {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		downloads.Add(1)
		writer.Header().Set("Etag", etag.Load().(string))
		_, _ = writer.Write(current.Load().([]byte))
	}))
	t.Cleanup(server.Close)

	service := &Service{
		ctx:           context.Background(),
		asnPath:       filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb"),
		asnURL:        server.URL,
		asnHTTPClient: server.Client(),
	}
	require.Empty(t, service.LookupASN(netip.MustParseAddr("1.0.0.1")))
	service.updateASN(context.Background())
	require.Equal(t, "AS13335", service.LookupASN(netip.MustParseAddr("1.0.0.1")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("8.8.8.8")))
	require.Equal(t, int32(1), downloads.Load())
	require.FileExists(t, service.asnPath)

	// unchanged etag: 304, no re-download
	service.updateASN(context.Background())
	require.Equal(t, int32(1), downloads.Load())

	// upstream change triggers a swap
	current.Store(second)
	etag.Store(`"v2"`)
	service.updateASN(context.Background())
	require.Equal(t, "AS424242", service.LookupASN(netip.MustParseAddr("8.8.8.8")))
	require.Empty(t, service.LookupASN(netip.MustParseAddr("1.0.0.1")))
	require.Equal(t, int32(2), downloads.Load())
	require.FileExists(t, service.asnPath)
}

func TestUpdateASNPreservesExistingOnHTTPError(t *testing.T) {
	valid := buildTestMMDB([]testASNNet{{prefix: netip.MustParsePrefix("1.0.0.0/24"), asn: 13335}})
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(writer, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write(valid)
	}))
	t.Cleanup(server.Close)

	service := &Service{
		ctx:           context.Background(),
		asnPath:       filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb"),
		asnURL:        server.URL,
		asnHTTPClient: server.Client(),
	}
	service.updateASN(context.Background())
	require.Equal(t, "AS13335", service.LookupASN(netip.MustParseAddr("1.0.0.1")))

	fail.Store(true)
	service.updateASN(context.Background())
	require.Equal(t, "AS13335", service.LookupASN(netip.MustParseAddr("1.0.0.1")))
	require.FileExists(t, service.asnPath)
}

type testASNNet struct {
	prefix netip.Prefix
	asn    uint32
}

// buildTestMMDB assembles a minimal MaxMind-DB file: IPv6 search tree with an
// IPv4 subnet below ::/96, one data map per network ("autonomous_system_number"),
// and a GeoLite2-ASN metadata section. Only non-overlapping IPv4 prefixes are
// supported, matching the reader's "longest prefix wins" walk.
func buildTestMMDB(records []testASNNet) []byte {
	maxDepth := 0
	for _, record := range records {
		prefix := record.prefix.Masked()
		if !prefix.Addr().Is4() || prefix.Bits() == 0 {
			panic("test fixtures support only non-empty IPv4 prefixes")
		}
		if prefix.Bits() > maxDepth {
			maxDepth = prefix.Bits()
		}
	}

	// search tree: nodes 0..95 follow ::/96 through left branches so the
	// reader's ipv4Start lands on node 96, the IPv4 root.
	tree := make([]uint64, (96+maxDepth)*2)
	for node := 0; node < 96; node++ {
		tree[node*2] = uint64(node + 1)
	}
	// IPv4 root (node 96) and any allocated node start with empty records.
	empty := uint64(0) // placeholder, replaced after nodeCount is known
	allocate := func() uint64 {
		node := uint64(len(tree) / 2)
		tree = append(tree, empty, empty)
		return node
	}
	type dataEntry struct {
		prefix netip.Prefix
		asn    uint32
	}
	var entries []dataEntry
	walk := func(record testASNNet) {
		prefix := record.prefix.Masked()
		addr := prefix.Addr().As4()
		node := uint64(96)
		for bit := 0; bit < prefix.Bits()-1; bit++ {
			b := (addr[bit/8] >> (7 - bit%8)) & 1
			index := node*2 + uint64(b)
			child := tree[index]
			if child == 0 {
				child = allocate()
				tree[index] = child
			}
			node = child
		}
		entries = append(entries, dataEntry{prefix: prefix, asn: record.asn})
	}
	for _, record := range records {
		walk(record)
	}
	nodeCount := uint64(len(tree) / 2)

	// data section: an empty record plus one per network, aligned to 6 bytes
	emptyRecord := []byte{0xE0}
	offsets := make([]uint64, 0, len(entries)+1)
	dataSize := uint64(0)
	appendData := func(entry []byte) uint64 {
		aligned := (len(entry) + 5) / 6 * 6
		offset := dataSize
		dataSize += uint64(aligned)
		offsets = append(offsets, offset)
		return offset
	}
	appendData(emptyRecord)
	dataPtrs := make([]uint64, len(entries))
	for index, entry := range entries {
		entryBytes := encodeASNMap(entry.asn)
		dataPtrs[index] = nodeCount + 16 + appendData(entryBytes)
	}
	dataSection := make([]byte, dataSize)
	copy(dataSection[offsets[0]:], emptyRecord)
	for index, entry := range entries {
		copy(dataSection[offsets[index+1]:], encodeASNMap(entry.asn))
	}

	// second pass: fill data pointers and convert placeholders to empty records
	for index, record := range records {
		prefix := record.prefix.Masked()
		addr := prefix.Addr().As4()
		node := uint64(96)
		for bit := 0; bit < prefix.Bits()-1; bit++ {
			b := (addr[bit/8] >> (7 - bit%8)) & 1
			child := tree[node*2+uint64(b)]
			if child == 0 || child >= nodeCount {
				child = nodeCount
			}
			node = child
		}
		b := (addr[(prefix.Bits()-1)/8] >> (7 - (prefix.Bits()-1)%8)) & 1
		tree[node*2+uint64(b)] = dataPtrs[index]
	}
	for index := range tree {
		if tree[index] == 0 {
			tree[index] = nodeCount
		}
	}

	recordBytes := make([]byte, 0, len(tree)*3)
	for _, record := range tree {
		recordBytes = append(recordBytes, byte(record>>16), byte(record>>8), byte(record))
	}

	// the reader expects the search tree at offset 0, followed by the 16-byte
	// data section separator, the data section, and the metadata marker.
	out := append([]byte{}, recordBytes...)
	out = append(out, make([]byte, 16)...) // data section separator
	out = append(out, dataSection...)
	out = append(out, []byte("\xAB\xCD\xEFMaxMind.com")...)
	out = append(out, encodeMetadata(nodeCount)...)
	return out
}

func encodeASNMap(asn uint32) []byte {
	out := []byte{0xE1} // map, one field
	out = append(out, encodeUTF8("autonomous_system_number")...)
	out = append(out, encodeUint32(asn)...)
	return out
}

func encodeMetadata(nodeCount uint64) []byte {
	fields := []struct {
		key   string
		value []byte
	}{
		{"binary_format_major_version", encodeUint16(2)},
		{"binary_format_minor_version", encodeUint16(0)},
		{"database_type", encodeUTF8("GeoLite2-ASN")},
		{"ip_version", encodeUint16(6)},
		{"node_count", encodeUint32(uint32(nodeCount))},
		{"record_size", encodeUint16(24)},
	}
	out := []byte{0xE6} // map, six fields
	for _, field := range fields {
		out = append(out, encodeUTF8(field.key)...)
		out = append(out, field.value...)
	}
	return out
}

func encodeUTF8(value string) []byte {
	out := []byte{byte(2<<5) | byte(len(value))}
	return append(out, value...)
}

func encodeUint16(value uint16) []byte {
	return []byte{byte(5<<5) | 2, byte(value >> 8), byte(value)}
}

func encodeUint32(value uint32) []byte {
	var size byte
	switch {
	case value < 1<<8:
		size = 1
	case value < 1<<16:
		size = 2
	case value < 1<<24:
		size = 3
	default:
		size = 4
	}
	out := []byte{byte(6<<5) | size}
	for index := int(size) - 1; index >= 0; index-- {
		out = append(out, byte(value>>(8*index)))
	}
	return out
}
