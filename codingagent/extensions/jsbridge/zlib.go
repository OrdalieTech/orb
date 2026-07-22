package jsbridge

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"

	"github.com/grafana/sobek"
)

func newZlibModule(runtime *sobek.Runtime, vm *runtimeVM) *sobek.Object {
	module := runtime.NewObject()
	mustSet(runtime, module, "gzipSync", func(call sobek.FunctionCall) sobek.Value {
		level := zlibLevel(runtime, call.Argument(1))
		compressed, err := gzipBytes(exportBytes(runtime, call.Argument(0)), level)
		if err != nil {
			panic(zlibErrorValue(runtime, err))
		}
		return newBufferValue(runtime, compressed)
	})
	mustSet(runtime, module, "gunzipSync", func(call sobek.FunctionCall) sobek.Value {
		decompressed, err := gunzipBytes(exportBytes(runtime, call.Argument(0)))
		if err != nil {
			panic(zlibErrorValue(runtime, err))
		}
		return newBufferValue(runtime, decompressed)
	})
	mustSet(runtime, module, "gzip", zlibCallback(runtime, vm, true))
	mustSet(runtime, module, "gunzip", zlibCallback(runtime, vm, false))
	return module
}

func zlibCallback(runtime *sobek.Runtime, vm *runtimeVM, compress bool) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		callbackIndex := 1
		level := gzip.DefaultCompression
		if len(call.Arguments) > 1 {
			if _, callable := sobek.AssertFunction(call.Argument(1)); !callable {
				level = zlibLevel(runtime, call.Argument(1))
				callbackIndex = 2
			}
		}
		callback, callable := sobek.AssertFunction(call.Argument(callbackIndex))
		if !callable {
			panic(nodeArgumentError(runtime, "TypeError", "The \"callback\" argument must be of type function", "ERR_INVALID_ARG_TYPE"))
		}
		input := append([]byte(nil), exportBytes(runtime, call.Argument(0))...)
		ctx := vm.context()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			var output []byte
			var err error
			if compress {
				output, err = gzipBytes(input, level)
			} else {
				output, err = gunzipBytes(input)
			}
			vm.post(ctx, true, func(runtime *sobek.Runtime) error {
				if err != nil {
					_, callbackErr := callback(sobek.Undefined(), zlibErrorValue(runtime, err))
					return callbackErr
				}
				_, callbackErr := callback(sobek.Undefined(), sobek.Null(), newBufferValue(runtime, output))
				return callbackErr
			})
		}()
		return sobek.Undefined()
	}
}

func zlibLevel(runtime *sobek.Runtime, value sobek.Value) int {
	level := gzip.DefaultCompression
	if !present(value) {
		return level
	}
	options, ok := value.(*sobek.Object)
	if !ok {
		panic(nodeArgumentError(runtime, "TypeError", "The \"options\" argument must be of type object", "ERR_INVALID_ARG_TYPE"))
	}
	if configured := options.Get("level"); present(configured) {
		level = int(configured.ToInteger())
		if level < gzip.DefaultCompression || level > gzip.BestCompression {
			panic(nodeArgumentError(runtime, "RangeError", "The value of \"options.level\" is out of range. It must be >= -1 and <= 9", "ERR_OUT_OF_RANGE"))
		}
	}
	return level
}

func gzipBytes(input []byte, level int) ([]byte, error) {
	var output bytes.Buffer
	writer, err := gzip.NewWriterLevel(&output, level)
	if err != nil {
		return nil, err
	}
	if _, err = writer.Write(input); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func gunzipBytes(input []byte) ([]byte, error) {
	if len(input) >= 2 && (input[0] != 0x1f || input[1] != 0x8b) {
		return nil, gzip.ErrHeader
	}
	reader, err := gzip.NewReader(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	decompressed, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return decompressed, nil
}

func zlibErrorValue(runtime *sobek.Runtime, err error) sobek.Value {
	code, errno, message := "Z_DATA_ERROR", -3, err.Error()
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		code, errno, message = "Z_BUF_ERROR", -5, "unexpected end of file"
	case errors.Is(err, gzip.ErrHeader):
		message = "incorrect header check"
	case errors.Is(err, gzip.ErrChecksum):
		message = "incorrect data check"
	}
	errorObject, newErr := runtime.New(runtime.Get("Error"), runtime.ToValue(message))
	if newErr != nil {
		return runtime.NewTypeError(message)
	}
	mustSet(runtime, errorObject, "code", code)
	mustSet(runtime, errorObject, "errno", errno)
	return errorObject
}

func nodeArgumentError(runtime *sobek.Runtime, constructor, message, code string) sobek.Value {
	errorObject, err := runtime.New(runtime.Get(constructor), runtime.ToValue(message))
	if err != nil {
		return runtime.NewTypeError(message)
	}
	mustSet(runtime, errorObject, "code", code)
	return errorObject
}
