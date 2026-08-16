package option

import (
	"reflect"

	"github.com/sagernet/sing-box/schema"
	"github.com/sagernet/sing/common/json/badoption"
)

type SelectorOutboundOptions struct {
	GroupCommonOption
	Default                   string `json:"default,omitempty" reference:"outbound"`
	InterruptExistConnections bool   `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	GroupCommonOption
	URL                       string                 `json:"url,omitempty"`
	Interval                  badoption.Duration     `json:"interval,omitempty"`
	Tolerance                 uint16                 `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration     `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool                   `json:"interrupt_exist_connections,omitempty"`
	Fallback                  URLTestFallbackOptions `json:"fallback,omitempty"`
}

type GroupCommonOption struct {
	Outbounds       []string          `json:"outbounds" reference:"outbound"`
	Providers       []string          `json:"providers" reference:"provider"`
	Exclude         *badoption.Regexp `json:"exclude,omitempty"`
	Include         *badoption.Regexp `json:"include,omitempty"`
	UseAllProviders bool              `json:"use_all_providers,omitempty"`
}

type URLTestFallbackOptions struct {
	Enabled  bool               `json:"enabled,omitempty"`
	MaxDelay badoption.Duration `json:"max_delay,omitempty"`
}

type LoadBalanceOutboundOptions struct {
	GroupCommonOption
	URL         string             `json:"url,omitempty"`
	Interval    badoption.Duration `json:"interval,omitempty"`
	IdleTimeout badoption.Duration `json:"idle_timeout,omitempty"`
	TTL         badoption.Duration `json:"ttl,omitempty"`
	Strategy    string             `json:"strategy,omitempty"`
}

type SmartOutboundOptions struct {
	GroupCommonOption
	URL               string             `json:"url,omitempty"`
	Interval          badoption.Duration `json:"interval,omitempty"`
	Timeout           badoption.Duration `json:"timeout,omitempty"`
	Tolerance         uint16             `json:"tolerance,omitempty"`
	MaxSelected       int                `json:"max_selected,omitempty"`
	MinSamples        int                `json:"min_samples,omitempty"`
	MaxFailedTimes    int                `json:"max_failed_times,omitempty"`
	HistoryPath       string             `json:"history_path,omitempty"`
	HistoryRetention  badoption.Duration `json:"history_retention,omitempty"`
	MaxHistoryEntries int                `json:"max_history_entries,omitempty"`
}

func (SmartOutboundOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	node := schema.StrictObject()
	if err := builder.FlattenStruct(node, reflect.TypeFor[SmartOutboundOptions]()); err != nil {
		return nil, err
	}
	for _, name := range []string{"max_selected", "min_samples", "max_failed_times", "max_history_entries"} {
		node.Properties.Put(name, schema.UnsignedNode(64))
	}
	for _, name := range []string{"interval", "timeout", "history_retention"} {
		node.Properties.Put(name, &schema.Node{Type: "string", Pattern: smartNonNegativeDurationPattern})
	}
	return node, nil
}

const smartNonNegativeDurationPattern = `^\+?(((\d+(\.\d*)?|\.\d+)(ns|us|\u00b5s|\u03bcs|ms|s|m|h|d))+|0)$`
