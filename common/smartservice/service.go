package smartservice

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/vernesong/leaves"
)

const (
	defaultModelURL             = "https://github.com/vernesong/mihomo/releases/download/LightGBM-Model/Model.bin"
	defaultModelPath            = "smart/Model.bin"
	defaultModelInterval        = 72 * time.Hour
	defaultCollectorPath        = "smart/smart_weight_data.csv"
	defaultCollectorMaxSize     = 100 * 1024 * 1024
	defaultASNPath              = "smart/asn"
	defaultASNInterval          = 24 * time.Hour
	defaultHTTPTimeout          = 90 * time.Second
	defaultASNHTTPTimeout       = 10 * time.Minute
	defaultASNRepository        = "MetaCubeX/meta-rules-dat"
	defaultASNBranch            = "sing"
	defaultASNAssetPath         = "asn"
	asnManifestVersion          = 3
	maxASNDownloadBytes     int = 32 << 20
	maxModelDownloadBytes   int = 128 << 20
	maxASNArchiveBytes      int = 512 << 20
)

var ErrModelUpdateInProgress = errors.New("LightGBM model is updating")

// Service owns the optional model, collection file, and ASN mirror shared by
// all smart groups. It is intentionally usable before its background sync
// completes: LookupASN simply returns an empty value until an index exists.
type Service struct {
	ctx     context.Context
	logger  log.ContextLogger
	options option.SmartOptions

	modelPath        string
	modelURL         string
	modelAutoUpdate  bool
	modelInterval    time.Duration
	collectorPath    string
	collectorMaxSize uint64
	asnPath          string
	asnInterval      time.Duration
	asnRepository    string
	asnBranch        string
	asnAssetPath     string

	httpClient    *http.Client
	asnHTTPClient *http.Client
	githubAPIURL  string

	modelAccess       sync.RWMutex
	model             *leaves.Ensemble
	modelUpdateAccess sync.Mutex
	modelEnabled      atomic.Bool
	index             atomic.Pointer[asnIndex]

	collectorAccess sync.Mutex
	collectorFile   *os.File
	collectorWriter *csv.Writer

	cancel context.CancelFunc
	worker sync.WaitGroup
	closed atomic.Bool
}

var _ adapter.LifecycleService = (*Service)(nil)

func NewService(ctx context.Context, logger log.ContextLogger, options option.SmartOptions) *Service {
	modelPath := options.Model.Path
	if modelPath == "" {
		modelPath = defaultModelPath
	}
	collectorPath := options.Collector.Path
	if collectorPath == "" {
		collectorPath = defaultCollectorPath
	}
	asnPath := options.ASN.Path
	if asnPath == "" {
		asnPath = defaultASNPath
	}
	asnRepository := options.ASN.Repository
	if asnRepository == "" {
		asnRepository = defaultASNRepository
	}
	asnBranch := options.ASN.Branch
	if asnBranch == "" {
		asnBranch = defaultASNBranch
	}
	asnAssetPath := options.ASN.AssetPath
	if asnAssetPath == "" {
		asnAssetPath = defaultASNAssetPath
	}
	modelInterval := time.Duration(options.Model.UpdateInterval)
	if modelInterval == 0 {
		modelInterval = defaultModelInterval
	}
	asnInterval := time.Duration(options.ASN.UpdateInterval)
	if asnInterval == 0 {
		asnInterval = defaultASNInterval
	}
	modelURL := options.Model.DownloadURL
	if modelURL == "" {
		modelURL = defaultModelURL
	}
	collectorMaxSize := options.Collector.MaxSize
	if collectorMaxSize == 0 {
		collectorMaxSize = defaultCollectorMaxSize
	}
	return &Service{
		ctx:              ctx,
		logger:           logger,
		options:          options,
		modelPath:        absoluteSmartPath(ctx, modelPath),
		modelURL:         modelURL,
		modelAutoUpdate:  options.Model.AutoUpdate,
		modelInterval:    modelInterval,
		collectorPath:    absoluteSmartPath(ctx, collectorPath),
		collectorMaxSize: collectorMaxSize,
		asnPath:          absoluteSmartPath(ctx, asnPath),
		asnInterval:      asnInterval,
		asnRepository:    asnRepository,
		asnBranch:        asnBranch,
		asnAssetPath:     asnAssetPath,
	}
}

