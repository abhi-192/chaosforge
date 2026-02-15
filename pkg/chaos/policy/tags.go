package policy

import (
	"fmt"
)

// EnsureTagScoping enforces requiredTags/denyTags for target selection.
//
// Convention: params may include a "targetTags" field used by plugins to scope resource selection.
// This function:
//  1. Parses/creates targetTags as map[string]string
//  2. Rejects if caller tries to include any deny tag value
//  3. Ensures all required tags are present (and match when required value is not "*")
//
// Returns updated params (with normalized targetTags).
func EnsureTagScoping(params map[string]any, requiredTags, denyTags map[string]string) (map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	tags := map[string]string{}
	if raw, ok := params["targetTags"]; ok && raw != nil {
		m, err := CoerceStringMap(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid params.targetTags: %w", err)
		}
		tags = m
	}

	// Deny tags (caller must not request a denied scope)
	for k, denyVal := range denyTags {
		if v, ok := tags[k]; ok {
			if denyVal == "*" || v == denyVal {
				return nil, fmt.Errorf("deny tag present: %s=%s", k, v)
			}
		}
	}
	// Merge required tags (fill missing) + enforce mismatches
	for k, v := range requiredTags {
		if cur, ok := tags[k]; ok {
			if v != "*" && v != "" && cur != v {
				return nil, fmt.Errorf("required tag mismatch: %s=%s (expected %s)", k, cur, v)
			}
			continue
		}
		if v == "*" {
			// wildcard value means key must exist (plugin should treat empty as tag-key filter)
			tags[k] = ""
		} else {
			tags[k] = v
		}
	}

	params["targetTags"] = tags
	return params, nil
}

// EnforceTags validates a tag map against required and deny rules.
// A required value of "*" means the key must exist with any value.
// A deny value of "*" means the key must not exist at all.
func EnforceTags(tags map[string]string, required, deny map[string]string) error {
	for k, denyVal := range deny {
		if v, ok := tags[k]; ok {
			if denyVal == "*" || v == denyVal {
				return fmt.Errorf("deny tag present: %s=%s", k, v)
			}
		}
	}
	for k, reqVal := range required {
		v, ok := tags[k]
		if !ok {
			continue
		}
		if reqVal != "*" && reqVal != "" && v != reqVal {
			return fmt.Errorf("required tag mismatch: %s=%s (expected %s)", k, v, reqVal)
		}
	}
	return nil
}
