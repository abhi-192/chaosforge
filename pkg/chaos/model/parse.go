package model

import (
	"fmt"
	"strconv"
	"strings"
)

func ParsePluginRef(ref string) (pluginID string, version int, err error) {
	parts := strings.Split(ref, "@")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid pluginRef %q", ref)
	}
	v, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid pluginRef version %q: %w", parts[1], err)
	}
	return parts[0], v, nil
}