func absoluteSmartPath(ctx context.Context, path string) string {
	path = filemanager.BasePath(ctx, path)
	absPath, err := filepath.Abs(path)
	if err == nil {
		return absPath
	}
	return path
}

func (s *Service) Name() string { return "smart" }

// EnableModel marks the shared model as required by at least one Smart group.
func (s *Service) EnableModel() {
	s.modelEnabled.Store(true)
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if s.httpClient == nil {
		transport, err := s.resolveTransport(s.options.Model.HTTPClient)
		if err != nil {
			return err
		}
		s.httpClient = &http.Client{Transport: transport, Timeout: defaultHTTPTimeout}
	}
	if s.asnHTTPClient == nil {
		transport, err := s.resolveTransport(s.options.ASN.HTTPClient)
		if err != nil {
			return err
		}
		s.asnHTTPClient = &http.Client{Transport: transport, Timeout: defaultASNHTTPTimeout}
	}
	if err := s.loadModel(); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.warn("load Smart Model.bin: ", err)
	}
	if err := s.loadASNMirror(); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.warn("load Smart ASN mirror: ", err)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	s.worker.Add(1)
	go s.loop(ctx)
	return nil
}

func (s *Service) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.worker.Wait()
	s.collectorAccess.Lock()
	if s.collectorWriter != nil {
		s.collectorWriter.Flush()
	}
	var err error
	if s.collectorFile != nil {
		err = s.collectorFile.Close()
	}
	s.collectorWriter = nil
	s.collectorFile = nil
	s.collectorAccess.Unlock()
	if s.httpClient != nil {
		s.httpClient.CloseIdleConnections()
	}
	if s.asnHTTPClient != nil && s.asnHTTPClient != s.httpClient {
		s.asnHTTPClient.CloseIdleConnections()
	}
	return err
}

func (s *Service) resolveTransport(options *option.HTTPClientOptions) (adapter.HTTPTransport, error) {
	manager := service.FromContext[adapter.HTTPClientManager](s.ctx)
	if manager == nil {
		return nil, E.New("missing HTTP client manager")
	}
	if options != nil && !options.IsEmpty() {
		return manager.ResolveTransport(s.ctx, s.logger, *options)
	}
	transport := manager.DefaultTransport()
	if transport == nil {
		return nil, E.New("default HTTP client transport is not initialized")
	}
	return transport, nil
}

func (s *Service) loop(ctx context.Context) {
	defer s.worker.Done()
	s.update(ctx)
	modelTicker := time.NewTicker(s.modelInterval)
	asnTicker := time.NewTicker(s.asnInterval)
	defer modelTicker.Stop()
	defer asnTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-modelTicker.C:
			if s.modelEnabled.Load() && (s.modelAutoUpdate || !s.modelLoaded()) {
				s.updateModel(ctx)
			}
		case <-asnTicker.C:
			s.updateASN(ctx)
		}
	}
}

func (s *Service) update(ctx context.Context) {
	s.updateInitialModel(ctx)
	s.updateASN(ctx)
}

func (s *Service) updateInitialModel(ctx context.Context) {
	if s.modelEnabled.Load() && !s.modelLoaded() {
		s.updateModel(ctx)
	}
}

func (s *Service) modelLoaded() bool {
	s.modelAccess.RLock()
	loaded := s.model != nil
	s.modelAccess.RUnlock()
	return loaded
}

func (s *Service) loadModel() error {
	model, err := leaves.LGEnsembleFromFile(s.modelPath, false)
	if err != nil {
		return err
	}
	s.modelAccess.Lock()
	s.model = model
	s.modelAccess.Unlock()
	return nil
}

func (s *Service) updateModel(parent context.Context) {
	if err := s.UpdateModel(parent); err != nil {
		s.warn("update Smart Model.bin: ", err)
	}
}

