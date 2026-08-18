package option

import (
	"path"
	"reflect"
	"strings"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
)

// SmartOptions owns resources shared by every smart outbound group.
type SmartOptions struct {
	Model     SmartModelOptions     `json:"model,omitempty"`
	Collector SmartCollectorOptions `json:"collector,omitempty"`
	ASN       SmartASNOptions       `json:"asn,omitempty"`
}

type SmartModelOptions struct {
	Path           string             `json:"path,omitempty"`
	DownloadURL    string             `json:"download_url,omitempty"`
	AutoUpdate     bool               `json:"auto_update,omitempty"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
	HTTPClient     *HTTPClientOptions `json:"http_client,omitempty"`
}

type SmartCollectorOptions struct {
	Path    string `json:"path,omitempty"`
	MaxSize uint64 `json:"max_size,omitempty"`
}

type SmartASNOptions struct {
	Path           string             `json:"path,omitempty"`
	Repository     string             `json:"repository,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	AssetPath      string             `json:"asset_path,omitempty"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
	HTTPClient     *HTTPClientOptions `json:"http_client,omitempty"`
}

func (o SmartOptions) Validate() error {
	if o.Model.UpdateInterval < 0 || o.ASN.UpdateInterval < 0 {
		return E.New("invalid smart update_interval")
	}
	if repository := o.ASN.Repository; repository != "" {
		owner, name, found := strings.Cut(repository, "/")
		if !found || owner == "" || name == "" || strings.Contains(name, "/") {
			return E.New("invalid smart ASN repository")
		}
	}
	if assetPath := o.ASN.AssetPath; assetPath != "" {
		if path.IsAbs(assetPath) || path.Clean(assetPath) != assetPath || assetPath == "." || strings.HasPrefix(assetPath, "../") || strings.Contains(assetPath, "/../") {
			return E.New("invalid smart ASN asset_path")
		}
	}
	return nil
}

func (o SmartOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	node := schema.StrictObject()
	model := schema.StrictObject()
	if err := builder.FlattenStruct(model, reflect.TypeFor[SmartModelOptions]()); err != nil {
		return nil, err
	}
	model.Properties.Put("update_interval", &schema.Node{Type: "string", Pattern: smartNonNegativeDurationPattern})
	node.Properties.Put("model", model)
	asn := schema.StrictObject()
	if err := builder.FlattenStruct(asn, reflect.TypeFor[SmartASNOptions]()); err != nil {
		return nil, err
	}
	asn.Properties.Put("update_interval", &schema.Node{Type: "string", Pattern: smartNonNegativeDurationPattern})
	node.Properties.Put("asn", asn)
	collector := schema.StrictObject()
	if err := builder.FlattenStruct(collector, reflect.TypeFor[SmartCollectorOptions]()); err != nil {
		return nil, err
	}
	collector.Properties.Put("max_size", schema.UnsignedNode(64))
	node.Properties.Put("collector", collector)
	return node, nil
}
