package jsbridge

// Node built-in shims (node:crypto, node:http, node:module), web encoding
// globals (atob/btoa/TextDecoder), and Node-style fs errors. Upstream runs
// extensions under real Node (jiti) where all of this exists natively; the
// surface here covers what real ecosystem extensions touch, backed by Go.

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // Node parity: extensions may ask for md5.
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // Node parity: extensions may ask for sha1.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/grafana/sobek"
	"github.com/rivo/uniseg"
)

// --- Node-style fs errors ---

// nodeFSErrorValue builds a Node-shaped error (code/errno/syscall/path and a
// Node-format message) from a Go filesystem error so the standard
// err.code === "ENOENT" idiom works. Unrecognized errors keep the previous
// plain-TypeError behavior.
func nodeFSErrorValue(rt *sobek.Runtime, err error, syscallName, path string) sobek.Value {
	var code, description string
	var errno int
	switch {
	case errors.Is(err, os.ErrNotExist):
		code, errno, description = "ENOENT", int(syscall.ENOENT), "no such file or directory"
	case errors.Is(err, os.ErrExist):
		code, errno, description = "EEXIST", int(syscall.EEXIST), "file already exists"
	case errors.Is(err, os.ErrPermission):
		code, errno, description = "EACCES", int(syscall.EACCES), "permission denied"
	case errors.Is(err, syscall.EISDIR):
		code, errno, description = "EISDIR", int(syscall.EISDIR), "illegal operation on a directory"
	case errors.Is(err, syscall.ENOTDIR):
		code, errno, description = "ENOTDIR", int(syscall.ENOTDIR), "not a directory"
	default:
		return rt.NewTypeError(err.Error())
	}
	message := fmt.Sprintf("%s: %s, %s '%s'", code, description, syscallName, path)
	errorObject, newErr := rt.New(rt.Get("Error"), rt.ToValue(message))
	if newErr != nil {
		return rt.NewTypeError(message)
	}
	mustSet(rt, errorObject, "code", code)
	mustSet(rt, errorObject, "errno", -errno)
	mustSet(rt, errorObject, "syscall", syscallName)
	mustSet(rt, errorObject, "path", path)
	return errorObject
}

func panicFS(rt *sobek.Runtime, err error, syscallName, path string) {
	panic(nodeFSErrorValue(rt, err, syscallName, path))
}

func rejectFS(rt *sobek.Runtime, err error, syscallName, path string) sobek.Value {
	promise, _, reject := rt.NewPromise()
	_ = reject(nodeFSErrorValue(rt, err, syscallName, path))
	return rt.ToValue(promise)
}

// --- atob / btoa / TextDecoder globals ---

