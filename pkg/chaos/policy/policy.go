package policy

import "fmt"

// EnforceAllowedPlugins returns error if pluginID is not in allowed list.
func EnforceAllowedPlugins(pluginID string, allowed []string) error {
	for _, p := range allowed {
		if p == pluginID {
			return nil
		}
	}
	return fmt.Errorf("plugin %q is not allowed by policy", pluginID)
}
