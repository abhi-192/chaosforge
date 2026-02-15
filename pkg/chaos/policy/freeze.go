package policy

import (
	"fmt"
	"strings"
	"time"
)

// FreezeWindow is a human-configured maintenance/freeze window during which new chaos runs
// must not start (typically for production change freezes).
//
// Representation is intentionally simple and matches config/chaosforge.yaml.example.
// - tz is an IANA TZ (e.g., "UTC", "Asia/Kolkata")
// - daysOfWeek is list of "MON".."SUN"
// - start/end are 24h time "HH:MM"
//
// If start > end, the window is treated as crossing midnight.
type FreezeWindow struct {
	Name       string
	TZ         string
	DaysOfWeek []string
	Start      string
	End        string
}

// EvaluateFreezeWindows returns an error if now falls within any configured freeze window.
// The policy model stores windows as []any (DynamoDB JSON-ish), so this parses best-effort.
func EvaluateFreezeWindows(now time.Time, freezeWindows []any) error {
	windows, err := NormalizeFreezeWindows(freezeWindows)
	if err != nil {
		return err
	}
	if len(windows) == 0 {
		return nil
	}
	frozen, name, err := IsFrozen(now, windows)
	if err != nil {
		return err
	}
	if frozen {
		return fmt.Errorf("environment is in a freeze window (%s)", name)
	}
	return nil
}

func NormalizeFreezeWindows(raw []any) ([]FreezeWindow, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]FreezeWindow, 0, len(raw))
	for i, v := range raw {
		switch t := v.(type) {
		case FreezeWindow:
			out = append(out, t)
		case map[string]any:
			fw := FreezeWindow{}
			if s, ok := t["name"].(string); ok {
				fw.Name = s
			}
			if s, ok := t["tz"].(string); ok {
				fw.TZ = s
			}
			if s, ok := t["start"].(string); ok {
				fw.Start = s
			}
			if s, ok := t["end"].(string); ok {
				fw.End = s
			}
			if ds, ok := t["daysOfWeek"].([]any); ok {
				for _, d := range ds {
					if ss, ok := d.(string); ok {
						fw.DaysOfWeek = append(fw.DaysOfWeek, ss)
					}
				}
			}
			if err := fw.validate(); err != nil {
				return nil, fmt.Errorf("invalid freezeWindows[%d]: %w", i, err)
			}
			out = append(out, fw)
		default:
			return nil, fmt.Errorf("invalid freezeWindows[%d]: expected map, got %T", i, v)
		}
	}
	return out, nil
}

func (fw FreezeWindow) validate() error {
	if strings.TrimSpace(fw.TZ) == "" {
		fw.TZ = "UTC"
	}
	if fw.Name == "" {
		fw.Name = "unnamed"
	}
	if _, err := time.LoadLocation(fw.TZ); err != nil {
		return fmt.Errorf("invalid tz %q: %w", fw.TZ, err)
	}
	if _, err := parseHHMM(fw.Start); err != nil {
		return fmt.Errorf("invalid start %q: %w", fw.Start, err)
	}
	if _, err := parseHHMM(fw.End); err != nil {
		return fmt.Errorf("invalid end %q: %w", fw.End, err)
	}
	if len(fw.DaysOfWeek) == 0 {
		// default: all days
		fw.DaysOfWeek = []string{"MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}
	}
	return nil
}

// IsFrozen returns whether now (UTC or any location) falls inside any freeze window.
func IsFrozen(now time.Time, windows []FreezeWindow) (bool, string, error) {
	for _, w := range windows {
		loc, err := time.LoadLocation(w.TZ)
		if err != nil {
			return false, "", err
		}
		local := now.In(loc)
		if !dayAllowed(local.Weekday(), w.DaysOfWeek) {
			continue
		}
		startMin, err := parseHHMM(w.Start)
		if err != nil {
			return false, "", err
		}
		endMin, err := parseHHMM(w.End)
		if err != nil {
			return false, "", err
		}
		curMin := local.Hour()*60 + local.Minute()
		if startMin == endMin {
			// Full-day freeze
			return true, w.Name, nil
		}
		if startMin < endMin {
			if curMin >= startMin && curMin < endMin {
				return true, w.Name, nil
			}
		} else {
			// Cross-midnight window
			if curMin >= startMin || curMin < endMin {
				return true, w.Name, nil
			}
		}
	}
	return false, "", nil
}

func parseHHMM(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("expected HH:MM")
	}
	hh, err := parseInt(parts[0])
	if err != nil {
		return 0, err
	}
	mm, err := parseInt(parts[1])
	if err != nil {
		return 0, err
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("out of range")
	}
	return hh*60 + mm, nil
}

func dayAllowed(wd time.Weekday, days []string) bool {
	want := weekdayToken(wd)
	for _, d := range days {
		if strings.EqualFold(strings.TrimSpace(d), want) {
			return true
		}
	}
	return false
}

func weekdayToken(wd time.Weekday) string {
	switch wd {
	case time.Monday:
		return "MON"
	case time.Tuesday:
		return "TUE"
	case time.Wednesday:
		return "WED"
	case time.Thursday:
		return "THU"
	case time.Friday:
		return "FRI"
	case time.Saturday:
		return "SAT"
	default:
		return "SUN"
	}
}
