package lsptsls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestShouldIgnoreDir keeps TypeScript traversal away from generated, vendored, and hidden directories.
func TestShouldIgnoreDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "node modules", input: "node_modules", expected: true},
		{name: "dist", input: "dist", expected: true},
		{name: "build", input: "build", expected: true},
		{name: "coverage", input: "coverage", expected: true},
		{name: "hidden", input: ".cache", expected: true},
		{name: "source", input: "src", expected: false},
		{name: "library", input: "lib", expected: false},
		{name: "utils", input: "utils", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, shouldIgnoreDir(testCase.input))
		})
	}
}

// TestLanguageIDForExtension keeps request-scoped didOpen notifications aligned with file type semantics.
func TestLanguageIDForExtension(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "ts", input: ".ts", expected: languageIDTypeScript},
		{name: "tsx", input: ".tsx", expected: languageIDTypeScriptReact},
		{name: "js", input: ".js", expected: languageIDJavaScript},
		{name: "jsx", input: ".jsx", expected: languageIDJavaScriptReact},
		{name: "mts", input: ".mts", expected: languageIDTypeScript},
		{name: "cts", input: ".cts", expected: languageIDTypeScript},
		{name: "mjs", input: ".mjs", expected: languageIDJavaScript},
		{name: "cjs", input: ".cjs", expected: languageIDJavaScript},
		{name: "unknown", input: ".unknown", expected: languageIDTypeScript},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, languageIDForExtension(testCase.input))
		})
	}
}

// TestCollectReferenceWorkflowFilesUsesTargetDirectoryScope proves that the tsls reference workflow
// keeps the target-directory open set local, sorted, and free from ignored or out-of-scope files.
func TestCollectReferenceWorkflowFilesUsesTargetDirectoryScope(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeWorkspaceFiles(t, workspaceRoot, map[string]string{
		"feature/fixture.ts":                  "export const fixture = 1\n",
		"feature/helper.js":                   "export const helper = 1\n",
		"feature/nested/secondary.ts":         "export const secondary = 1\n",
		"feature/node_modules/pkg/ignored.ts": "export const ignored = 1\n",
		"feature/.cache/ignored.ts":           "export const ignored = 1\n",
		"outside/outside.ts":                  "export const outside = 1\n",
	})

	files, err := collectReferenceWorkflowFiles(workspaceRoot, filepath.ToSlash(filepath.Join("feature", "fixture.ts")))
	require.NoError(t, err)
	require.Equal(t, []string{
		filepath.ToSlash(filepath.Join("feature", "helper.js")),
		filepath.ToSlash(filepath.Join("feature", "nested", "secondary.ts")),
		filepath.ToSlash(filepath.Join("feature", "fixture.ts")),
	}, files)
}

// TestRunWithReferenceWorkflowFilesReusesSharedLifecycle proves that the tsls outer workflow can reuse
// the same request-document lifecycle state that shared stdlsp requests use.
func TestRunWithReferenceWorkflowFilesReusesSharedLifecycle(t *testing.T) {
	t.Parallel()

	workspaceRoot, conn, recorder := newReferenceWorkflowTestEnv(t, map[string]string{
		"fixture.ts": "export const fixture = 1\n",
	})
	fixturePath := filepath.Join(workspaceRoot, "fixture.ts")
	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)

	err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"fixture.ts"},
		withRequestDocument,
		func(callCtx context.Context) error {
			return withRequestDocument(callCtx, conn, fixturePath, func(context.Context) error {
				return nil
			})
		},
	)
	require.NoError(t, err)
	waitForURIMethods(t, recorder, uri.File(fixturePath), []string{
		protocol.MethodTextDocumentDidOpen,
		protocol.MethodTextDocumentDocumentSymbol,
		protocol.MethodTextDocumentDidClose,
	})
}

