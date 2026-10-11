package option

import "github.com/sagernet/sing-box/schema"

func (XHTTPRange) DescribeSchema(_ schema.Builder) (*schema.Node, error) {
	minimum, maximum := int64(-2147483648), uint64(2147483647)
	return schema.AnyOf(
		&schema.Node{Type: "integer", Minimum: &minimum, Maximum: &maximum},
		&schema.Node{Type: "string", Pattern: `^\s*(?:[+-]?[0-9]+(?:\s*-\s*[+-]?[0-9]+)?)?\s*$`},
	), nil
}