// UpdateModel downloads, validates, and atomically publishes the LightGBM model.
func (s *Service) UpdateModel(parent context.Context) error {
	if !s.modelUpdateAccess.TryLock() {
		return ErrModelUpdateInProgress
	}
	defer s.modelUpdateAccess.Unlock()
	if s.closed.Load() || s.httpClient == nil {
		return E.New("Smart service is not ready")
	}
	ctx, cancel := context.WithTimeout(parent, defaultHTTPTimeout)
	defer cancel()
	content, err := s.download(ctx, s.modelURL, maxModelDownloadBytes)
	if err != nil {
		return E.Cause(err, "download LightGBM model")
	}
	if err = filemanager.MkdirAll(s.ctx, filepath.Dir(s.modelPath), 0o755); err == nil {
		err = filemanager.WriteFile(s.ctx, s.modelPath+".tmp", content, 0o644)
	}
	if err != nil {
		return E.Cause(err, "save LightGBM model")
	}
	model, err := leaves.LGEnsembleFromFile(s.modelPath+".tmp", false)
	if err != nil {
		return E.Cause(err, "validate LightGBM model")
	}
	err = filemanager.Rename(s.ctx, s.modelPath+".tmp", s.modelPath)
	if err == nil {
		s.modelAccess.Lock()
		s.model = model
		s.modelAccess.Unlock()
		return nil
	}
	return E.Cause(err, "publish LightGBM model")
}

func (s *Service) Predict(input smart.ModelInput) (float64, bool) {
	s.modelAccess.RLock()
	model := s.model
	s.modelAccess.RUnlock()
	if model == nil || input.Success+input.Failure < smart.DefaultMinSamples {
		return 0, false
	}
	prediction := model.PredictSingle(modelFeatures(input), 0)
	if math.IsNaN(prediction) || math.IsInf(prediction, 0) || prediction <= 0 {
		return 0, false
	}
	return prediction, true
}

func modelFeatures(input smart.ModelInput) []float64 {
	log := math.Log1p
	features := []float64{
		float64(input.Success), float64(input.Failure), log(input.ConnectTime.Seconds() * 1000), log(input.Latency.Seconds() * 1000),
		log(input.UploadMB), log(input.HistoryUploadMB), log(input.MaxUploadRateKB), log(input.HistoryMaxUploadRateKB),
		log(input.DownloadMB), log(input.HistoryDownloadMB), log(input.MaxDownloadRateKB), log(input.HistoryMaxDownloadRateKB),
		log(input.ConnectionDuration.Minutes()), log(input.HistoryConnectionDuration.Minutes()), log(time.Since(input.LastUsed).Seconds()),
		boolFeature(input.IsUDP), boolFeature(!input.IsUDP), input.LossRate, input.CumulativeLossRate,
		hashFeature(input.ASN, 500), hashFeature(input.Target, 1000), hashFeature(input.DestinationIP, 10000), 0,
		trafficRatio(input.UploadMB, input.DownloadMB), trafficDensity(input.UploadMB+input.DownloadMB, input.ConnectionDuration), 0,
		hashFeature(input.ASN, 500), hashFeature(input.Target, 1000), hashFeature(input.DestinationIP, 10000), 0,
	}
	return features
}

func boolFeature(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func hashFeature(value string, buckets uint32) float64 {
	if value == "" {
		return 0
	}
	var hash uint32 = 2166136261
	for index := range len(value) {
		hash ^= uint32(value[index])
		hash *= 16777619
	}
	return float64(hash%buckets + 1)
}

func trafficRatio(upload, download float64) float64 {
	if upload == 0 || download == 0 {
		return 0
	}
	if upload > download {
		return download / upload
	}
	return -upload / download
}

func trafficDensity(total float64, duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return math.Log1p(total / duration.Minutes())
}

func (s *Service) Collect(input smart.ModelInput, group, node string, weight float64) {
	s.collectorAccess.Lock()
	defer s.collectorAccess.Unlock()
	if s.collectorWriter == nil && !s.openCollector() {
		return
	}
	stat, err := s.collectorFile.Stat()
	if err != nil || uint64(stat.Size()) >= s.collectorMaxSize {
		return
	}
	features := modelFeatures(input)
	record := make([]string, 0, len(features)+6)
	for _, feature := range features {
		record = append(record, strconv.FormatFloat(feature, 'f', 6, 64))
	}
	record = append(record, group, node, input.ASN, input.Target, strconv.FormatFloat(weight, 'f', 6, 64), time.Now().Format(time.RFC3339))
	if err = s.collectorWriter.Write(record); err == nil {
		s.collectorWriter.Flush()
		err = s.collectorWriter.Error()
	}
	if err != nil {
		s.warn("write Smart collection: ", err)
	}
}

func (s *Service) openCollector() bool {
	if err := filemanager.MkdirAll(s.ctx, filepath.Dir(s.collectorPath), 0o755); err != nil {
		s.warn("create Smart collection directory: ", err)
		return false
	}
	file, err := filemanager.OpenFile(s.ctx, s.collectorPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.warn("open Smart collection: ", err)
		return false
	}
	writer := csv.NewWriter(file)
	stat, err := file.Stat()
	if err == nil && stat.Size() == 0 {
		header := make([]string, 30)
		for index := range header {
			header[index] = fmt.Sprintf("feature_%d", index)
		}
		header = append(header, "group", "node", "asn", "target", "weight", "timestamp")
		if err = writer.Write(header); err == nil {
			writer.Flush()
			err = writer.Error()
		}
	}
	if err != nil {
		_ = file.Close()
		s.warn("initialize Smart collection: ", err)
		return false
	}
	s.collectorFile = file
	s.collectorWriter = writer
	return true
}

type asnManifest struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"`
	Branch    string     `json:"branch"`
	AssetPath string     `json:"asset_path"`
	Revision  string     `json:"revision"`
	Files     []asnAsset `json:"files"`
}

