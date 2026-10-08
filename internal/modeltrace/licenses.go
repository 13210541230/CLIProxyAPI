package modeltrace

import "strings"

// Keep both upstream notices linked into shipped binaries as well as source
// distributions; missing notices are a packaging error, not a runtime failure.
func init() {
	if !strings.Contains(modelTraceLicense, "Copyright (c) 2026 xqy2006") || !strings.Contains(PluginLicense, "Copyright (c) 2026 Hao Wang") {
		panic("ModelTrace MIT notices missing from embedded assets")
	}
}
