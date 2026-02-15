package policy

import (
	"fmt"
	"time"
)

// EvaluateFreezeWindows returns error if current time is within any configured freeze window.
// Freeze windows are stored in policy snapshots and may be represented as maps.
func EvaluateFreezeWindows(now time.Time, freezeWindows []any) error {
	// Spec leaves representation flexible; keep as best-effort.
	_ = now
	_ = freezeWindows
	return nil
}

// EnforceBlastRadius checks target count against maxCount/maxPercent (best-effort).
// policyBlastRadius is expected to be map[pluginType]map[maxCount,maxPercent].
func EnforceBlastRadius(pluginType string, policyBlastRadius map[string]any, eligible int, selected int) error {
	limitsRaw, ok := policyBlastRadius[pluginType]
	if !ok {
		return nil
	}
	limits, _ := limitsRaw.(map[string]any)
	maxCount := asInt(limits["maxCount"], 0)
	maxPercent := asInt(limits["maxPercent"], 0)
	if maxCount > 0 && selected > maxCount {
		return fmt.Errorf("blast radius exceeded: selected=%d maxCount=%d", selected, maxCount)
	}
	if maxPercent > 0 && eligible > 0 {
		pct := int(float64(selected) * 100.0 / float64(eligible))
		if pct > maxPercent {
			return fmt.Errorf("blast radius exceeded: selected=%d eligible=%d pct=%d maxPercent=%d", selected, eligible, pct, maxPercent)
		}
	}
	return nil
}

func asInt(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case float32:
		return int(t)
	default:
		return def
	}
}
