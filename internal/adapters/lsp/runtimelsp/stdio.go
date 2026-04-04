package runtimelsp

import (
	"errors"
	"io"
)

// stdioConn adapts child process pipes to the ReadWriteCloser shape required by jsonrpc2.
type stdioConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

var (
	_ io.ReadCloser  = (*stdioConn)(nil)
	_ io.WriteCloser = (*stdioConn)(nil)
)

// Read satisfies the JSON-RPC stream contract by exposing server stdout as incoming bytes.
func (c *stdioConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// Write satisfies the JSON-RPC stream contract by forwarding outgoing bytes to server stdin.
func (c *stdioConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

// Close shuts both pipes together because jsonrpc2 owns them as one bidirectional stream.
func (c *stdioConn) Close() error {
	return errors.Join(c.writer.Close(), c.reader.Close())
}