func installEncodingGlobals(rt *sobek.Runtime) error {
	if err := rt.Set("btoa", func(call sobek.FunctionCall) sobek.Value {
		input := call.Argument(0).String()
		bytes := make([]byte, 0, len(input))
		for _, character := range input {
			if character > 0xff {
				panic(rt.NewTypeError("InvalidCharacterError: The string to be encoded contains characters outside of the Latin1 range."))
			}
			bytes = append(bytes, byte(character))
		}
		return rt.ToValue(base64.StdEncoding.EncodeToString(bytes))
	}); err != nil {
		return err
	}
	if err := rt.Set("atob", func(call sobek.FunctionCall) sobek.Value {
		input := strings.Map(func(character rune) rune {
			switch character {
			case '\t', '\n', '\f', '\r', ' ':
				return -1
			}
			return character
		}, call.Argument(0).String())
		decoded, err := base64.StdEncoding.DecodeString(input)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(input)
		}
		if err != nil {
			panic(rt.NewTypeError("InvalidCharacterError: The string to be decoded is not correctly encoded."))
		}
		characters := make([]rune, len(decoded))
		for index, value := range decoded {
			characters[index] = rune(value)
		}
		return rt.ToValue(string(characters))
	}); err != nil {
		return err
	}
	// structuredClone is a standard global under Node; real packages use it
	// for JSON-safe deep copies (@zigai/pi-extension-settings cloneJson).
	if _, err := rt.RunString(`globalThis.structuredClone = globalThis.structuredClone || function structuredClone(value) {
	"use strict";
	var seen = new Map();
	function clone(input) {
		if (input === null || (typeof input !== "object")) {
			if (typeof input === "function" || typeof input === "symbol") {
				throw new TypeError("DataCloneError: " + String(input) + " could not be cloned.");
			}
			return input;
		}
		if (seen.has(input)) { return seen.get(input); }
		if (input instanceof Date) { return new Date(input.getTime()); }
		if (input instanceof RegExp) { return new RegExp(input.source, input.flags); }
		if (Array.isArray(input)) {
			var array = [];
			seen.set(input, array);
			for (var index = 0; index < input.length; index++) { array[index] = clone(input[index]); }
			return array;
		}
		if (input instanceof Map) {
			var map = new Map();
			seen.set(input, map);
			input.forEach(function (item, key) { map.set(clone(key), clone(item)); });
			return map;
		}
		if (input instanceof Set) {
			var set = new Set();
			seen.set(input, set);
			input.forEach(function (item) { set.add(clone(item)); });
			return set;
		}
		var output = {};
		seen.set(input, output);
		var keys = Object.keys(input);
		for (var position = 0; position < keys.length; position++) {
			output[keys[position]] = clone(input[keys[position]]);
		}
		return output;
	}
	return clone(value);
};`); err != nil {
		return err
	}
	if err := installIntlSegmenter(rt); err != nil {
		return err
	}
	return rt.Set("TextDecoder", func(call sobek.ConstructorCall) *sobek.Object {
		fatal := false
		var pending []byte
		if options := call.Argument(1); present(options) {
			value := options.ToObject(rt).Get("fatal")
			fatal = present(value) && value.ToBoolean()
		}
		mustSet(rt, call.This, "decode", func(inner sobek.FunctionCall) sobek.Value {
			input := inner.Argument(0)
			var chunk []byte
			if present(input) {
				switch exported := input.Export().(type) {
				case []byte:
					chunk = exported
				case sobek.ArrayBuffer:
					chunk = exported.Bytes()
				case string:
					chunk = []byte(exported)
				default:
					chunk = exportBytes(rt, input)
				}
			}
			decoded := make([]byte, 0, len(pending)+len(chunk))
			decoded = append(decoded, pending...)
			decoded = append(decoded, chunk...)
			stream := false
			if options := inner.Argument(1); present(options) {
				value := options.ToObject(rt).Get("stream")
				stream = present(value) && value.ToBoolean()
			}
			if stream {
				prefix, remainder, invalid := splitStreamingUTF8(decoded)
				pending = append(pending[:0], remainder...)
				if fatal && invalid {
					panic(rt.NewTypeError("The encoded data was not valid for encoding utf-8"))
				}
				return rt.ToValue(string([]rune(string(prefix))))
			}
			pending = nil
			if fatal && !utf8.Valid(decoded) {
				panic(rt.NewTypeError("The encoded data was not valid for encoding utf-8"))
			}
			return rt.ToValue(string([]rune(string(decoded))))
		})
		return nil
	})
}

func splitStreamingUTF8(data []byte) (prefix, remainder []byte, invalid bool) {
	for index := 0; index < len(data); {
		if data[index] < utf8.RuneSelf {
			index++
			continue
		}
		r, size := utf8.DecodeRune(data[index:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data[index:]) {
				return data[:index], data[index:], invalid
			}
			invalid = true
		}
		index += size
	}
	return data, nil, invalid
}

type segmenterEntry struct {
	value string
	index int
	end   int
}