type asnAsset struct {
	Path string `json:"path"`
}

type githubReference struct {
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

func (s *Service) loadASNMirror() error {
	manifest, err := s.readASNManifest()
	if err != nil {
		return err
	}
	if !s.validASNManifest(manifest) {
		return E.New("invalid Smart ASN manifest")
	}
	index, err := s.buildASNIndex(filepath.Join(s.asnPath, "snapshots", manifest.Revision), manifest.Files)
	if err != nil {
		return err
	}
	index.revision = manifest.Revision
	s.index.Store(index)
	return nil
}

func (s *Service) readASNManifest() (asnManifest, error) {
	content, err := filemanager.ReadFile(s.ctx, filepath.Join(s.asnPath, "manifest.json"))
	if err != nil {
		return asnManifest{}, err
	}
	var manifest asnManifest
	if err = json.Unmarshal(content, &manifest); err != nil {
		return asnManifest{}, err
	}
	return manifest, nil
}

func (s *Service) validASNManifest(manifest asnManifest) bool {
	return manifest.Version == asnManifestVersion && manifest.Source == s.asnRepository && manifest.Branch == s.asnBranch && manifest.AssetPath == s.asnAssetPath && manifest.Revision != "" && len(manifest.Files) > 0
}

func sameASNManifest(saved, current asnManifest) bool {
	return saved.Version == current.Version && saved.Source == current.Source && saved.Branch == current.Branch && saved.AssetPath == current.AssetPath && saved.Revision == current.Revision && len(saved.Files) > 0
}

func (s *Service) updateASN(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, defaultASNHTTPTimeout)
	defer cancel()
	manifest, err := s.fetchASNManifest(ctx)
	if err != nil {
		s.warn("update Smart ASN mirror: ", err)
		return
	}
	if loaded := s.index.Load(); loaded != nil && loaded.revision == manifest.Revision {
		return
	}
	root := filepath.Join(s.asnPath, "snapshots", manifest.Revision)
	if savedManifest, savedErr := s.readASNManifest(); savedErr == nil && sameASNManifest(savedManifest, manifest) {
		if index, indexErr := s.buildASNIndex(root, savedManifest.Files); indexErr == nil {
			manifest.Files = savedManifest.Files
			s.publishASN(manifest, index)
			return
		}
	}
	staging := root + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err = filemanager.MkdirAll(s.ctx, staging, 0o755); err != nil {
		s.warn("create Smart ASN staging directory: ", err)
		return
	}
	defer filemanager.RemoveAll(s.ctx, staging)
	if err = s.downloadASNArchive(ctx, &manifest, staging); err != nil {
		s.warn("download Smart ASN archive: ", err)
		return
	}
	index, err := s.buildASNIndex(staging, manifest.Files)
	if err != nil {
		s.warn("parse Smart ASN mirror: ", err)
		return
	}
	if err = filemanager.MkdirAll(s.ctx, filepath.Dir(root), 0o755); err == nil {
		_ = filemanager.RemoveAll(s.ctx, root)
		err = filemanager.Rename(s.ctx, staging, root)
	}
	if err != nil {
		s.warn("publish Smart ASN snapshot: ", err)
		return
	}
	s.publishASN(manifest, index)
}

