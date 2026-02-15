package policy

import (
	"fmt"
	"strconv"
)

func parseInt(s string) (int, error) {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid int %q", s)
	}
	return i, nil
}

// CoerceStringMap tries to convert various decoded JSON/YAML map shapes into map[string]string.
func CoerceStringMap(v any) (map[string]string, error) {
	out := map[string]string{}
	switch t := v.(type) {
	case map[string]string:
		for k, v := range t {
			out[k] = v
		}
		return out, nil
	case map[string]any:
		for k, vv := range t {
			if vv == nil {
				out[k] = ""
				continue
			}
			s, ok := vv.(string)
			if !ok {
				return nil, fmt.Errorf("value for %q is not string", k)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected map, got %T", v)
	}
}
