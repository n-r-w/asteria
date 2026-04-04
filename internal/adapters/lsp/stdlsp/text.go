package stdlsp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"go.lsp.dev/protocol"
)

// inclusiveLineBounds converts one LSP range into 0-based inclusive line bounds.
func inclusiveLineBounds(targetRange protocol.Range) (startLine, endLine int) {
	startLine = int(targetRange.Start.Line)
	endLine = int(targetRange.End.Line)
	if targetRange.End.Character == 0 && endLine > startLine {
		endLine--
	}

	return startLine, max(startLine, endLine)
}

// readSymbolBody slices one full symbol body from disk using the LSP range reported by the provider.
func readSymbolBody(workspaceRootPath string, node *node, fileCache map[string]string) (string, error) {
	content, err := readFileContent(workspaceRootPath, node.RelativePath, fileCache)
	if err != nil {
		return "", err
	}

	return sliceContentByRange(content, node.Range), nil
}

// readReferenceSnippet returns one line of leading context, the reference line, and one line of trailing context.
func readReferenceSnippet(
	workspaceRootPath string,
	relativePath string,
	line int,
	fileCache map[string]string,
) (snippet string, startLine, endLine int, err error) {
	content, err := readFileContent(workspaceRootPath, relativePath, fileCache)
	if err != nil {
		return "", 0, 0, err
	}

	lines := strings.Split(content, "\n")
	if line < 0 || line >= len(lines) {
		return "", 0, 0, fmt.Errorf("reference line %d is outside %q", line, relativePath)
	}

	startLine = max(0, line-1)
	endLine = min(len(lines)-1, line+1)
	snippetLines := make([]string, 0, endLine-startLine+1)
	for lineIndex := startLine; lineIndex <= endLine; lineIndex++ {
		snippetLines = append(snippetLines, lines[lineIndex])
	}

	return strings.Join(snippetLines, "\n"), startLine, endLine, nil
}

// readFileContent caches file reads so symbol bodies and snippets do not keep hitting the disk.
func readFileContent(workspaceRootPath, relativePath string, fileCache map[string]string) (string, error) {
	if cachedContent, ok := fileCache[relativePath]; ok {
		return cachedContent, nil
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRootPath, relativePath)
	if err != nil {
		return "", err
	}

	safeAbsolutePath := filepath.Clean(absolutePath)
	rawContent, err := os.ReadFile(safeAbsolutePath)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", relativePath, err)
	}

	content := string(rawContent)
	fileCache[relativePath] = content

	return content, nil
}

// sliceContentByRange extracts text for one LSP range using UTF-16-aware column conversion.
func sliceContentByRange(content string, targetRange protocol.Range) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}

	startLine := min(max(0, int(targetRange.Start.Line)), len(lines)-1)
	endLine := min(max(0, int(targetRange.End.Line)), len(lines)-1)
	if startLine > endLine {
		return ""
	}

	if startLine == endLine {
		return sliceLineByUTF16Columns(lines[startLine], int(targetRange.Start.Character), int(targetRange.End.Character))
	}

	parts := make([]string, 0, endLine-startLine+1)
	parts = append(
		parts,
		sliceLineByUTF16Columns(
			lines[startLine],
			int(targetRange.Start.Character),
			utf16ColumnWidth(lines[startLine]),
		),
	)
	for lineIndex := startLine + 1; lineIndex < endLine; lineIndex++ {
		parts = append(parts, lines[lineIndex])
	}
	parts = append(parts, sliceLineByUTF16Columns(lines[endLine], 0, int(targetRange.End.Character)))

	return strings.Join(parts, "\n")
}

// sliceLineByUTF16Columns slices one line using UTF-16 column indexes from the language server.
func sliceLineByUTF16Columns(line string, startColumn, endColumn int) string {
	runes := []rune(line)
	startRuneIndex := utf16ColumnToRuneIndex(line, startColumn)
	endRuneIndex := utf16ColumnToRuneIndex(line, endColumn)
	if startRuneIndex < 0 {
		startRuneIndex = 0
	}
	if endRuneIndex < startRuneIndex {
		endRuneIndex = startRuneIndex
	}
	if endRuneIndex > len(runes) {
		endRuneIndex = len(runes)
	}

	return string(runes[startRuneIndex:endRuneIndex])
}

// utf16ColumnWidth returns the full UTF-16 width of one source line.
func utf16ColumnWidth(line string) int {
	width := 0
	for _, runeValue := range line {
		width += utf16.RuneLen(runeValue)
	}

	return width
}

// utf16ColumnToRuneIndex converts one UTF-16 column offset into the matching rune index.
func utf16ColumnToRuneIndex(line string, column int) int {
	if column <= 0 {
		return 0
	}

	runes := []rune(line)
	utf16Offset := 0
	for runeIndex, runeValue := range runes {
		runeWidth := utf16.RuneLen(runeValue)
		if utf16Offset+runeWidth > column {
			return runeIndex
		}
		utf16Offset += runeWidth
	}

	return len(runes)
}

// referenceEvidenceCandidateFromRange converts one LSP reference range into one evidence candidate.
func referenceEvidenceCandidateFromRange(
	workspaceRootPath string,
	relativePath string,
	targetRange protocol.Range,
	fileCache map[string]string,
) (referenceEvidenceCandidate, error) {
	startLine, endLine := inclusiveLineBounds(targetRange)
	snippet, contentStartLine, contentEndLine, err := readReferenceSnippet(
		workspaceRootPath,
		relativePath,
		startLine,
		fileCache,
	)
	if err != nil {
		return referenceEvidenceCandidate{}, err
	}

	return referenceEvidenceCandidate{
		StartLine:        startLine,
		EndLine:          endLine,
		ContentStartLine: contentStartLine,
		ContentEndLine:   contentEndLine,
		Column:           int(targetRange.Start.Character) + 1,
		Content:          snippet,
	}, nil
}