func (s *Service) publishASN(manifest asnManifest, index *asnIndex) {
	content, err := json.Marshal(manifest)
	if err != nil {
		s.warn("encode Smart ASN manifest: ", err)
		return
	}
	manifestPath := filepath.Join(s.asnPath, "manifest.json")
	if err = filemanager.MkdirAll(s.ctx, s.asnPath, 0o755); err == nil {
		err = filemanager.WriteFile(s.ctx, manifestPath+".tmp", content, 0o600)
	}
	if err == nil {
		err = filemanager.Rename(s.ctx, manifestPath+".tmp", manifestPath)
	}
	if err != nil {
		s.warn("publish Smart ASN manifest: ", err)
		return
	}
	index.revision = manifest.Revision
	s.index.Store(index)
}

func (s *Service) fetchASNManifest(ctx context.Context) (asnManifest, error) {
	content, err := s.downloadWith(s.asnClient(), ctx, s.githubAPI()+"/repos/"+s.asnRepository+"/git/ref/heads/"+url.PathEscape(s.asnBranch), maxASNDownloadBytes)
	if err != nil {
		return asnManifest{}, err
	}
	var reference githubReference
	if err = json.Unmarshal(content, &reference); err != nil || reference.Object.SHA == "" || reference.Object.Type != "commit" {
		return asnManifest{}, E.New("invalid Smart ASN branch reference")
	}
	return asnManifest{
		Version:   asnManifestVersion,
		Source:    s.asnRepository,
		Branch:    s.asnBranch,
		AssetPath: s.asnAssetPath,
		Revision:  reference.Object.SHA,
	}, nil
}

func (s *Service) downloadASNArchive(ctx context.Context, manifest *asnManifest, staging string) error {
	client := s.asnClient()
	if client == nil {
		return E.New("Smart service is not ready")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPI()+"/repos/"+manifest.Source+"/tarball/"+manifest.Revision, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return E.New("unexpected HTTP status: ", response.Status)
	}
	if response.ContentLength > int64(maxASNArchiveBytes) {
		return E.New("ASN archive exceeds maximum size")
	}
	gzipReader, err := gzip.NewReader(io.LimitReader(response.Body, int64(maxASNArchiveBytes)+1))
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	assetPrefix := strings.TrimSuffix(manifest.AssetPath, "/") + "/"
	manifest.Files = nil
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 {
			continue
		}
		_, archivePath, found := strings.Cut(header.Name, "/")
		if !found || !strings.HasPrefix(archivePath, assetPrefix) {
			continue
		}
		assetName := strings.TrimPrefix(archivePath, assetPrefix)
		asn, valid := asnAssetNumber(assetName)
		if !valid {
			continue
		}
		if header.Size > int64(maxASNDownloadBytes) {
			return E.New("Smart ASN asset exceeds maximum size: ", assetName)
		}
		content, readErr := io.ReadAll(io.LimitReader(archive, int64(maxASNDownloadBytes)+1))
		if readErr != nil {
			return readErr
		}
		if len(content) > maxASNDownloadBytes {
			return E.New("Smart ASN asset exceeds maximum size: ", assetName)
		}
		if err = filemanager.WriteFile(s.ctx, filepath.Join(staging, assetName), content, 0o644); err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, asnAsset{Path: filepath.ToSlash(filepath.Join(manifest.AssetPath, asn+".srs"))})
	}
	if len(manifest.Files) == 0 {
		return E.New("no Smart ASN assets found")
	}
	if _, err = io.Copy(io.Discard, gzipReader); err != nil {
		return err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	return nil
}

func (s *Service) download(ctx context.Context, url string, maxBytes int) ([]byte, error) {
	return s.downloadWith(s.httpClient, ctx, url, maxBytes)
}

