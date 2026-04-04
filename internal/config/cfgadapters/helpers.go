package cfgadapters

import (
	"fmt"
	"strings"
)

// trimNonEmptyEntries normalizes environment-derived slices by trimming whitespace and removing empty items.
func trimNonEmptyEntries(values []string) []string {
	trimmedValues := make([]string, 0, len(values))
	for _, value := range values {
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue == "" {
			continue
		}

		trimmedValues = append(trimmedValues, trimmedValue)
	}

	return trimmedValues
}

// parseKeyValueEntries converts KEY=VALUE entries from environment variables into a map.
func parseKeyValueEntries(entries []string, envVarName string) (map[string]string, error) {
	if len(entries) == 0 {
		return map[string]string{}, nil
	}

	parsedEntries := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("%s entry %q must use KEY=VALUE format", envVarName, entry)
		}

		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return nil, fmt.Errorf("%s entry %q must have a non-empty key", envVarName, entry)
		}

		parsedEntries[trimmedKey] = value
	}

	return parsedEntries, nil
}
