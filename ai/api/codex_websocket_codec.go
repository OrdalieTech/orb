package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the handshake accept value.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const codexWebSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type codexWebSocket struct {
	connection io.ReadWriteCloser
	reader     *bufio.Reader
	cancel     context.CancelFunc
	writeMu    sync.Mutex
	stateMu    sync.Mutex
	closed     bool
}

type codexWebSocketConnectResult struct {
	response *http.Response
	err      error
}

func connectCodexWebSocket(
	ctx context.Context,
	endpoint string,
	headers http.Header,
	timeout time.Duration,
) (*codexWebSocket, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "wss":
		parsed.Scheme = "https"
	case "ws":
		parsed.Scheme = "http"
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("generate WebSocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	connectCtx, cancel := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(connectCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header = headers.Clone()
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", key)

	result := make(chan codexWebSocketConnectResult)
	abandoned := make(chan struct{})
	go func() {
		response, requestErr := openAIHTTPClient.Do(request)
		select {
		case result <- codexWebSocketConnectResult{response: response, err: requestErr}:
		case <-abandoned:
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()
	var timer *time.Timer
	var timeoutChannel <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutChannel = timer.C
	}
	if timer != nil {
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		cancel()
		close(abandoned)
		return nil, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
	case <-timeoutChannel:
		cancel()
		close(abandoned)
		return nil, fmt.Errorf("WebSocket connect timeout after %dms", timeout.Milliseconds()) //nolint:staticcheck // Upstream capitalization is observable.
	case completed := <-result:
		if completed.err != nil {
			cancel()
			return nil, completed.err
		}
		response := completed.response
		if response == nil || response.Body == nil {
			cancel()
			return nil, errors.New("WebSocket handshake returned no response") //nolint:staticcheck // Upstream capitalization is observable.
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			_ = response.Body.Close()
			cancel()
			return nil, fmt.Errorf("WebSocket handshake failed: %s", response.Status)
		}
		if !headerContainsToken(response.Header, "Connection", "upgrade") || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
			_ = response.Body.Close()
			cancel()
			return nil, errors.New("WebSocket handshake returned invalid upgrade headers") //nolint:staticcheck // Upstream capitalization is observable.
		}
		digest := sha1.Sum([]byte(key + codexWebSocketGUID)) //nolint:gosec // RFC 6455 handshake, not a security hash.
		wantAccept := base64.StdEncoding.EncodeToString(digest[:])
		if response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
			_ = response.Body.Close()
			cancel()
			return nil, errors.New("WebSocket handshake returned invalid accept value") //nolint:staticcheck // Upstream capitalization is observable.
		}
		connection, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			_ = response.Body.Close()
			cancel()
			return nil, errors.New("WebSocket transport is not writable") //nolint:staticcheck // Upstream capitalization is observable.
		}
		return &codexWebSocket{connection: connection, reader: bufio.NewReader(connection), cancel: cancel}, nil
	}
}

