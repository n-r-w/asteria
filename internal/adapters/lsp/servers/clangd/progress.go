package lspclangd

import (
	"context"
	"strings"
	"sync"

	"github.com/segmentio/encoding/json"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// backgroundIndexState describes clangd background index availability for reference completeness warnings.
type backgroundIndexState int

const (
	// backgroundIndexStateUnknown means clangd has not reported background-index progress for this workspace yet.
	backgroundIndexStateUnknown backgroundIndexState = iota
	// backgroundIndexStateIndexing means clangd is still building background-index data for this workspace.
	backgroundIndexStateIndexing
	// backgroundIndexStateIdle means clangd has ended all observed background-index progress tokens for this workspace.
	backgroundIndexStateIdle
)

// backgroundIndexProgress tracks clangd work-done progress tokens per workspace.
type backgroundIndexProgress struct {
	mu                      sync.Mutex
	stateByWorkspace        map[string]backgroundIndexState
	activeTokensByWorkspace map[string]map[string]struct{}
}

// progressParams contains the subset of LSP progress params needed to classify clangd indexing state.
type progressParams struct {
	Token json.RawMessage `json:"token"`
	Value progressValue   `json:"value"`
}

// progressValue contains the work-done progress fields emitted by clangd for background indexing.
type progressValue struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// newBackgroundIndexProgress creates an unknown-state tracker so callers warn until clangd reports idle.
func newBackgroundIndexProgress() *backgroundIndexProgress {
	return &backgroundIndexProgress{
		mu:                      sync.Mutex{},
		stateByWorkspace:        make(map[string]backgroundIndexState),
		activeTokensByWorkspace: make(map[string]map[string]struct{}),
	}
}

// handleCallback observes server progress notifications while leaving normal runtime handling unchanged.
func (p *backgroundIndexProgress) handleCallback(
	_ context.Context,
	_ jsonrpc2.Replier,
	req jsonrpc2.Request,
	workspaceRoot string,
) (bool, error) {
	if req.Method() != protocol.MethodProgress {
		return false, nil
	}

	p.recordProgress(workspaceRoot, req.Params())

	return false, nil
}

// incomplete reports whether reference results for one workspace should be marked as potentially incomplete.
func (p *backgroundIndexProgress) incomplete(workspaceRoot string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, ok := p.stateByWorkspace[workspaceRoot]
	if !ok {
		state = backgroundIndexStateUnknown
	}

	return state != backgroundIndexStateIdle
}

// recordProgress updates the tracked background-index state from one raw LSP progress notification.
func (p *backgroundIndexProgress) recordProgress(workspaceRoot string, rawParams []byte) {
	var params progressParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return
	}

	tokenKey := string(params.Token)

	p.mu.Lock()
	defer p.mu.Unlock()

	activeTokens := p.activeTokensByWorkspace[workspaceRoot]
	_, trackedToken := activeTokens[tokenKey]
	isIndexProgress := isBackgroundIndexProgress(params.Value)
	switch params.Value.Kind {
	case "begin", "report":
		if !trackedToken && !isIndexProgress {
			return
		}
		if activeTokens == nil {
			activeTokens = make(map[string]struct{})
			p.activeTokensByWorkspace[workspaceRoot] = activeTokens
		}
		activeTokens[tokenKey] = struct{}{}
		p.stateByWorkspace[workspaceRoot] = backgroundIndexStateIndexing
	case "end":
		if !trackedToken {
			return
		}
		delete(activeTokens, tokenKey)
		if len(activeTokens) == 0 {
			delete(p.activeTokensByWorkspace, workspaceRoot)
			p.stateByWorkspace[workspaceRoot] = backgroundIndexStateIdle
		}
	}
}

// isBackgroundIndexProgress identifies clangd indexing progress without depending on exact token names.
func isBackgroundIndexProgress(value progressValue) bool {
	progressText := strings.ToLower(value.Title + " " + value.Message)

	return strings.Contains(progressText, "index")
}