func (s *Service) downloadWith(client *http.Client, ctx context.Context, url string, maxBytes int) ([]byte, error) {
	if client == nil {
		return nil, E.New("Smart service is not ready")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, E.New("unexpected HTTP status: ", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxBytes {
		return nil, E.New("download exceeds maximum size")
	}
	return content, nil
}

func (s *Service) asnClient() *http.Client {
	if s.asnHTTPClient != nil {
		return s.asnHTTPClient
	}
	return s.httpClient
}

func (s *Service) githubAPI() string {
	if s.githubAPIURL != "" {
		return strings.TrimSuffix(s.githubAPIURL, "/")
	}
	return "https://api.github.com"
}

// LookupASN returns an AS label only after a fully parsed mirror has been
// atomically published. This preserves normal target weights during first sync.
func (s *Service) LookupASN(address netip.Addr) string {
	index := s.index.Load()
	if index == nil || !address.IsValid() {
		return ""
	}
	return index.lookup(address.Unmap())
}

type asnIndex struct {
	v4       *asnTrie
	v6       *asnTrie
	revision string
}

type asnTrie struct {
	children [2]*asnTrie
	asn      string
}

func (s *Service) buildASNIndex(root string, assets []asnAsset) (*asnIndex, error) {
	index := &asnIndex{v4: new(asnTrie), v6: new(asnTrie)}
	for _, asset := range assets {
		asn, loaded := asnAssetNumber(filepath.Base(asset.Path))
		if !loaded {
			return nil, E.New("invalid ASN asset path: ", asset.Path)
		}
		content, err := filemanager.ReadFile(s.ctx, filepath.Join(root, filepath.Base(asset.Path)))
		if err != nil {
			return nil, err
		}
		compat, err := srs.Read(bytes.NewReader(content), true)
		if err != nil {
			return nil, E.Cause(err, "read ", asset.Path)
		}
		for _, rule := range compat.Options.Rules {
			for _, prefix := range srsPrefixes(rule) {
				index.insert(prefix, asn)
			}
		}
	}
	return index, nil
}

func isASNAssetPath(assetPath, sourcePath string) bool {
	if sourcePath != "" {
		prefix := strings.TrimSuffix(sourcePath, "/") + "/"
		if !strings.HasPrefix(assetPath, prefix) {
			return false
		}
		assetPath = strings.TrimPrefix(assetPath, prefix)
	}
	_, loaded := asnAssetNumber(assetPath)
	return loaded
}

func asnAssetNumber(assetPath string) (string, bool) {
	if strings.Contains(assetPath, "/") || !strings.HasPrefix(assetPath, "AS") || !strings.HasSuffix(assetPath, ".srs") {
		return "", false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(assetPath, "AS"), ".srs")
	if number == "" || strings.Trim(number, "0123456789") != "" {
		return "", false
	}
	return "AS" + number, true
}

func srsPrefixes(rule option.HeadlessRule) []netip.Prefix {
	var prefixes []netip.Prefix
	if rule.Type == "logical" {
		for _, nested := range rule.LogicalOptions.Rules {
			prefixes = append(prefixes, srsPrefixes(nested)...)
		}
		return prefixes
	}
	prefixes = make([]netip.Prefix, 0, len(rule.DefaultOptions.IPCIDR))
	for _, value := range rule.DefaultOptions.IPCIDR {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && prefix.IsValid() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return prefixes
}

func (i *asnIndex) insert(prefix netip.Prefix, asn string) {
	trie := i.v6
	address := prefix.Addr()
	bytes := address.As16()
	if address.Is4() {
		trie = i.v4
		v4 := address.As4()
		copy(bytes[:], v4[:])
	}
	for index := 0; index < prefix.Bits(); index++ {
		bit := (bytes[index/8] >> (7 - index%8)) & 1
		if trie.children[bit] == nil {
			trie.children[bit] = new(asnTrie)
		}
		trie = trie.children[bit]
	}
	trie.asn = asn
}

func (i *asnIndex) lookup(address netip.Addr) string {
	trie := i.v6
	bytes := address.As16()
	bits := 128
	if address.Is4() {
		trie = i.v4
		v4 := address.As4()
		copy(bytes[:], v4[:])
		bits = 32
	}
	result := trie.asn
	for index := 0; index < bits; index++ {
		trie = trie.children[(bytes[index/8]>>(7-index%8))&1]
		if trie == nil {
			break
		}
		if trie.asn != "" {
			result = trie.asn
		}
	}
	return result
}

func (s *Service) warn(args ...any) {
	if s.logger != nil {
		s.logger.Warn(args...)
	}
}
