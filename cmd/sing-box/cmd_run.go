package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	runtimeDebug "runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/configoptions"
	"github.com/sagernet/sing-box/common/configscript"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/service"

	"github.com/spf13/cobra"
)

var commandRun = &cobra.Command{
	Use:   "run",
	Short: "Run service",
	Run: func(cmd *cobra.Command, args []string) {
		err := run()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	mainCommand.AddCommand(commandRun)
}

type OptionsEntry struct {
	content []byte
	path    string
	options option.Options
}

func cliConfigScriptHost() configscript.Host {
	return configscript.Host{OS: runtime.GOOS, Arch: runtime.GOARCH, Client: "cli"}
}

func newCLIServiceContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(service.ExtendContext(globalCtx))
	service.MustRegister[configscript.Host](ctx, cliConfigScriptHost())
	return ctx, cancel
}

func readConfigAt(path string) (*OptionsEntry, error) {
	optionsEntry, err := readConfigRawAt(path)
	if err != nil {
		return nil, err
	}
	optionsEntry.options, err = configoptions.Parse(globalCtx, optionsEntry.content, cliConfigScriptHost())
	if err != nil {
		return nil, E.Cause(err, "decode config at ", path)
	}
	return optionsEntry, nil
}

func readConfigRawAt(path string) (*OptionsEntry, error) {
	var (
		configContent []byte
		err           error
	)
	if path == "stdin" {
		configContent, err = io.ReadAll(os.Stdin)
	} else {
		configContent, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, E.Cause(err, "read config at ", path)
	}
	return &OptionsEntry{content: configContent, path: path}, nil
}

func readConfigRaw() ([]*OptionsEntry, error) {
	var optionsList []*OptionsEntry
	for _, path := range configPaths {
		optionsEntry, err := readConfigRawAt(path)
		if err != nil {
			return nil, err
		}
		optionsList = append(optionsList, optionsEntry)
	}
	for _, directory := range configDirectories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, E.Cause(err, "read config directory at ", directory)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
				continue
			}
			optionsEntry, err := readConfigRawAt(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			optionsList = append(optionsList, optionsEntry)
		}
	}
	sort.Slice(optionsList, func(i, j int) bool {
		return optionsList[i].path < optionsList[j].path
	})
	return optionsList, nil
}

func readConfig() ([]*OptionsEntry, error) {
	optionsList, err := readConfigRaw()
	if err != nil {
		return nil, err
	}
	for _, optionsEntry := range optionsList {
		optionsEntry.options, err = configoptions.Parse(globalCtx, optionsEntry.content, cliConfigScriptHost())
		if err != nil {
			return nil, E.Cause(err, "decode config at ", optionsEntry.path)
		}
	}
	return optionsList, nil
}

func readConfigAndMerge() (option.Options, error) {
	optionsList, err := readConfig()
	if err != nil {
		return option.Options{}, err
	}
	return mergeOptionsList(optionsList)
}

func mergeOptionsList(optionsList []*OptionsEntry) (option.Options, error) {
	if len(optionsList) == 1 {
		return optionsList[0].options, nil
	}
	var (
		mergedMessage json.RawMessage
		err           error
	)
	for _, options := range optionsList {
		mergedMessage, err = badjson.MergeJSON(globalCtx, options.options.RawMessage, mergedMessage, false)
		if err != nil {
			return option.Options{}, E.Cause(err, "merge config at ", options.path)
		}
	}
	var mergedOptions option.Options
	err = mergedOptions.UnmarshalJSONContext(globalCtx, mergedMessage)
	if err != nil {
		return option.Options{}, E.Cause(err, "unmarshal merged config")
	}
	return mergedOptions, nil
}

func wrapCLIConfigSources(optionsList []*OptionsEntry, err error) error {
	if err == nil {
		return nil
	}
	for _, optionsEntry := range optionsList {
		hasScripts, inspectErr := configscript.HasScripts(optionsEntry.content)
		if inspectErr != nil || !hasScripts {
			continue
		}
		err = configscript.WrapGeneratedConfigError(optionsEntry.content, err)
		err = E.Cause(err, "config at ", optionsEntry.path)
	}
	return err
}

