package helpers

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
)

// referenceWorkflowSuite checks which open failures of the reference workflow file set end the request.
type referenceWorkflowSuite struct {
	suite.Suite
}

// TestReferenceWorkflow runs the checks of opening the reference workflow file set.
func TestReferenceWorkflow(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(referenceWorkflowSuite))
}

// TestUnreadableFileIsSkippedAndReported checks that an unreadable neighbor file is skipped and returned.
func (s *referenceWorkflowSuite) TestUnreadableFileIsSkippedAndReported() {
	readErr := &fs.PathError{
		Op:   "open",
		Path: "/root/b.php",
		Err:  fs.ErrPermission,
	}
	var openedInRun []string

	skipped, err := RunWithReferenceWorkflowFiles(
		s.T().Context(),
		nil,
		"/root",
		[]string{"a.php", "b.php", "target.php"},
		failingOpen("b.php", readErr),
		func(_ context.Context, opened []string, _ []SkippedFile) error {
			openedInRun = opened

			return nil
		},
	)
	s.Require().NoError(err)

	s.Equal([]string{"a.php", "target.php"}, openedInRun)
	s.Require().Len(skipped, 1)
	s.Equal("b.php", skipped[0].RelativePath)
	s.ErrorIs(skipped[0].Err, fs.ErrPermission)
}

// TestUnreadableTargetEndsRequest checks that the target file, which comes last, is never skipped.
func (s *referenceWorkflowSuite) TestUnreadableTargetEndsRequest() {
	readErr := &fs.PathError{
		Op:   "open",
		Path: "/root/target.php",
		Err:  fs.ErrPermission,
	}

	_, err := RunWithReferenceWorkflowFiles(
		s.T().Context(),
		nil,
		"/root",
		[]string{"a.php", "target.php"},
		failingOpen("target.php", readErr),
		func(context.Context, []string, []SkippedFile) error { return nil },
	)
	s.ErrorIs(err, fs.ErrPermission)
}

// TestNotificationErrorSkipsFile checks that any other error of opening a neighbor file also skips this file.
func (s *referenceWorkflowSuite) TestNotificationErrorSkipsFile() {
	notifyErr := errors.New("open request document: notification failed")

	skipped, err := RunWithReferenceWorkflowFiles(
		s.T().Context(),
		nil,
		"/root",
		[]string{"a.php", "target.php"},
		failingOpen("a.php", notifyErr),
		func(context.Context, []string, []SkippedFile) error { return nil },
	)
	s.Require().NoError(err)

	s.Require().Len(skipped, 1)
	s.Equal("a.php", skipped[0].RelativePath)
	s.ErrorIs(skipped[0].Err, notifyErr)
}

// TestEndedRequestContextEndsRequest checks that an open error after the request context ended is not skipped.
func (s *referenceWorkflowSuite) TestEndedRequestContextEndsRequest() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	_, err := RunWithReferenceWorkflowFiles(
		ctx,
		nil,
		"/root",
		[]string{"a.php", "target.php"},
		failingOpen("a.php", errors.New("open request document: notification failed")),
		func(context.Context, []string, []SkippedFile) error { return nil },
	)
	s.Error(err)
}

// TestUnavailableSessionEndsRequest checks that an open error that marks a failed session is not skipped.
func (s *referenceWorkflowSuite) TestUnavailableSessionEndsRequest() {
	sessionErr := NewSessionError(errors.New("server does not start"))

	_, err := RunWithReferenceWorkflowFiles(
		s.T().Context(),
		nil,
		"/root",
		[]string{"a.php", "target.php"},
		failingOpen("a.php", sessionErr),
		func(context.Context, []string, []SkippedFile) error { return nil },
	)
	s.ErrorIs(err, sessionErr)
}

// failingOpen returns err when the file with the given base name opens and runs the request for other files.
func failingOpen(
	baseName string,
	err error,
) func(context.Context, jsonrpc2.Conn, string, func(context.Context) error) error {
	return func(ctx context.Context, _ jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if filepath.Base(absolutePath) == baseName {
			return err
		}

		return run(ctx)
	}
}
