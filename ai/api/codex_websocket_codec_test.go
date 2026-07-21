package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Test mirrors the RFC 6455 handshake.
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type codexCodecBuffer struct {
	mu     sync.Mutex
	reader *bytes.Reader
	writes bytes.Buffer
	closed bool
}

func (buffer *codexCodecBuffer) Read(contents []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.reader.Read(contents)
}

func (buffer *codexCodecBuffer) Write(contents []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.writes.Write(contents)
}

func (buffer *codexCodecBuffer) Close() error {
	buffer.mu.Lock()
	buffer.closed = true
	buffer.mu.Unlock()
	return nil
}

func (buffer *codexCodecBuffer) written() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.writes.Bytes()...)
}

func TestCodexWebSocketUpgradeUsesRFC6455Headers(t *testing.T) {
	connection := &codexCodecBuffer{reader: bytes.NewReader(nil)}
	var request *http.Request
	withCodexHTTPClient(t, func(got *http.Request) (*http.Response, error) {
		request = got.Clone(got.Context())
		digest := sha1.Sum([]byte(got.Header.Get("Sec-WebSocket-Key") + codexWebSocketGUID)) //nolint:gosec // RFC 6455 handshake.
		responseHeaders := make(http.Header)
		responseHeaders.Set("Connection", "keep-alive, Upgrade")
		responseHeaders.Set("Upgrade", "websocket")
		responseHeaders.Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(digest[:]))
		return &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Status:     "101 Switching Protocols",
			Header:     responseHeaders,
			Body:       connection,
		}, nil
	})
	headers := http.Header{"Authorization": []string{"Bearer fixture"}}
	socket, err := connectCodexWebSocket(context.Background(), "wss://example.test/codex/responses", headers, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if request == nil || request.URL.Scheme != "https" || request.URL.Host != "example.test" || request.Header.Get("Connection") != "Upgrade" || request.Header.Get("Upgrade") != "websocket" || request.Header.Get("Sec-WebSocket-Version") != "13" || request.Header.Get("Authorization") != "Bearer fixture" || request.Header.Get("Sec-WebSocket-Key") == "" {
		t.Fatalf("upgrade request = %#v", request)
	}
	if err := socket.forceClose(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexWebSocketWritesMaskedClientFramesAtAllLengths(t *testing.T) {
	for _, size := range []int{5, 130, 70_000} {
		t.Run(strings.Repeat("x", min(size, 16)), func(t *testing.T) {
			connection := &codexCodecBuffer{reader: bytes.NewReader(nil)}
			socket := &codexWebSocket{connection: connection, reader: bufioNewReader(connection), cancel: func() {}}
			contents := bytes.Repeat([]byte{'a'}, size)
			if err := socket.WriteText(contents); err != nil {
				t.Fatal(err)
			}
			opcode, decoded, consumed, err := decodeCodexClientFrame(connection.written())
			if err != nil {
				t.Fatal(err)
			}
			if opcode != 0x1 || consumed != len(connection.written()) || !bytes.Equal(decoded, contents) {
				t.Fatalf("opcode=%d consumed=%d/%d decoded=%d", opcode, consumed, len(connection.written()), len(decoded))
			}
		})
	}
}

func TestCodexWebSocketReadsFragmentedMessageAndAnswersPing(t *testing.T) {
	frames := append(codexServerFrame(false, 0x1, []byte("hel")), codexServerFrame(true, 0x9, []byte("ping"))...)
	frames = append(frames, codexServerFrame(true, 0x0, []byte("lo"))...)
	connection := &codexCodecBuffer{reader: bytes.NewReader(frames)}
	socket := &codexWebSocket{connection: connection, reader: bufioNewReader(connection), cancel: func() {}}
	message, err := socket.ReadMessage(context.Background(), 0)
	if err != nil || string(message) != "hello" {
		t.Fatalf("message = %q, %v", message, err)
	}
	opcode, payload, _, err := decodeCodexClientFrame(connection.written())
	if err != nil || opcode != 0xa || string(payload) != "ping" {
		t.Fatalf("pong opcode=%d payload=%q err=%v", opcode, payload, err)
	}
}

func TestCodexWebSocketCloseAndIdleErrors(t *testing.T) {
	closeFrame := codexServerFrame(true, 0x8, append([]byte{0x03, 0xf1}, []byte{}...))
	connection := &codexCodecBuffer{reader: bytes.NewReader(closeFrame)}
	socket := &codexWebSocket{connection: connection, reader: bufioNewReader(connection), cancel: func() {}}
	if _, err := socket.ReadMessage(context.Background(), 0); err == nil || err.Error() != "WebSocket closed 1009 message too big" {
		t.Fatalf("close error = %v", err)
	}

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	written := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 256)
		read, _ := server.Read(buffer)
		written <- buffer[:read]
	}()
	idleSocket := &codexWebSocket{connection: client, reader: bufioNewReader(client), cancel: func() {}}
	if _, err := idleSocket.ReadMessage(context.Background(), 5*time.Millisecond); err == nil || err.Error() != "WebSocket idle timeout after 5ms" {
		t.Fatalf("idle error = %v", err)
	}
	// CX-m4: the idle timeout must send a close frame 1000 "idle_timeout"
	// before dropping the connection, matching upstream closeWebSocketSilently.
	frame := <-written
	opcode, payload, _, err := decodeCodexClientFrame(frame)
	if err != nil || opcode != 0x8 || len(payload) < 2 || binary.BigEndian.Uint16(payload[:2]) != 1000 || string(payload[2:]) != "idle_timeout" {
		t.Fatalf("idle close frame opcode=%d payload=%q err=%v", opcode, payload, err)
	}
}

func bufioNewReader(reader io.Reader) *bufio.Reader { return bufio.NewReader(reader) }

func codexServerFrame(final bool, opcode byte, contents []byte) []byte {
	first := opcode
	if final {
		first |= 0x80
	}
	frame := []byte{first}
	switch {
	case len(contents) <= 125:
		frame = append(frame, byte(len(contents)))
	case len(contents) <= 0xffff:
		frame = append(frame, 126, byte(len(contents)>>8), byte(len(contents)))
	default:
		frame = append(frame, 127)
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(len(contents)))
		frame = append(frame, encoded[:]...)
	}
	return append(frame, contents...)
}

func decodeCodexClientFrame(frame []byte) (byte, []byte, int, error) {
	if len(frame) < 6 || frame[1]&0x80 == 0 {
		return 0, nil, 0, io.ErrUnexpectedEOF
	}
	opcode := frame[0] & 0x0f
	length := int(frame[1] & 0x7f)
	offset := 2
	switch length {
	case 126:
		if len(frame) < offset+2 {
			return 0, nil, 0, io.ErrUnexpectedEOF
		}
		length = int(binary.BigEndian.Uint16(frame[offset : offset+2]))
		offset += 2
	case 127:
		if len(frame) < offset+8 {
			return 0, nil, 0, io.ErrUnexpectedEOF
		}
		length = int(binary.BigEndian.Uint64(frame[offset : offset+8]))
		offset += 8
	}
	if len(frame) < offset+4+length {
		return 0, nil, 0, io.ErrUnexpectedEOF
	}
	mask := frame[offset : offset+4]
	offset += 4
	contents := make([]byte, length)
	for index := range contents {
		contents[index] = frame[offset+index] ^ mask[index%len(mask)]
	}
	return opcode, contents, offset + length, nil
}