// configCheckerFunc exposes check to the Clash API, which reloads through the
// same path as SIGHUP.
type configCheckerFunc func() error

func (f configCheckerFunc) CheckConfig() error {
	return f()
}

func create(options option.Options, checkConfig func() error, optionsList []*OptionsEntry) (*box.Box, context.CancelFunc, error) {
	if disableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}
	ctx, cancel := newCLIServiceContext()
	service.MustRegister[adapter.ConfigChecker](ctx, configCheckerFunc(checkConfig))
	instance, err := box.New(box.Options{
		Context:                    ctx,
		Options:                    options,
		NetworkNamespaceHolderArgs: []string{"/proc/self/exe", commandNetnsHolder.Use},
	})
	if err != nil {
		cancel()
		return nil, nil, E.Cause(wrapCLIConfigSources(optionsList, err), "create service")
	}

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(osSignals)
		close(osSignals)
	}()
	startCtx, finishStart := context.WithCancel(context.Background())
	go func() {
		_, loaded := <-osSignals
		if loaded {
			cancel()
			closeMonitor(startCtx)
		}
	}()
	err = instance.Start()
	finishStart()
	if err != nil {
		cancel()
		return nil, nil, E.Cause(wrapCLIConfigSources(optionsList, err), "start service")
	}
	return instance, cancel, nil
}

func run() error {
	optionsList, err := readConfig()
	if err != nil {
		return err
	}
	options, err := mergeOptionsList(optionsList)
	if err != nil {
		return err
	}
	err = runInUserNamespaceIfNeeded(options, optionsList)
	if err != nil {
		return err
	}
	var (
		reloadCandidateLock    sync.Mutex
		reloadCandidate        option.Options
		reloadCandidateSources []*OptionsEntry
		reloadCandidateSet     bool
	)
	checkAndStoreCandidate := func() error {
		candidate, sources, err := readConfigAndCheck()
		if err != nil {
			return err
		}
		reloadCandidateLock.Lock()
		reloadCandidate = candidate
		reloadCandidateSources = sources
		reloadCandidateSet = true
		reloadCandidateLock.Unlock()
		return nil
	}
	takeCandidate := func() (option.Options, []*OptionsEntry, bool) {
		reloadCandidateLock.Lock()
		defer reloadCandidateLock.Unlock()
		if !reloadCandidateSet {
			return option.Options{}, nil, false
		}
		candidate := reloadCandidate
		sources := reloadCandidateSources
		reloadCandidate = option.Options{}
		reloadCandidateSources = nil
		reloadCandidateSet = false
		return candidate, sources, true
	}
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	for {
		instance, cancel, createErr := create(options, checkAndStoreCandidate, optionsList)
		if createErr != nil {
			return createErr
		}
		runtimeDebug.FreeOSMemory()
		for {
			reloadTag := false
			var (
				nextOptions option.Options
				nextSources []*OptionsEntry
			)
			select {
			case osSignal := <-osSignals:
				if osSignal == syscall.SIGHUP {
					nextOptions, nextSources, err = readConfigAndCheck()
					if err != nil {
						log.Error(E.Cause(err, "reload service"))
						continue
					}
					takeCandidate()
					reloadTag = true
				}
			case <-instance.ReloadChan():
				nextOptions, nextSources, reloadTag = takeCandidate()
				if !reloadTag {
					nextOptions, nextSources, err = readConfigAndCheck()
					if err != nil {
						log.Error(E.Cause(err, "reload service"))
						continue
					}
					reloadTag = true
				}
			}
			cancel()
			closeCtx, closed := context.WithCancel(context.Background())
			go closeMonitor(closeCtx)
			err = instance.Close()
			closed()
			if !reloadTag {
				if err != nil {
					log.Error(E.Cause(err, "sing-box did not closed properly"))
				}
				return nil
			}
			options = nextOptions
			optionsList = nextSources
			break
		}
	}
}

func closeMonitor(ctx context.Context) {
	time.Sleep(C.FatalStopTimeout)
	select {
	case <-ctx.Done():
		return
	default:
	}
	log.Fatal("sing-box did not close!")
}