func installIntlSegmenter(rt *sobek.Runtime) error {
	intl := rt.NewObject()
	if current := rt.Get("Intl"); present(current) {
		intl = current.ToObject(rt)
	} else if err := rt.Set("Intl", intl); err != nil {
		return err
	}
	constructor := func(call sobek.ConstructorCall) *sobek.Object {
		granularity := "grapheme"
		if options := call.Argument(1); present(options) {
			if value := options.ToObject(rt).Get("granularity"); present(value) {
				granularity = value.String()
			}
		}
		if granularity != "grapheme" {
			panic(rt.NewTypeError("Intl.Segmenter granularity %q is not supported", granularity))
		}
		mustSet(rt, call.This, "segment", func(inner sobek.FunctionCall) sobek.Value {
			input := inner.Argument(0).String()
			entries := make([]segmenterEntry, 0, len(input))
			segments := uniseg.NewGraphemes(input)
			index := 0
			for segments.Next() {
				value := segments.Str()
				end := index + utf16Length(value)
				entries = append(entries, segmenterEntry{value: value, index: index, end: end})
				index = end
			}
			values := make([]any, len(entries))
			for position, entry := range entries {
				values[position] = segmenterEntryObject(rt, input, entry)
			}
			result := rt.NewArray(values...)
			mustSet(rt, result, "containing", func(lookup sobek.FunctionCall) sobek.Value {
				offset := int(lookup.Argument(0).ToInteger())
				for _, entry := range entries {
					if offset >= entry.index && offset < entry.end {
						return segmenterEntryObject(rt, input, entry)
					}
				}
				return sobek.Undefined()
			})
			return result
		})
		mustSet(rt, call.This, "resolvedOptions", func(sobek.FunctionCall) sobek.Value {
			return toJS(rt, map[string]any{"locale": "en-US", "granularity": granularity})
		})
		return nil
	}
	return intl.Set("Segmenter", constructor)
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length += utf16.RuneLen(character)
	}
	return length
}

func segmenterEntryObject(rt *sobek.Runtime, input string, entry segmenterEntry) *sobek.Object {
	value := rt.NewObject()
	mustSet(rt, value, "segment", entry.value)
	mustSet(rt, value, "index", entry.index)
	mustSet(rt, value, "input", input)
	return value
}

// --- node:crypto ---

func newCryptoDigest(rt *sobek.Runtime, algorithm string) hash.Hash {
	switch strings.ReplaceAll(strings.ToLower(algorithm), "-", "") {
	case "sha256":
		return sha256.New()
	case "sha1":
		return sha1.New() //nolint:gosec // Node parity.
	case "sha512":
		return sha512.New()
	case "sha384":
		return sha512.New384()
	case "md5":
		return md5.New() //nolint:gosec // Node parity.
	default:
		panic(rt.NewTypeError("Digest method not supported: %q", algorithm))
	}
}

func newDigestObject(rt *sobek.Runtime, digest hash.Hash) *sobek.Object {
	object := rt.NewObject()
	mustSet(rt, object, "update", func(call sobek.FunctionCall) sobek.Value {
		digest.Write(jsBytes(call.Argument(0)))
		return object
	})
	mustSet(rt, object, "digest", func(call sobek.FunctionCall) sobek.Value {
		sum := digest.Sum(nil)
		if present(call.Argument(0)) {
			switch encoding := call.Argument(0).String(); encoding {
			case "hex":
				return rt.ToValue(hex.EncodeToString(sum))
			case "base64":
				return rt.ToValue(base64.StdEncoding.EncodeToString(sum))
			case "base64url":
				return rt.ToValue(base64.RawURLEncoding.EncodeToString(sum))
			default:
				panic(rt.NewTypeError("Unsupported digest encoding %q", encoding))
			}
		}
		return newBufferValue(rt, sum)
	})
	return object
}

func newCryptoModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "randomUUID", func(sobek.FunctionCall) sobek.Value {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			panic(rt.NewGoError(err))
		}
		raw[6] = (raw[6] & 0x0f) | 0x40
		raw[8] = (raw[8] & 0x3f) | 0x80
		encoded := hex.EncodeToString(raw[:])
		return rt.ToValue(encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:])
	})
	mustSet(rt, m, "randomBytes", func(call sobek.FunctionCall) sobek.Value {
		size := int(call.Argument(0).ToInteger())
		if size < 0 {
			panic(rt.NewTypeError("randomBytes size must be non-negative"))
		}
		bytes := make([]byte, size)
		if _, err := rand.Read(bytes); err != nil {
			panic(rt.NewGoError(err))
		}
		return newBufferValue(rt, bytes)
	})
	mustSet(rt, m, "createHash", func(call sobek.FunctionCall) sobek.Value {
		return newDigestObject(rt, newCryptoDigest(rt, call.Argument(0).String()))
	})
	mustSet(rt, m, "createHmac", func(call sobek.FunctionCall) sobek.Value {
		algorithm := call.Argument(0).String()
		key := jsBytes(call.Argument(1))
		mac := hmac.New(func() hash.Hash { return newCryptoDigest(rt, algorithm) }, key)
		return newDigestObject(rt, mac)
	})
	return m
}

