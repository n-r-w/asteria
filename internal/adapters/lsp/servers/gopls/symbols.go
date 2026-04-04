package lspgopls

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

// GetSymbolsOverview publishes canonical Go symbol paths while stdlsp keeps the shared overview workflow.
func (s *Service) GetSymbolsOverview(
	ctx context.Context, request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	return s.std.GetSymbolsOverview(ctx, request)
}

// FindSymbol normalizes accepted Go query variants before the first shared stdlsp search starts.
func (s *Service) FindSymbol(
	ctx context.Context, request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		request.Path = normalizeGoQueryPath(request.Path)
	}

	return s.std.FindSymbol(ctx, request)
}

// FindReferencingSymbols normalizes accepted Go query variants before stdlsp resolves the target symbol.
func (s *Service) FindReferencingSymbols(
	ctx context.Context, request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	if request == nil {
		return s.std.FindReferencingSymbols(ctx, nil)
	}

	request.Path = normalizeGoQueryPath(request.Path)

	return s.std.FindReferencingSymbols(ctx, request)
}

// shouldIgnoreDir keeps gopls directory traversal aligned with the previous hidden-directory skip behavior.
func shouldIgnoreDir(relativePath string) bool {
	return strings.HasPrefix(filepath.Base(relativePath), ".")
}

// shouldWatchGoFile keeps runtime-managed watched-files focused on Go source and workspace-definition files.
func shouldWatchGoFile(relativePath string) bool {
	switch filepath.Base(relativePath) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	default:
		return filepath.Ext(relativePath) == ".go"
	}
}

// buildNamePath publishes the canonical Go path contract: package-unqualified names separated with '/'.
func buildNamePath(parentPath, symbolName string) string {
	if receiverType, methodName, ok := parseGoMethodSymbol(symbolName); ok {
		if parentPath == "" {
			parentPath = receiverType
		}

		return stdlsp.JoinNamePath(parentPath, methodName)
	}

	return stdlsp.JoinNamePath(parentPath, symbolName)
}

// normalizeGoQueryPath accepts package-qualified relative Go queries while reserving leading-slash exact
// matching for canonical public paths only.
func normalizeGoQueryPath(path string) string {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" || strings.HasPrefix(trimmedPath, "/") {
		return trimmedPath
	}

	segments := strings.Split(trimmedPath, "/")

	if len(segments) > 1 &&
		isLikelyGoPackageSegment(segments[0]) &&
		startsWithLetter(stripGoPackageQualifier(segments[1])) {
		segments = segments[1:]
	}
	segments[0] = stripGoPackageQualifier(segments[0])

	return strings.Join(segments, "/")
}

// stripGoPackageQualifier removes one leading package qualifier from one symbol-path segment.
func stripGoPackageQualifier(segment string) string {
	separatorIndex := strings.LastIndex(segment, ".")
	if separatorIndex <= 0 || separatorIndex == len(segment)-1 {
		return segment
	}

	packagePart := segment[:separatorIndex]
	symbolPart := segment[separatorIndex+1:]
	if !isLikelyGoPackageSegment(packagePart) || !startsWithLetter(symbolPart) {
		return segment
	}

	return symbolPart
}

// isLikelyGoPackageSegment keeps Go query normalization conservative so canonical paths keep priority.
func isLikelyGoPackageSegment(segment string) bool {
	if segment == "" || !startsWithLowercaseLetter(segment) {
		return false
	}

	for _, runeValue := range segment {
		if unicode.IsLower(runeValue) || unicode.IsDigit(runeValue) || runeValue == '_' {
			continue
		}

		return false
	}

	return true
}

// parseGoMethodSymbol recognizes gopls method names with receiver prefixes and returns a normalized receiver type.
func parseGoMethodSymbol(symbolName string) (receiverType, methodName string, ok bool) {
	lastDotIndex := strings.LastIndex(symbolName, ".")
	if lastDotIndex <= 0 || lastDotIndex == len(symbolName)-1 {
		return "", "", false
	}

	leftPart := symbolName[:lastDotIndex]
	rightPart := symbolName[lastDotIndex+1:]
	normalizedReceiver := normalizeGoReceiverType(leftPart)
	if normalizedReceiver == "" {
		return "", "", false
	}
	if normalizedReceiver == leftPart && !looksLikeGoReceiver(leftPart) {
		return "", "", false
	}

	return normalizedReceiver, rightPart, true
}

// normalizeGoReceiverType strips pointers, generics, and package qualifiers from one receiver type string.
func normalizeGoReceiverType(receiver string) string {
	normalizedReceiver := strings.TrimSpace(receiver)
	normalizedReceiver = strings.TrimPrefix(normalizedReceiver, "(")
	normalizedReceiver = strings.TrimPrefix(normalizedReceiver, "*")
	normalizedReceiver = strings.TrimSuffix(normalizedReceiver, ")")
	normalizedReceiver = strings.TrimPrefix(normalizedReceiver, "*")
	if genericIndex := strings.Index(normalizedReceiver, "["); genericIndex >= 0 {
		normalizedReceiver = normalizedReceiver[:genericIndex]
	}
	if packageSeparatorIndex := strings.LastIndex(normalizedReceiver, "."); packageSeparatorIndex >= 0 {
		normalizedReceiver = normalizedReceiver[packageSeparatorIndex+1:]
	}

	return strings.TrimSpace(normalizedReceiver)
}

// looksLikeGoReceiver prevents plain dotted names from being mistaken for receiver-prefixed method symbols.
func looksLikeGoReceiver(candidate string) bool {
	return strings.ContainsAny(candidate, "()*[") || startsWithUppercaseLetter(candidate)
}

// startsWithUppercaseLetter checks the first rune without allocating a full []rune slice.
func startsWithUppercaseLetter(value string) bool {
	runeValue, _ := utf8.DecodeRuneInString(value)

	return runeValue != utf8.RuneError && unicode.IsUpper(runeValue)
}

// startsWithLetter keeps package-qualifier stripping symmetric for exported and unexported Go identifiers.
func startsWithLetter(value string) bool {
	runeValue, _ := utf8.DecodeRuneInString(value)

	return runeValue != utf8.RuneError && unicode.IsLetter(runeValue)
}

// startsWithLowercaseLetter checks the first rune without allocating a full []rune slice.
func startsWithLowercaseLetter(value string) bool {
	runeValue, _ := utf8.DecodeRuneInString(value)

	return runeValue != utf8.RuneError && unicode.IsLower(runeValue)
}
