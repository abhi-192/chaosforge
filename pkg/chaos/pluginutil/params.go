package pluginutil

import (
	"fmt"

	"chaosforge/pkg/chaos/policy"
)

// GetIntParam reads an int-like param from a map.
func GetIntParam(params map[string]any, key string, def int) int {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return def
	}
}

// GetStringMapParam reads a map param as map[string]string.
func GetStringMapParam(params map[string]any, key string) (map[string]string, error) {
	if params == nil {
		return map[string]string{}, nil
	}
	v, ok := params[key]
	if !ok || v == nil {
		return map[string]string{}, nil
	}
	m, err := policy.CoerceStringMap(v)
	if err != nil {
		return nil, fmt.Errorf("param %s: %w", key, err)
	}
	return m, nil
}
