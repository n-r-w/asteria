package server

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/domain"
)

const (
	// serveTestToolTimeout is the tool deadline that a stopped server must not wait for.
	serveTestToolTimeout = time.Hour
	// serveTestBlockedCalls is the number of tool calls that run when the server stops.
	serveTestBlockedCalls = 2
)

// serveSuite checks how the MCP server stops while tool calls are still running.
type serveSuite struct {
	suite.Suite
}

// TestServeSuite checks that the MCP server stops running tool calls at once when its client input ends or its run
// context is canceled.
func TestServeSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(serveSuite))
}

// TestInputEndStopsRunningToolCalls proves that the end of client input cancels all running tool calls at once
// and lets the server return without waiting for the tool deadline.
func (s *serveSuite) TestInputEndStopsRunningToolCalls() {
	synctest.Test(s.T(), func(t *testing.T) {
		start := time.Now()
		run := startServeWithBlockedToolCalls(t, t.Context())

		require.NoError(t, run.clientOutput.Close())

		requireBlockedToolCallsStopped(t, run, start)
	})
}

// TestRunContextCancelStopsRunningToolCalls proves that a termination request cancels all running tool calls at once
// and lets the server return without waiting for the tool deadline.
func (s *serveSuite) TestRunContextCancelStopsRunningToolCalls() {
	synctest.Test(s.T(), func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(t.Context())
		defer cancelRun()
		start := time.Now()
		run := startServeWithBlockedToolCalls(t, runCtx)

		cancelRun()

		requireBlockedToolCallsStopped(t, run, start)
	})
}

// blockedServeRun holds the handles of a served MCP session whose tool calls block until their contexts end.
type blockedServeRun struct {
	// clientOutput is the client side of the server input stream.
	clientOutput io.WriteCloser
	// serveErr receives the result of serve.
	serveErr chan error
	// toolCallErrs receives the error of the context of each blocked tool call when this context ends.
	toolCallErrs chan error
	// releaseToolCalls lets the blocked tool calls return after the test saw their contexts end.
	releaseToolCalls chan struct{}
}

// startServeWithBlockedToolCalls serves one MCP session over pipes and returns after all its tool calls block.
func startServeWithBlockedToolCalls(t *testing.T, ctx context.Context) blockedServeRun {
	t.Helper()

	ctrl := gomock.NewController(t)
	search := NewMockILSP(ctrl)
	svc := New("v0.0.0", search, &config.Config{
		CacheRoot:              "",
		GetSymbolsOverviewDesc: "",
		FindSymbolDesc:         "",
		FindReferencesDesc:     "",
		ToolTimeout:            serveTestToolTimeout,
		LSPSessionIdleTimeout:  0,
		ToolOutputMaxBytes:     0,
		Log:                    config.LogConfig{},
		Adapters:               cfgadapters.Config{},
	})

	toolCallsStarted := make(chan struct{}, serveTestBlockedCalls)
	run := blockedServeRun{
		clientOutput:     nil,
		serveErr:         make(chan error, 1),
		toolCallErrs:     make(chan error, serveTestBlockedCalls),
		releaseToolCalls: make(chan struct{}),
	}
	search.EXPECT().GetSymbolsOverview(gomock.Any(), gomock.Any()).Times(serveTestBlockedCalls).DoAndReturn(
		func(ctx context.Context, _ *domain.GetSymbolsOverviewRequest) (domain.GetSymbolsOverviewResult, error) {
			toolCallsStarted <- struct{}{}
			<-ctx.Done()
			run.toolCallErrs <- ctx.Err()
			<-run.releaseToolCalls

			return domain.GetSymbolsOverviewResult{}, ctx.Err()
		},
	)

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	run.clientOutput = clientOutput

	go func() {
		run.serveErr <- svc.serve(ctx, &mcp.IOTransport{Reader: serverInput, Writer: serverOutput, MaxLineLength: 0})
	}()

	client := mcp.NewClient(
		&mcp.Implementation{ //nolint:exhaustruct_v5 // optional SDK fields are irrelevant for test transport setup
			Name:    "test-client",
			Version: "v0.0.0",
		},
		nil,
	)
	clientSession, err := client.Connect(
		t.Context(),
		&mcp.IOTransport{Reader: clientInput, Writer: clientOutput, MaxLineLength: 0},
		nil,
	)
	require.NoError(t, err)

	for range serveTestBlockedCalls {
		go func() {
			// The server stops before it answers, so the call result carries no information for the test.
			_, _ = clientSession.CallTool(
				t.Context(),
				&mcp.CallToolParams{ //nolint:exhaustruct_v5 // only tool name and arguments matter for the test
					Name: domain.ToolNameGetSymbolsOverview,
					Arguments: map[string]any{
						"workspace_root": "/tmp/workspace",
						"file_path":      "fixture.go",
					},
				},
			)
		}()
	}
	for range serveTestBlockedCalls {
		<-toolCallsStarted
	}
	// Tool registration sends a delayed list-changed notification; a real server sends it right after start.
	time.Sleep(time.Second)
	synctest.Wait()

	return run
}

// requireBlockedToolCallsStopped checks that the contexts of all blocked tool calls end by cancellation
// before any of the calls returns, and that serve then returns before the tool deadline.
func requireBlockedToolCallsStopped(t *testing.T, run blockedServeRun, start time.Time) {
	t.Helper()

	for range serveTestBlockedCalls {
		require.ErrorIs(t, <-run.toolCallErrs, context.Canceled)
	}
	close(run.releaseToolCalls)

	require.NoError(t, <-run.serveErr)
	require.Less(t, time.Since(start), serveTestToolTimeout)
}