// TestRunWithReferenceWorkflowFilesClosesOpenedFilesAfterRunError proves that the tsls outer workflow
// unwinds every already opened file when the wrapped shared workflow fails.
func TestRunWithReferenceWorkflowFilesClosesOpenedFilesAfterRunError(t *testing.T) {
	t.Parallel()

	workspaceRoot, conn, recorder := newReferenceWorkflowTestEnv(t, map[string]string{
		"helper.ts":  "export const helper = 1\n",
		"fixture.ts": "export const fixture = 1\n",
	})
	firstPath := filepath.Join(workspaceRoot, "helper.ts")
	secondPath := filepath.Join(workspaceRoot, "fixture.ts")
	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)
	expectedErr := errors.New("shared workflow failed")

	err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"helper.ts", "fixture.ts"},
		withRequestDocument,
		func(context.Context) error {
			return expectedErr
		},
	)
	require.ErrorIs(t, err, expectedErr)
	waitForEvents(t, recorder, []documentLifecycleEvent{
		{Method: protocol.MethodTextDocumentDidOpen, URI: uri.File(firstPath)},
		{Method: protocol.MethodTextDocumentDidOpen, URI: uri.File(secondPath)},
		{Method: protocol.MethodTextDocumentDocumentSymbol, URI: uri.File(firstPath)},
		{Method: protocol.MethodTextDocumentDocumentSymbol, URI: uri.File(secondPath)},
		{Method: protocol.MethodTextDocumentDidClose, URI: uri.File(secondPath)},
		{Method: protocol.MethodTextDocumentDidClose, URI: uri.File(firstPath)},
	})
}

// TestRunWithReferenceWorkflowFilesClosesOpenedFilesAfterOpenFailure proves that one later open failure
// still closes files that were already opened by the tsls outer workflow.
func TestRunWithReferenceWorkflowFilesClosesOpenedFilesAfterOpenFailure(t *testing.T) {
	t.Parallel()

	workspaceRoot, conn, recorder := newReferenceWorkflowTestEnv(t, map[string]string{
		"helper.ts": "export const helper = 1\n",
	})
	firstPath := filepath.Join(workspaceRoot, "helper.ts")
	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)

	err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"helper.ts", "missing.ts"},
		withRequestDocument,
		func(context.Context) error {
			return nil
		},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, fmt.Sprintf("read request document %q", filepath.Join(workspaceRoot, "missing.ts")))
	require.ErrorIs(t, err, os.ErrNotExist)
	waitForURIMethods(t, recorder, uri.File(firstPath), []string{protocol.MethodTextDocumentDidOpen, protocol.MethodTextDocumentDidClose})
}

// TestShouldRetryReferenceResult proves that tsls retries cross-file reference lookups only while results are
// still empty or target-file-only despite additional workflow files being open.
func TestShouldRetryReferenceResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                   string
		targetRelativePath     string
		referenceWorkflowFiles []string
		result                 domain.FindReferencingSymbolsResult
		expected               bool
	}{
		{
			name:                   "single target file does not retry",
			targetRelativePath:     "fixture.ts",
			referenceWorkflowFiles: []string{"fixture.ts"},
			result:                 domain.FindReferencingSymbolsResult{},
			expected:               false,
		},
		{
			name:                   "empty cross file result retries",
			targetRelativePath:     "fixture.ts",
			referenceWorkflowFiles: []string{"references.ts", "fixture.ts"},
			result:                 domain.FindReferencingSymbolsResult{},
			expected:               true,
		},
		{
			name:               "target file only result retries",
			targetRelativePath: "fixture.ts",
			referenceWorkflowFiles: []string{
				"references.ts",
				"fixture.ts",
			},
			result: domain.FindReferencingSymbolsResult{Symbols: []domain.ReferencingSymbol{{
				Kind:             0,
				Path:             "",
				File:             "fixture.ts",
				ContentStartLine: 0,
				ContentEndLine:   0,
				Content:          "",
			}}},
			expected: true,
		},
		{
			name:               "cross file result stops retry",
			targetRelativePath: "fixture.ts",
			referenceWorkflowFiles: []string{
				"references.ts",
				"fixture.ts",
			},
			result: domain.FindReferencingSymbolsResult{Symbols: []domain.ReferencingSymbol{{
				Kind:             0,
				Path:             "",
				File:             "references.ts",
				ContentStartLine: 0,
				ContentEndLine:   0,
				Content:          "",
			}}},
			expected: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(
				t,
				testCase.expected,
				shouldRetryReferenceResult(testCase.targetRelativePath, testCase.referenceWorkflowFiles, testCase.result),
			)
		})
	}
}

