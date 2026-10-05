//go:build js && wasm

package worker

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall/js"
)

// FetchTransport sends requests through the shim's fetch helpers
// (deploy/worker.mjs). net/http's js transport reads the request body into a
// growing buffer, crosses into JavaScript several times per header and makes
// two callbacks per body chunk; this one copies the body into JavaScript once,
// passes headers as one string each way and crosses twice per chunk. A body
// read also ends when the request's context does, as it would natively.
func FetchTransport(helpers js.Value) http.RoundTripper {
	return fetchTransport{fetch: helpers.Get("fetch"), read: helpers.Get("read"), release: helpers.Get("release")}
}

type fetchTransport struct{ fetch, read, release js.Value }

type fetchHead struct {
	status            int
	headers, location string // location: the final URL after redirects
	reader            js.Value
}

func (transport fetchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := fetchRequestBody(request)
	if err != nil {
		return nil, err
	}
	var lines []string
	for name, values := range request.Header {
		for _, value := range values {
			lines = append(lines, name, value)
		}
	}
	heads := make(chan fetchHead, 1)
	var done js.Func
	done = js.FuncOf(func(_ js.Value, args []js.Value) any {
		done.Release()
		head := fetchHead{status: args[0].Int(), headers: args[1].String()}
		if head.status != 0 {
			head.location, head.reader = args[2].String(), args[3]
		}
		heads <- head
		return nil
	})
	controller := transport.fetch.Invoke(request.URL.String(), request.Method, strings.Join(lines, "\n"), body, done)
	var head fetchHead
	select {
	case head = <-heads:
	case <-request.Context().Done():
		controller.Call("abort")
		if head := <-heads; head.reader.Truthy() {
			head.reader.Call("cancel")
		}
		return nil, request.Context().Err()
	}
	if head.status == 0 {
		return nil, errors.New(head.headers)
	}
	header := http.Header{}
	pairs := strings.Split(head.headers, "\n")
	for index := 0; index+1 < len(pairs); index += 2 {
		name := http.CanonicalHeaderKey(pairs[index])
		header[name] = append(header[name], pairs[index+1])
	}
	response := &http.Response{
		Status: strconv.Itoa(head.status) + " " + http.StatusText(head.status), StatusCode: head.status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: header, ContentLength: -1, Request: request, Body: http.NoBody,
	}
	if length := header.Get("Content-Length"); length != "" {
		if response.ContentLength, err = strconv.ParseInt(length, 10, 64); err != nil || response.ContentLength < 0 {
			if head.reader.Truthy() {
				head.reader.Call("cancel")
			}
			return nil, errors.New("net/http: ill-formed Content-Length header: " + length)
		}
	}
	// fetch has already decoded a gzip body.
	if strings.EqualFold(header.Get("Content-Encoding"), "gzip") {
		header.Del("Content-Encoding")
		header.Del("Content-Length")
		response.ContentLength, response.Uncompressed = -1, true
	}
	if head.location != "" && head.location != request.URL.String() {
		if location, err := url.Parse(head.location); err == nil {
			response.Request = request.Clone(request.Context())
			response.Request.URL = location
		}
	}
	if head.reader.Truthy() {
		response.Body = &fetchBody{transport: transport, request: request, controller: controller, reader: head.reader}
	}
	return response, nil
}

// fetchRequestBody copies the request body into a Uint8Array. A body that
// writes itself, as http.NewRequest's bytes bodies do, is copied straight
// from its own bytes into an array of its declared length.
func fetchRequestBody(request *http.Request) (js.Value, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return js.Null(), nil
	}
	defer request.Body.Close()
	if _, ok := request.Body.(io.WriterTo); ok && request.ContentLength > 0 {
		array := &jsArrayWriter{array: jsUint8Array.New(request.ContentLength)}
		if _, err := io.Copy(array, request.Body); err != nil {
			return js.Value{}, err
		}
		if array.offset != int(request.ContentLength) {
			return js.Value{}, errors.New("net/http: ContentLength does not match the body")
		}
		return array.array, nil
	}
	data, err := io.ReadAll(request.Body)
	if err != nil || len(data) == 0 {
		return js.Null(), err
	}
	return bytesToJS(data), nil
}

type jsArrayWriter struct {
	array  js.Value
	offset int
}

func (writer *jsArrayWriter) Write(data []byte) (int, error) {
	if writer.offset+len(data) > writer.array.Length() {
		return 0, errors.New("net/http: ContentLength does not match the body")
	}
	target := writer.array
	if writer.offset > 0 || len(data) < writer.array.Length() {
		target = writer.array.Call("subarray", writer.offset, writer.offset+len(data))
	}
	writer.offset += js.CopyBytesToJS(target, data)
	return len(data), nil
}

type fetchBody struct {
	transport  fetchTransport
	request    *http.Request
	controller js.Value
	reader     js.Value
	pending    []byte
	buffer     []byte
	err        error // sticky
}

func (body *fetchBody) Read(buffer []byte) (int, error) {
	for len(body.pending) == 0 {
		if body.err != nil {
			return 0, body.err
		}
		body.next()
	}
	written := copy(buffer, body.pending)
	body.pending = body.pending[written:]
	return written, nil
}

// next reads one chunk: bytes, null at the end, or an error message.
func (body *fetchBody) next() {
	chunks := make(chan js.Value, 1)
	done := js.FuncOf(func(_ js.Value, args []js.Value) any {
		chunks <- args[0]
		return nil
	})
	defer done.Release()
	body.transport.read.Invoke(body.reader, done)
	var chunk js.Value
	select {
	case chunk = <-chunks:
	case <-body.request.Context().Done():
		body.controller.Call("abort")
		<-chunks
		body.err = body.request.Context().Err()
		return
	}
	switch chunk.Type() {
	case js.TypeNull:
		body.err = io.EOF
	case js.TypeString:
		body.err = errors.New(chunk.String())
	default:
		body.buffer = slices.Grow(body.buffer[:0], chunk.Length())[:chunk.Length()]
		body.pending = body.buffer[:js.CopyBytesToGo(body.buffer, chunk)]
	}
}

var errFetchBodyClosed = errors.New("net/http: reader is closed")

func (body *fetchBody) Close() error {
	if body.err == nil {
		// A stream often ends just after its last event; release cancels
		// only a body that is still arriving.
		body.transport.release.Invoke(body.reader)
	}
	if body.err == nil || body.err == io.EOF {
		body.err = errFetchBodyClosed
	}
	return nil
}
