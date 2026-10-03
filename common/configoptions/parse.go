package configoptions

import (
	"context"

	"github.com/sagernet/sing-box/common/configscript"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// Parse generates marked configuration comments and strictly decodes the
// selected source using the caller's context and host identity.
func Parse(ctx context.Context, source []byte, host configscript.Host) (option.Options, error) {
	content, hasScripts, err := configscript.Generate(ctx, source, host)
	if err != nil {
		return option.Options{}, E.Cause(err, "generate config")
	}
	if !hasScripts {
		content = source
	}

	options, err := json.UnmarshalExtendedContext[option.Options](ctx, content)
	if err != nil {
		err = E.Cause(err, "decode config")
		if hasScripts {
			err = configscript.WrapGeneratedConfigError(source, err)
		}
		return option.Options{}, err
	}
	return options, nil
}