// documentLifecycleEvent records one didOpen or didClose notification for later assertions.
type documentLifecycleEvent struct {
	Method string
	URI    uri.URI
}

// lifecycleRecorder keeps JSON-RPC notification assertions deterministic across the recorder goroutine
// and the test goroutine.
type lifecycleRecorder struct {
	mu      sync.Mutex
	records []documentLifecycleEvent
}

// addEvent appends one protocol event in arrival order.
func (r *lifecycleRecorder) addEvent(event documentLifecycleEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, event)
}

// events returns a stable snapshot of all recorded events.
func (r *lifecycleRecorder) events() []documentLifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]documentLifecycleEvent(nil), r.records...)
}

// methodsForURI extracts the ordered methods recorded for one concrete URI.
func (r *lifecycleRecorder) methodsForURI(targetURI uri.URI) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	methods := make([]string, 0, len(r.records))
	for _, event := range r.records {
		if event.URI == targetURI {
			methods = append(methods, event.Method)
		}
	}

	return methods
}

// waitForURIMethods waits until the recorder sees the exact lifecycle sequence for one URI.
func waitForURIMethods(t *testing.T, recorder *lifecycleRecorder, targetURI uri.URI, expected []string) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(recorder.methodsForURI(targetURI), expected)
	}, time.Second, 10*time.Millisecond)
}

// waitForEvents waits until the recorder sees the exact full event stream.
func waitForEvents(t *testing.T, recorder *lifecycleRecorder, expected []documentLifecycleEvent) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(recorder.events(), expected)
	}, time.Second, 10*time.Millisecond)
}

// writeWorkspaceFiles keeps test setup focused on relevant files instead of repeated directory creation.
func writeWorkspaceFiles(t *testing.T, workspaceRoot string, files map[string]string) {
	t.Helper()

	for relativePath, content := range files {
		absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
		require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), 0o755))
		require.NoError(t, os.WriteFile(absolutePath, []byte(content), 0o600))
	}
}

// newReferenceWorkflowTestEnv keeps lifecycle tests focused on workflow assertions instead of repeated setup.
func newReferenceWorkflowTestEnv(t *testing.T, files map[string]string) (string, jsonrpc2.Conn, *lifecycleRecorder) {
	t.Helper()

	workspaceRoot := t.TempDir()
	writeWorkspaceFiles(t, workspaceRoot, files)
	conn, recorder := newLifecycleRecorderConn(t)

	return workspaceRoot, conn, recorder
}

// waitForConnDone keeps shutdown regressions local to the exact cleanup wait site.
func waitForConnDone(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s shutdown", name)
	}
}

// newLifecycleRecorderConn builds an in-memory JSON-RPC peer that records didOpen, documentSymbol, and didClose calls.
func newLifecycleRecorderConn(t *testing.T) (jsonrpc2.Conn, *lifecycleRecorder) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	recorder := &lifecycleRecorder{}

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodTextDocumentDidOpen:
			var params protocol.DidOpenTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(documentLifecycleEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return nil
		case protocol.MethodTextDocumentDidClose:
			var params protocol.DidCloseTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(documentLifecycleEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return nil
		case protocol.MethodTextDocumentDocumentSymbol:
			var params protocol.DocumentSymbolParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return reply(ctx, nil, err)
			}
			recorder.addEvent(documentLifecycleEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return reply(ctx, []json.RawMessage{}, nil)
		default:
			return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn, recorder
}