// --- node:module ---

func newModuleModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "createRequire", func(call sobek.FunctionCall) sobek.Value {
		base := call.Argument(0).String()
		globalRequire, ok := sobek.AssertFunction(rt.Get("require"))
		if !ok {
			panic(rt.NewTypeError("extension module loader is unavailable"))
		}
		localRequire := rt.ToValue(func(inner sobek.FunctionCall) sobek.Value {
			value, err := globalRequire(sobek.Undefined(), inner.Arguments...)
			if err != nil {
				panic(err)
			}
			return value
		}).ToObject(rt)
		mustSet(rt, localRequire, "resolve", func(inner sobek.FunctionCall) sobek.Value {
			resolver, ok := sobek.AssertFunction(rt.Get("__piGoRequireResolve"))
			if !ok {
				panic(rt.NewTypeError("require.resolve is unavailable"))
			}
			value, err := resolver(sobek.Undefined(), inner.Argument(0), rt.ToValue(base))
			if err != nil {
				panic(err)
			}
			return value
		})
		return localRequire
	})
	return m
}

// --- node:http (minimal: createServer/listen/close/address, request/get) ---

type httpShimReply struct {
	status  int
	headers http.Header
	body    []byte
}

func (h *shimHost) newHTTPModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "createServer", func(call sobek.FunctionCall) sobek.Value {
		handler, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			panic(rt.NewTypeError("http.createServer requires a request handler"))
		}
		return h.newHTTPServerObject(rt, handler)
	})
	request := func(call sobek.FunctionCall, autoEnd bool) sobek.Value {
		return h.newHTTPClientRequest(rt, call, "http:", autoEnd)
	}
	mustSet(rt, m, "request", func(call sobek.FunctionCall) sobek.Value { return request(call, false) })
	mustSet(rt, m, "get", func(call sobek.FunctionCall) sobek.Value { return request(call, true) })
	return m
}

// newHTTPSModule shares the http client (Go's net/http speaks TLS); only the
// default protocol differs. No TLS server surface.
func (h *shimHost) newHTTPSModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	request := func(call sobek.FunctionCall, autoEnd bool) sobek.Value {
		return h.newHTTPClientRequest(rt, call, "https:", autoEnd)
	}
	mustSet(rt, m, "request", func(call sobek.FunctionCall) sobek.Value { return request(call, false) })
	mustSet(rt, m, "get", func(call sobek.FunctionCall) sobek.Value { return request(call, true) })
	return m
}

func (h *shimHost) newHTTPServerObject(rt *sobek.Runtime, handler sobek.Callable) *sobek.Object {
	server := rt.NewObject()
	var listener net.Listener
	opID := h.vm.allocID()

	goHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, int64(fetchMaxResponseBody)))
		method, target := request.Method, request.RequestURI
		headers := make(map[string]any, len(request.Header))
		for name, values := range request.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		reply := make(chan httpShimReply, 1)
		posted := h.vm.post(h.vm.rootCtx, true, func(rt *sobek.Runtime) error {
			requestObject := rt.NewObject()
			mustSet(rt, requestObject, "method", method)
			mustSet(rt, requestObject, "url", target)
			mustSet(rt, requestObject, "headers", toJS(rt, headers))
			installImmediateBodyEvents(rt, requestObject, body)
			_, err := handler(sobek.Undefined(), requestObject, newHTTPServerResponse(rt, reply))
			return err
		})
		if !posted {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		select {
		case response := <-reply:
			for name, values := range response.headers {
				for _, value := range values {
					writer.Header().Add(name, value)
				}
			}
			writer.WriteHeader(response.status)
			_, _ = writer.Write(response.body)
		case <-request.Context().Done():
		case <-h.vm.stop:
		}
	})

	mustSet(rt, server, "listen", func(call sobek.FunctionCall) sobek.Value {
		port, host := 0, ""
		var callback sobek.Callable
		for _, argument := range call.Arguments {
			if fn, ok := sobek.AssertFunction(argument); ok {
				callback = fn
				continue
			}
			switch value := argument.Export().(type) {
			case int64:
				port = int(value)
			case float64:
				port = int(value)
			case string:
				host = value
			}
		}
		created, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			panic(rt.NewGoError(err))
		}
		listener = created
		h.vm.mu.Lock()
		h.vm.ops[opID] = func() { _ = created.Close() }
		h.vm.mu.Unlock()
		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			_ = http.Serve(created, goHandler) //nolint:gosec // Extension-local server, closed with the VM.
		}()
		if callback != nil {
			h.vm.postCallback(func(*sobek.Runtime) { _, _ = callback(server) })
		}
		return server
	})
	mustSet(rt, server, "address", func(sobek.FunctionCall) sobek.Value {
		if listener == nil {
			return sobek.Null()
		}
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			return sobek.Null()
		}
		object := rt.NewObject()
		mustSet(rt, object, "port", address.Port)
		mustSet(rt, object, "address", address.IP.String())
		mustSet(rt, object, "family", "IPv4")
		return object
	})
	mustSet(rt, server, "close", func(call sobek.FunctionCall) sobek.Value {
		h.vm.cancelOp(opID)
		if callback, ok := sobek.AssertFunction(call.Argument(0)); ok {
			h.vm.postCallback(func(*sobek.Runtime) { _, _ = callback(sobek.Undefined()) })
		}
		return server
	})
	mustSet(rt, server, "on", func(sobek.FunctionCall) sobek.Value { return server })
	return server
}

