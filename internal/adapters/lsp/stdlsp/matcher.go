package stdlsp

import "strings"

// namePathMatcher stores the compiled path pattern for repeated symbol checks.
type namePathMatcher struct {
	// isAbsolute requires an exact full-path match instead of a suffix match.
	isAbsolute bool
	// substringMatching enables substring matching only for the last pattern segment.
	substringMatching bool
	// components contains the parsed name-path pattern.
	components []string
}

// JoinNamePath centralizes slash-delimited paths so output stays consistent across response shapes.
func JoinNamePath(parentPath, name string) string {
	if parentPath == "" {
		return name
	}

	return parentPath + "/" + name
}

// newNamePathMatcher compiles one supported name-path pattern for repeated checks.
func newNamePathMatcher(pattern string, substringMatching bool) *namePathMatcher {
	trimmedPattern := strings.TrimSpace(pattern)
	matcher := &namePathMatcher{
		isAbsolute:        strings.HasPrefix(trimmedPattern, "/"),
		substringMatching: substringMatching,
		components:        parseNamePathComponents(strings.Trim(trimmedPattern, "/")),
	}

	return matcher
}

// matches checks whether one normalized name path satisfies the compiled pattern.
func (m *namePathMatcher) matches(namePath string) bool {
	components := parseNamePathComponents(strings.Trim(namePath, "/"))
	if len(m.components) == 0 || len(components) < len(m.components) {
		return false
	}

	for patternOffset := range len(m.components) {
		patternComponent := m.components[len(m.components)-1-patternOffset]
		candidateComponent := components[len(components)-1-patternOffset]
		useSubstringMatching := m.substringMatching && patternOffset == 0
		if !matchNamePathComponent(patternComponent, candidateComponent, useSubstringMatching) {
			return false
		}
	}

	return !m.isAbsolute || len(components) == len(m.components)
}

// parseNamePathComponents splits one name-path string into slash-delimited components.
func parseNamePathComponents(namePath string) []string {
	if strings.TrimSpace(namePath) == "" {
		return nil
	}

	return strings.Split(namePath, "/")
}

// matchNamePathComponent compares one pattern component against one symbol component.
func matchNamePathComponent(patternComponent, candidateComponent string, substringMatching bool) bool {
	patternBase, patternHasDiscriminator := splitNamePathDiscriminator(patternComponent)
	candidateBase, _ := splitNamePathDiscriminator(candidateComponent)

	switch {
	case substringMatching:
		candidateValue := candidateBase
		patternValue := patternBase
		if patternHasDiscriminator {
			candidateValue = candidateComponent
			patternValue = patternComponent
		}
		if !strings.Contains(candidateValue, patternValue) {
			return false
		}
	case patternHasDiscriminator:
		if candidateComponent != patternComponent {
			return false
		}
	case candidateBase != patternBase:
		return false
	}

	return true
}

// splitNamePathDiscriminator removes one duplicate-symbol position suffix when the suffix matches the exported format.
func splitNamePathDiscriminator(component string) (string, bool) {
	separatorIndex := strings.LastIndex(component, namePathDiscriminatorSeparator)
	if separatorIndex <= 0 || separatorIndex >= len(component)-1 {
		return component, false
	}

	if !isNamePathDiscriminator(component[separatorIndex+1:]) {
		return component, false
	}

	return component[:separatorIndex], true
}

// isNamePathDiscriminator validates the exported line:character duplicate suffix without allocations or regexes.
func isNamePathDiscriminator(value string) bool {
	lineValue, characterValue, ok := strings.Cut(value, namePathDiscriminatorValueSeparator)
	if !ok || lineValue == "" || characterValue == "" {
		return false
	}

	return isDecimalDigits(lineValue) && isDecimalDigits(characterValue)
}

// isDecimalDigits checks that one string contains only ASCII decimal digits.
func isDecimalDigits(value string) bool {
	for _, runeValue := range value {
		if runeValue < '0' || runeValue > '9' {
			return false
		}
	}

	return true
}
