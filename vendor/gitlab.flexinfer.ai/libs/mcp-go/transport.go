package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Transport defines the interface for MCP message transport.
type Transport interface {
	// Send sends a message over the transport.
	Send(ctx context.Context, msg *Message) error
	// Recv receives a message from the transport.
	Recv(ctx context.Context) (*Message, error)
	// Close closes the transport.
	Close() error
}

// recvResult carries a message or error from the background reader goroutine.
type recvResult struct {
	msg *Message
	err error
}

// StdioTransport implements MCP transport over stdio using newline-delimited JSON.
//
// MCP STDIO TRANSPORT SPECIFICATION:
// The MCP stdio transport uses newline-delimited JSON-RPC 2.0 messages.
// This is different from LSP which uses Content-Length headers.
//
// Message format:
//   - Each message is a single JSON object on one line
//   - Messages are terminated by newline (\n)
//   - Messages MUST NOT contain embedded newlines
//   - UTF-8 encoding is required
//
// Example message:
//
//	{"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}\n
//
// Reference: https://modelcontextprotocol.io/specification/2025-06-18/basic/transports
type StdioTransport struct {
	reader           *bufio.Reader
	writer           io.Writer
	rawReader        io.Reader // original reader, retained for closing
	writeMu          sync.Mutex
	useContentLength atomic.Bool // mirror client's framing on send
	done             chan struct{}
	closeOnce        sync.Once
	msgCh            chan recvResult // background reader publishes here
	readerWg         sync.WaitGroup  // tracks background reader goroutine
}

// NewStdioTransport creates a new stdio transport.
// A background goroutine is started to read messages from the reader.
// Call Close to stop the goroutine and release resources.
func NewStdioTransport(r io.Reader, w io.Writer) *StdioTransport {
	t := &StdioTransport{
		reader:    bufio.NewReader(r),
		writer:    w,
		rawReader: r,
		done:      make(chan struct{}),
		msgCh:     make(chan recvResult, 1),
	}
	t.readerWg.Add(1)
	go t.readLoop()
	return t
}

// Send sends a message, mirroring the client's framing style.
// If the client sent Content-Length headers, responses use the same framing.
// Otherwise, newline-delimited JSON is used.
func (t *StdioTransport) Send(ctx context.Context, msg *Message) error {
	select {
	case <-t.done:
		return fmt.Errorf("transport closed")
	default:
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	if t.useContentLength.Load() {
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
		if _, err := t.writer.Write([]byte(header)); err != nil {
			return fmt.Errorf("write header: %w", err)
		}
		if _, err := t.writer.Write(data); err != nil {
			return fmt.Errorf("write body: %w", err)
		}
	} else {
		if _, err := t.writer.Write(data); err != nil {
			return fmt.Errorf("write body: %w", err)
		}
		if _, err := t.writer.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write newline: %w", err)
		}
	}

	return nil
}

// Recv receives a message.
// Supports both Content-Length framing (LSP-style) and newline-delimited JSON.
// A background goroutine handles blocking reads, so context cancellation only
// aborts the current Recv call — the transport remains usable for subsequent calls.
// Call Close to permanently terminate the transport.
func (t *StdioTransport) Recv(ctx context.Context) (*Message, error) {
	select {
	case <-t.done:
		return nil, fmt.Errorf("transport closed")
	default:
	}

	select {
	case r := <-t.msgCh:
		return r.msg, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, fmt.Errorf("transport closed")
	}
}

// readLoop is a persistent goroutine that reads messages from the underlying
// reader and publishes them to msgCh. This decouples blocking I/O from
// context cancellation — canceling a Recv context no longer destroys the
// transport.
func (t *StdioTransport) readLoop() {
	defer t.readerWg.Done()
	for {
		msg, err := t.recvBlocking()
		select {
		case t.msgCh <- recvResult{msg, err}:
		case <-t.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// recvBlocking performs the blocking read without context awareness.
// Context/close handling is done by the caller via the done channel.
func (t *StdioTransport) recvBlocking() (*Message, error) {
	// Peek at the first few bytes to detect framing
	peek, _ := t.reader.Peek(14) // "Content-Length" is 14 chars

	if strings.HasPrefix(string(peek), "Content-Length") {
		t.useContentLength.Store(true)
		return t.recvContentLength()
	}

	return t.recvLine()
}

func (t *StdioTransport) recvContentLength() (*Message, error) {
	var contentLength int64
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			break
		}

		if strings.HasPrefix(line, "Content-Length: ") {
			if _, err := fmt.Sscanf(line, "Content-Length: %d", &contentLength); err != nil {
				return nil, fmt.Errorf("parse content length: %w", err)
			}
		}
	}

	if contentLength <= 0 {
		return nil, fmt.Errorf("invalid content length: %d", contentLength)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(t.reader, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

func (t *StdioTransport) recvLine() (*Message, error) {
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read message: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "{") {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal message: %w", err)
		}

		return &msg, nil
	}
}

// Close closes the transport, terminating any in-progress Recv.
// It closes the underlying reader and writer if they implement io.Closer,
// then waits for the background reader goroutine to exit.
// Close is safe to call multiple times.
func (t *StdioTransport) Close() error {
	var firstErr error
	t.closeOnce.Do(func() {
		close(t.done)
		if c, ok := t.rawReader.(io.Closer); ok {
			if err := c.Close(); err != nil {
				firstErr = err
			}
		}
		if c, ok := t.writer.(io.Closer); ok {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	})
	t.readerWg.Wait()
	return firstErr
}

// PipeTransport is an in-memory transport for testing.
type PipeTransport struct {
	incoming chan *Message
	outgoing chan *Message
	closed   bool
	mu       sync.Mutex
}

// NewPipeTransport creates a pair of connected pipe transports.
// Messages sent on one transport are received on the other.
func NewPipeTransport() (*PipeTransport, *PipeTransport) {
	ch1 := make(chan *Message, 16)
	ch2 := make(chan *Message, 16)

	t1 := &PipeTransport{incoming: ch1, outgoing: ch2}
	t2 := &PipeTransport{incoming: ch2, outgoing: ch1}

	return t1, t2
}

// Send sends a message to the connected transport.
func (t *PipeTransport) Send(ctx context.Context, msg *Message) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("transport closed")
	}
	t.mu.Unlock()

	select {
	case t.outgoing <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recv receives a message from the connected transport.
func (t *PipeTransport) Recv(ctx context.Context) (*Message, error) {
	select {
	case msg, ok := <-t.incoming:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the transport.
func (t *PipeTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closed = true
		close(t.outgoing)
	}
	return nil
}