func newHTTPServerResponse(rt *sobek.Runtime, reply chan<- httpShimReply) *sobek.Object {
	response := rt.NewObject()
	headers := make(http.Header)
	var body []byte
	status := 0
	ended := false
	mustSet(rt, response, "statusCode", 200)
	mustSet(rt, response, "setHeader", func(call sobek.FunctionCall) sobek.Value {
		headers.Set(call.Argument(0).String(), call.Argument(1).String())
		return response
	})
	mustSet(rt, response, "writeHead", func(call sobek.FunctionCall) sobek.Value {
		status = int(call.Argument(0).ToInteger())
		if present(call.Argument(1)) {
			parseHeaders(rt, call.Argument(1), headers)
		}
		return response
	})
	mustSet(rt, response, "write", func(call sobek.FunctionCall) sobek.Value {
		body = append(body, exportBytes(rt, call.Argument(0))...)
		return rt.ToValue(true)
	})
	mustSet(rt, response, "end", func(call sobek.FunctionCall) sobek.Value {
		if ended {
			return response
		}
		ended = true
		if present(call.Argument(0)) {
			if _, isFunction := sobek.AssertFunction(call.Argument(0)); !isFunction {
				body = append(body, exportBytes(rt, call.Argument(0))...)
			}
		}
		if status == 0 {
			status = int(response.Get("statusCode").ToInteger())
		}
		reply <- httpShimReply{status: status, headers: headers, body: body}
		return response
	})
	return response
}

// installImmediateBodyEvents wires the buffered-body .on("data"/"end") pattern
// (same delivery model as the fs.createReadStream shim).
func installImmediateBodyEvents(rt *sobek.Runtime, object *sobek.Object, body []byte) {
	mustSet(rt, object, "on", func(call sobek.FunctionCall) sobek.Value {
		handler, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			return object
		}
		switch call.Argument(0).String() {
		case "data":
			if len(body) > 0 {
				_, _ = handler(sobek.Undefined(), newBufferValue(rt, body))
			}
		case "end":
			_, _ = handler(sobek.Undefined())
		}
		return object
	})
}

