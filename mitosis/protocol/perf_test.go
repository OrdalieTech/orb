package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

type countingWriter struct {
	w      io.Writer
	writes atomic.Int64
	bytes  atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes.Add(1)
	n, err := c.w.Write(p)
	c.bytes.Add(int64(n))
	return n, err
}

type countingConn struct {
	net.Conn
	reads, writes     atomic.Int64
	readByt, writeByt atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	n, err := c.Conn.Read(p)
	c.readByt.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	n, err := c.Conn.Write(p)
	c.writeByt.Add(int64(n))
	return n, err
}

func benchPayload(size int) []byte {
	pad := strings.Repeat("a", max(0, size-64))
	raw, _ := json.Marshal(map[string]any{
		"session_id": "01a21719-c134-72d5-9bb3-0548c7bb95df",
		"data":       pad,
	})
	return raw
}

func BenchmarkWriteFrame(b *testing.B) {
	for _, size := range []int{128, 8 << 10, 256 << 10} {
		payload := benchPayload(size)
		b.Run(formatSize(size), func(b *testing.B) {
			cw := &countingWriter{w: io.Discard}
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := Write(cw, payload); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cw.writes.Load())/float64(b.N), "writes/op")
			b.ReportMetric(float64(cw.bytes.Load()-int64(b.N*len(payload)))/float64(b.N), "overhead-B/op")
		})
	}
}

func BenchmarkReadFrame(b *testing.B) {
	for _, size := range []int{128, 8 << 10, 256 << 10} {
		payload := benchPayload(size)
		var wire bytes.Buffer
		if err := Write(&wire, payload); err != nil {
			b.Fatal(err)
		}
		frameBytes := wire.Bytes()
		b.Run(formatSize(size), func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Read(bytes.NewReader(frameBytes)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateAndDecode(b *testing.B) {
	for _, size := range []int{128, 8 << 10, 256 << 10} {
		payload := benchPayload(size)
		b.Run("Validate/"+formatSize(size), func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := Validate(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("Decode/"+formatSize(size), func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var out struct {
					SessionID string `json:"session_id"`
					Data      string `json:"data"`
				}
				if err := Decode(payload, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRPCRoundTrip(b *testing.B) {
	for _, size := range []int{128, 8 << 10, 128 << 10} {
		payload := benchPayload(size)
		b.Run(formatSize(size), func(b *testing.B) {
			left, right := net.Pipe()
			cc := &countingConn{Conn: left}
			server := NewConn(right, func(_ context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
				var p struct {
					SessionID string `json:"session_id"`
					Data      string `json:"data"`
				}
				if err := Decode(params, &p); err != nil {
					return nil, err
				}
				return params, nil
			})
			client := NewConn(cc, nil)
			defer func() { _ = client.Close(); _ = server.Close() }()

			ctx := context.Background()
			var req map[string]any
			_ = json.Unmarshal(payload, &req)
			b.SetBytes(int64(len(payload) * 2))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var resp json.RawMessage
				if err := client.Call(ctx, "echo", req, &resp); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(cc.writes.Load())/float64(b.N), "writes/rpc")
			b.ReportMetric(float64(cc.writeByt.Load()+cc.readByt.Load())/float64(b.N), "wire-B/rpc")
		})
	}
}

func formatSize(n int) string {
	switch {
	case n >= 1<<20:
		return strings.TrimSuffix(json.Number(string(rune('0'+n/(1<<20)))).String(), "") + "MiB"
	case n >= 1<<10:
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(n / (1 << 10))
		return strings.TrimSpace(buf.String()) + "KiB"
	default:
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(n)
		return strings.TrimSpace(buf.String()) + "B"
	}
}