func headerContainsToken(headers http.Header, name, token string) bool {
	for _, value := range headers.Values(name) {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}

func (socket *codexWebSocket) IsOpen() bool {
	socket.stateMu.Lock()
	defer socket.stateMu.Unlock()
	return !socket.closed
}

func (socket *codexWebSocket) WriteText(contents []byte) error {
	return socket.writeFrame(0x1, contents)
}

func (socket *codexWebSocket) ReadMessage(ctx context.Context, idleTimeout time.Duration) ([]byte, error) {
	type readResult struct {
		contents []byte
		err      error
	}
	result := make(chan readResult, 1)
	go func() {
		contents, err := socket.readMessage()
		result <- readResult{contents: contents, err: err}
	}()
	var timer *time.Timer
	var timeoutChannel <-chan time.Time
	if idleTimeout > 0 {
		timer = time.NewTimer(idleTimeout)
		timeoutChannel = timer.C
	}
	if timer != nil {
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		_ = socket.forceClose()
		return nil, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
	case <-timeoutChannel:
		// Upstream closes with an explicit close frame (1000, "idle_timeout").
		_ = socket.Close(1000, "idle_timeout")
		return nil, fmt.Errorf("WebSocket idle timeout after %dms", idleTimeout.Milliseconds()) //nolint:staticcheck // Upstream capitalization is observable.
	case completed := <-result:
		return completed.contents, completed.err
	}
}

func (socket *codexWebSocket) Close(code uint16, reason string) error {
	socket.stateMu.Lock()
	if socket.closed {
		socket.stateMu.Unlock()
		return nil
	}
	socket.stateMu.Unlock()
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	payload = append(payload, reason...)
	if len(payload) > 125 {
		payload = payload[:125]
	}
	writeErr := socket.writeFrame(0x8, payload)
	closeErr := socket.forceClose()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (socket *codexWebSocket) forceClose() error {
	socket.stateMu.Lock()
	if socket.closed {
		socket.stateMu.Unlock()
		return nil
	}
	socket.closed = true
	socket.stateMu.Unlock()
	err := socket.connection.Close()
	socket.cancel()
	return err
}

func (socket *codexWebSocket) writeFrame(opcode byte, contents []byte) error {
	socket.writeMu.Lock()
	defer socket.writeMu.Unlock()
	socket.stateMu.Lock()
	closed := socket.closed
	socket.stateMu.Unlock()
	if closed {
		return errors.New("WebSocket is closed") //nolint:staticcheck // Upstream capitalization is observable.
	}
	if opcode >= 0x8 && len(contents) > 125 {
		return errors.New("WebSocket control frame is too large") //nolint:staticcheck // Upstream capitalization is observable.
	}
	frame := make([]byte, 0, len(contents)+14)
	frame = append(frame, 0x80|opcode)
	switch {
	case len(contents) <= 125:
		frame = append(frame, 0x80|byte(len(contents)))
	case len(contents) <= 0xffff:
		frame = append(frame, 0x80|126, byte(len(contents)>>8), byte(len(contents)))
	default:
		frame = append(frame, 0x80|127)
		length := uint64(len(contents))
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], length)
		frame = append(frame, encoded[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("generate WebSocket mask: %w", err)
	}
	frame = append(frame, mask[:]...)
	for index, value := range contents {
		frame = append(frame, value^mask[index%len(mask)])
	}
	for len(frame) > 0 {
		written, err := socket.connection.Write(frame)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func (socket *codexWebSocket) readMessage() ([]byte, error) {
	var message []byte
	var dataOpcode byte
	for {
		final, opcode, contents, err := socket.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x0:
			if dataOpcode == 0 {
				return nil, errors.New("WebSocket continuation without initial frame") //nolint:staticcheck // Upstream capitalization is observable.
			}
			message = append(message, contents...)
			if final {
				return message, nil
			}
		case 0x1, 0x2:
			if dataOpcode != 0 {
				return nil, errors.New("WebSocket data frame interrupted fragmented message") //nolint:staticcheck // Upstream capitalization is observable.
			}
			dataOpcode = opcode
			message = append(message, contents...)
			if final {
				return message, nil
			}
		case 0x8:
			return nil, parseCodexWebSocketClose(contents)
		case 0x9:
			if err := socket.writeFrame(0xa, contents); err != nil {
				return nil, err
			}
		case 0xa:
		default:
			return nil, fmt.Errorf("unsupported WebSocket opcode %d", opcode)
		}
	}
}

func (socket *codexWebSocket) readFrame() (bool, byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(socket.reader, header[:]); err != nil {
		return false, 0, nil, err
	}
	if header[0]&0x70 != 0 {
		return false, 0, nil, errors.New("WebSocket frame uses unsupported reserved bits") //nolint:staticcheck // Upstream capitalization is observable.
	}
	final, opcode := header[0]&0x80 != 0, header[0]&0x0f
	if header[1]&0x80 != 0 {
		return false, 0, nil, errors.New("WebSocket server frame must not be masked") //nolint:staticcheck // Upstream capitalization is observable.
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var encoded [2]byte
		if _, err := io.ReadFull(socket.reader, encoded[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(encoded[:]))
	case 127:
		var encoded [8]byte
		if _, err := io.ReadFull(socket.reader, encoded[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(encoded[:])
		if length>>63 != 0 {
			return false, 0, nil, errors.New("WebSocket frame length exceeds RFC 6455 limit") //nolint:staticcheck // Upstream capitalization is observable.
		}
	}
	if opcode >= 0x8 && (!final || length > 125) {
		return false, 0, nil, errors.New("invalid WebSocket control frame") //nolint:staticcheck // Upstream capitalization is observable.
	}
	if length > uint64(^uint(0)>>1) {
		return false, 0, nil, errors.New("WebSocket frame is too large") //nolint:staticcheck // Upstream capitalization is observable.
	}
	contents := make([]byte, int(length))
	if _, err := io.ReadFull(socket.reader, contents); err != nil {
		return false, 0, nil, err
	}
	return final, opcode, contents, nil
}

type codexWebSocketCloseError struct {
	code   uint16
	reason string
}

func (failure *codexWebSocketCloseError) Error() string {
	if failure.code == 0 {
		return "WebSocket closed"
	}
	reason := failure.reason
	if reason == "" && failure.code == 1009 {
		reason = "message too big"
	}
	if reason == "" {
		return fmt.Sprintf("WebSocket closed %d", failure.code)
	}
	return fmt.Sprintf("WebSocket closed %d %s", failure.code, reason)
}

func parseCodexWebSocketClose(contents []byte) error {
	if len(contents) == 0 {
		return &codexWebSocketCloseError{}
	}
	if len(contents) == 1 {
		return errors.New("invalid WebSocket close frame") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return &codexWebSocketCloseError{code: binary.BigEndian.Uint16(contents[:2]), reason: string(contents[2:])}
}
