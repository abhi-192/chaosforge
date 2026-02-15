package policy

import "fmt"

// EnforceBlastRadius checks selected target count against maxCount/maxPercent.
//
// policyBlastRadius is expected to be map[targetType]map[maxCount,maxPercent].
// targetType should align with policy keys (e.g., ec2|ssm|alb).
func EnforceBlastRadius(targetType string, policyBlastRadius map[string]any, eligible int, selected int) error {
	if policyBlastRadius == nil {
		return nil
	}
	limitsRaw, ok := policyBlastRadius[targetType]
	if !ok {
		// If policy doesn't define the type, default is deny (safer) IF eligible>0 and selected>0.
		if selected > 0 {
			return fmt.Errorf("no blast radius limits configured for target type %q", targetType)
		}
		return nil
	}
	limits, _ := limitsRaw.(map[string]any)
	maxCount := asInt(limits["maxCount"], 0)
	maxPercent := asInt(limits["maxPercent"], 0)
	if maxCount > 0 && selected > maxCount {
		return fmt.Errorf("blast radius exceeded: selected=%d maxCount=%d", selected, maxCount)
	}
	if maxPercent > 0 {
		if eligible <= 0 {
			// If we can't compute percent, fall back to count enforcement only.
			return nil
		}
		pct := int((float64(selected) * 100.0) / float64(eligible))
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
