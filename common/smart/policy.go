package smart

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type PriorityRule struct {
	pattern string
	factor  float64
	regex   *regexp.Regexp
}

func ParsePriorityRules(value string) ([]PriorityRule, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ";")
	rules := make([]PriorityRule, 0, len(parts))
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		index := lastUnescapedColon(raw)
		if index <= 0 || index == len(raw)-1 {
			return nil, fmt.Errorf("invalid policy_priority rule %q: expected pattern:factor", raw)
		}
		pattern := unescapePolicyPattern(strings.TrimSpace(raw[:index]))
		if pattern == "" {
			return nil, fmt.Errorf("invalid policy_priority rule %q: empty pattern", raw)
		}
		factor, err := strconv.ParseFloat(strings.TrimSpace(raw[index+1:]), 64)
		if err != nil || factor <= 0 || math.IsInf(factor, 0) || math.IsNaN(factor) {
			return nil, fmt.Errorf("invalid policy_priority factor in %q", raw)
		}
		rule := PriorityRule{pattern: pattern, factor: factor}
		rule.regex, _ = regexp.Compile(pattern)
		rules = append(rules, rule)
	}
	return rules, nil
}

func (rules PriorityRuleList) Factor(node string) float64 {
	for _, rule := range rules {
		if rule.regex != nil && rule.regex.MatchString(node) || rule.regex == nil && strings.Contains(node, rule.pattern) {
			return rule.factor
		}
	}
	return 1
}

type PriorityRuleList []PriorityRule

type StatusRange struct{ From, To uint16 }

type StatusRanges []StatusRange

func ParseStatusRanges(value string) (StatusRanges, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 28 {
		return nil, fmt.Errorf("too many expected_status ranges")
	}
	ranges := make(StatusRanges, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 || part == "" {
			return nil, fmt.Errorf("invalid expected_status range %q", part)
		}
		from, err := parseStatusCode(strings.TrimSpace(bounds[0]))
		if err != nil {
			return nil, err
		}
		to := from
		if len(bounds) == 2 {
			to, err = parseStatusCode(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, err
			}
		}
		if from > to {
			return nil, fmt.Errorf("invalid expected_status range %q", part)
		}
		ranges = append(ranges, StatusRange{from, to})
	}
	return ranges, nil
}

func (ranges StatusRanges) Contains(status uint16) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, item := range ranges {
		if status >= item.From && status <= item.To {
			return true
		}
	}
	return false
}

func parseStatusCode(value string) (uint16, error) {
	code, err := strconv.ParseUint(value, 10, 16)
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("invalid expected_status code %q", value)
	}
	return uint16(code), nil
}

func lastUnescapedColon(value string) int {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] != ':' {
			continue
		}
		backslashes := 0
		for index := i - 1; index >= 0 && value[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return i
		}
	}
	return -1
}

func unescapePolicyPattern(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) {
			index++
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}
