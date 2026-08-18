package config

// MCP server entries live under the top-level "mcpServers" object (the
// Claude Desktop / Cline dialect agent/mcp parses). These setters are the
// write path behind `orb mcp`; like every other setter they persist to the
// global settings file, while project settings keep overlaying per-server
// through the one-level merge.

// GetMCPServers returns the merged mcpServers objects, raw and including
// disabled entries, keyed by server name.
func (manager *SettingsManager) GetMCPServers() map[string]map[string]any {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	configured := nestedObject(manager.effective, "mcpServers")
	result := make(map[string]map[string]any, len(configured))
	for name, value := range configured {
		switch server := value.(type) {
		case map[string]any:
			result[name] = cloneMap(server)
		case Settings:
			result[name] = cloneMap(server)
		}
	}
	return result
}

// SetMCPServer persists one server entry, replacing any previous value.
func (manager *SettingsManager) SetMCPServer(name string, server map[string]any) {
	manager.setGlobalNested("mcpServers", name, server)
}

// RemoveMCPServer deletes one server entry from the global settings.
func (manager *SettingsManager) RemoveMCPServer(name string) {
	manager.removeGlobalNested("mcpServers", name)
}

// SetMCPServerEnabled flips enabled while preserving the rest of the entry,
// and clears the "disabled" alias so the entry cannot contradict itself.
func (manager *SettingsManager) SetMCPServerEnabled(name string, enabled bool) {
	manager.mu.RLock()
	configured := nestedObject(nestedObject(manager.global, "mcpServers"), name)
	if configured != nil {
		configured = cloneMap(configured)
	}
	manager.mu.RUnlock()
	if configured == nil {
		configured = map[string]any{}
	}
	configured["enabled"] = enabled
	delete(configured, "disabled")
	manager.setGlobalNested("mcpServers", name, configured)
}
