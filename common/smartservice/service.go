package smartservice

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
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
	defaultASNPath              = "smart/asn/GeoLite2-ASN.mmdb"
	defaultASNURL               = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb"
	defaultASNInterval          = 24 * time.Hour
	defaultHTTPTimeout          = 90 * time.Second
	defaultASNHTTPTimeout       = 10 * time.Minute
	maxASNDownloadBytes     int = 128 << 20
	maxModelDownloadBytes   int = 128 << 20
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
	asnURL           string
	asnInterval      time.Duration
	asnEtag          string

	httpClient    *http.Client
	asnHTTPClient *http.Client

	modelAccess       sync.RWMutex
	model             *leaves.Ensemble
	modelUpdateAccess sync.Mutex
	modelEnabled      atomic.Bool
	asnReader         atomic.Pointer[maxminddb.Reader]

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
	asnURL := options.ASN.URL
	if asnURL == "" {
		asnURL = defaultASNURL
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
		asnURL:           asnURL,
		asnInterval:      asnInterval,
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

// loadASNMirror maps the local ASN database and remembers the ETag it was
// fetched with, so unchanged upstream assets can be skipped (304).
func (s *Service) loadASNMirror() error {
	reader, err := maxminddb.Open(s.asnPath)
	if err != nil {
		return err
	}
	s.asnReader.Store(reader)
	if etag, err := filemanager.ReadFile(s.ctx, s.asnPath+".etag"); err == nil {
		s.asnEtag = strings.TrimSpace(string(etag))
	}
	return nil
}

// updateASN refreshes the ASN database when the upstream asset changed.
func (s *Service) updateASN(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, defaultASNHTTPTimeout)
	defer cancel()
	if err := s.fetchASN(ctx); err != nil {
		s.warn("update Smart ASN database: ", err)
	}
}

// fetchASN downloads the ASN database and atomically publishes it. A 304
// response (If-None-Match) skips the download; a body that does not parse as
// a MaxMind database is discarded, matching remote rule-set behavior.
func (s *Service) fetchASN(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.asnURL, nil)
	if err != nil {
		return err
	}
	if s.asnEtag != "" {
		request.Header.Set("If-None-Match", s.asnEtag)
	}
	response, err := s.asnClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		return nil
	default:
		return E.New("unexpected status: ", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, int64(maxASNDownloadBytes)+1))
	if err != nil {
		return err
	}
	if len(content) > maxASNDownloadBytes {
		return E.New("download exceeds maximum size")
	}
	if err = filemanager.MkdirAll(s.ctx, filepath.Dir(s.asnPath), 0o755); err == nil {
		err = filemanager.WriteFile(s.ctx, s.asnPath+".tmp", content, 0o644)
	}
	if err == nil {
		err = filemanager.Rename(s.ctx, s.asnPath+".tmp", s.asnPath)
	} else {
		_ = filemanager.RemoveAll(s.ctx, s.asnPath+".tmp")
	}
	if err != nil {
		return E.Cause(err, "publish Smart ASN database")
	}
	mmdb, err := maxminddb.Open(s.asnPath)
	if err != nil {
		return err
	}
	s.asnReader.Store(mmdb)
	if etag := response.Header.Get("Etag"); etag != "" {
		s.asnEtag = etag
		_ = filemanager.WriteFile(s.ctx, s.asnPath+".etag", []byte(etag), 0o644)
	}
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

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
}

func (s *Service) LookupASN(address netip.Addr) string {
	reader := s.asnReader.Load()
	if reader == nil || !address.IsValid() {
		return ""
	}
	var record asnRecord
	if err := reader.Lookup(address.Unmap().AsSlice(), &record); err != nil || record.Number == 0 {
		return ""
	}
	return "AS" + strconv.FormatUint(uint64(record.Number), 10)
}

func (s *Service) warn(args ...any) {
	if s.logger != nil {
		s.logger.Warn(args...)
	}
}