func (h *shimHost) newHTTPClientRequest(rt *sobek.Runtime, call sobek.FunctionCall, defaultProtocol string, autoEnd bool) sobek.Value {
	target, method, requestHeaders := "", "GET", make(http.Header)
	var callback sobek.Callable
	for _, argument := range call.Arguments {
		if fn, ok := sobek.AssertFunction(argument); ok {
			callback = fn
			continue
		}
		if exported, ok := argument.Export().(string); ok {
			target = exported
			continue
		}
		if !present(argument) {
			continue
		}
		options := argument.ToObject(rt)
		protocol := defaultProtocol
		if value := stringProp(options, "protocol"); value != "" {
			protocol = value
		}
		host := stringProp(options, "hostname")
		if host == "" {
			host = stringProp(options, "host")
		}
		if host == "" {
			host = "localhost"
		}
		if port := stringProp(options, "port"); port != "" {
			host = net.JoinHostPort(host, port)
		}
		path := stringProp(options, "path")
		if path == "" {
			path = "/"
		}
		target = protocol + "//" + host + path
		if value := stringProp(options, "method"); value != "" {
			method = strings.ToUpper(value)
		}
		if headerValue := options.Get("headers"); present(headerValue) {
			parseHeaders(rt, headerValue, requestHeaders)
		}
	}
	if parsed, err := url.Parse(target); err == nil && parsed.Scheme == "" {
		target = defaultProtocol + "//" + target
	}

	requestObject := rt.NewObject()
	handlers := make(map[string]sobek.Callable)
	var body []byte
	started := false
	mustSet(rt, requestObject, "on", func(inner sobek.FunctionCall) sobek.Value {
		if fn, ok := sobek.AssertFunction(inner.Argument(1)); ok {
			handlers[inner.Argument(0).String()] = fn
		}
		return requestObject
	})
	mustSet(rt, requestObject, "setHeader", func(inner sobek.FunctionCall) sobek.Value {
		requestHeaders.Set(inner.Argument(0).String(), inner.Argument(1).String())
		return requestObject
	})
	mustSet(rt, requestObject, "write", func(inner sobek.FunctionCall) sobek.Value {
		body = append(body, exportBytes(rt, inner.Argument(0))...)
		return rt.ToValue(true)
	})
	start := func() {
		if started {
			return
		}
		started = true
		requestBody := append([]byte(nil), body...)
		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			var reader io.Reader
			if len(requestBody) > 0 {
				reader = strings.NewReader(string(requestBody))
			}
			request, err := http.NewRequestWithContext(h.vm.rootCtx, method, target, reader)
			if err == nil {
				request.Header = requestHeaders
			}
			var response *http.Response
			if err == nil {
				response, err = fetchClient.Do(request)
			}
			if err != nil {
				failure := err
				h.vm.postCallback(func(rt *sobek.Runtime) {
					if fn, ok := handlers["error"]; ok {
						_, _ = fn(sobek.Undefined(), rt.NewGoError(failure))
					}
				})
				return
			}
			responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, int64(fetchMaxResponseBody)))
			_ = response.Body.Close()
			if readErr != nil {
				h.vm.postCallback(func(rt *sobek.Runtime) {
					if fn, ok := handlers["error"]; ok {
						_, _ = fn(sobek.Undefined(), rt.NewGoError(readErr))
					}
				})
				return
			}
			statusCode := response.StatusCode
			statusText := http.StatusText(statusCode)
			responseHeaders := make(map[string]any, len(response.Header))
			for name, values := range response.Header {
				responseHeaders[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			h.vm.postCallback(func(rt *sobek.Runtime) {
				responseObject := rt.NewObject()
				mustSet(rt, responseObject, "statusCode", statusCode)
				mustSet(rt, responseObject, "statusMessage", statusText)
				mustSet(rt, responseObject, "headers", toJS(rt, responseHeaders))
				mustSet(rt, responseObject, "setEncoding", func(sobek.FunctionCall) sobek.Value { return sobek.Undefined() })
				mustSet(rt, responseObject, "resume", func(sobek.FunctionCall) sobek.Value { return responseObject })
				installImmediateBodyEvents(rt, responseObject, responseBody)
				if callback != nil {
					_, _ = callback(sobek.Undefined(), responseObject)
				}
				if fn, ok := handlers["response"]; ok {
					_, _ = fn(sobek.Undefined(), responseObject)
				}
			})
		}()
	}
	mustSet(rt, requestObject, "end", func(inner sobek.FunctionCall) sobek.Value {
		if present(inner.Argument(0)) {
			if _, isFunction := sobek.AssertFunction(inner.Argument(0)); !isFunction {
				body = append(body, exportBytes(rt, inner.Argument(0))...)
			}
		}
		start()
		return requestObject
	})
	if autoEnd {
		start()
	}
	return rt.ToValue(requestObject)
}
