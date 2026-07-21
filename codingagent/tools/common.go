package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
	textunicode "golang.org/x/text/encoding/unicode"
)

var errOperationAborted = upstreamToolError("Operation aborted")

const (
	accessWrite = 2
	accessRead  = 4
)

func checkAborted(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return errOperationAborted
	}
	return nil
}

func upstreamToolError(message string) error {
	return errors.New(message)
}

func upstreamToolErrorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func nodeNullPathError(path string) error {
	if !strings.ContainsRune(path, 0) {
		return nil
	}
	return nodeInvalidArgumentError{message: fmt.Sprintf("The argument 'path' must be a string, Uint8Array, or URL without null bytes. Received %s", nodeInspectString(path))}
}

type nodeInvalidArgumentError struct{ message string }

func (err nodeInvalidArgumentError) Error() string { return err.message }
func (nodeInvalidArgumentError) Code() string      { return "ERR_INVALID_ARG_VALUE" }

func nodeInspectString(value string) string {
	quote := '\''
	if strings.ContainsRune(value, '\'') {
		switch {
		case !strings.ContainsRune(value, '"'):
			quote = '"'
		case !strings.ContainsRune(value, '`'):
			quote = '`'
		}
	}
	var inspected strings.Builder
	inspected.WriteRune(quote)
	for _, character := range value {
		switch character {
		case quote:
			inspected.WriteRune('\\')
			inspected.WriteRune(character)
		case '\\':
			inspected.WriteString(`\\`)
		case 0:
			inspected.WriteString(`\x00`)
		case '\b':
			inspected.WriteString(`\b`)
		case '\t':
			inspected.WriteString(`\t`)
		case '\n':
			inspected.WriteString(`\n`)
		case '\v':
			inspected.WriteString(`\v`)
		case '\f':
			inspected.WriteString(`\f`)
		case '\r':
			inspected.WriteString(`\r`)
		default:
			if character < 0x20 || character == 0x7f {
				_, _ = fmt.Fprintf(&inspected, `\x%02x`, character)
			} else {
				inspected.WriteRune(character)
			}
		}
	}
	inspected.WriteRune(quote)
	return inspected.String()
}

func runCancelable[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := checkAborted(ctx); err != nil {
		return zero, err
	}
	if ctx == nil {
		return fn()
	}
	type outcome struct {
		value T
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		value, err := fn()
		result <- outcome{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, errOperationAborted
	case completed := <-result:
		if err := checkAborted(ctx); err != nil {
			return zero, err
		}
		return completed.value, completed.err
	}
}

type nodeFilesystemError struct {
	code      string
	operation string
	path      string
}

func (err nodeFilesystemError) Code() string { return err.code }

func (err nodeFilesystemError) Error() string {
	description := map[string]string{
		"EACCES":       "permission denied",
		"EEXIST":       "file already exists",
		"EIO":          "i/o error",
		"EINVAL":       "invalid argument",
		"EISDIR":       "illegal operation on a directory",
		"ELOOP":        "too many symbolic links encountered",
		"ENAMETOOLONG": "name too long",
		"ENOENT":       "no such file or directory",
		"ENOMEM":       "not enough memory",
		"ENOSPC":       "no space left on device",
		"ENOTDIR":      "not a directory",
		"EPERM":        "operation not permitted",
		"EROFS":        "read-only file system",
		"ETXTBSY":      "text file is busy",
	}[err.code]
	if description == "" {
		description = "unknown error"
	}
	if err.path == "" {
		return fmt.Sprintf("%s: %s, %s", err.code, description, err.operation)
	}
	return fmt.Sprintf("%s: %s, %s '%s'", err.code, description, err.operation, err.path)
}

func asNodeFilesystemError(operation, path string, err error) error {
	return nodeFilesystemErrorFor(operation, path, err, true)
}

func asNodeFilesystemErrorAt(operation, path string, err error) error {
	return nodeFilesystemErrorFor(operation, path, err, false)
}

func nodeFilesystemErrorFor(operation, path string, err error, inheritPathError bool) error {
	if err == nil {
		return nil
	}
	code := filesystemErrorCode(err)
	if code == "" {
		return err
	}
	var pathError *os.PathError
	if inheritPathError && errors.As(err, &pathError) {
		operation = pathError.Op
		path = pathError.Path
	}
	if operation == "read" || operation == "write" || operation == "close" {
		path = ""
	}
	return nodeFilesystemError{code: code, operation: operation, path: path}
}

func runCancelableError(ctx context.Context, fn func() error) error {
	_, err := runCancelable(ctx, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

func toolParams(value any) (map[string]any, error) {
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if object == nil {
		return nil, errors.New("invalid tool arguments: expected an object")
	}
	return object, nil
}

func requiredString(object map[string]any, name string) (string, error) {
	value, ok := object[name]
	if !ok {
		return "", fmt.Errorf("invalid tool arguments: %s is required", name)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid tool arguments: %s must be a string", name)
	}
	return text, nil
}

func optionalString(object map[string]any, name string) (*string, error) {
	value, ok := object[name]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("invalid tool arguments: %s must be a string", name)
	}
	return &text, nil
}

func optionalNumber(object map[string]any, name string) (*float64, error) {
	value, ok := object[name]
	if !ok || value == nil {
		return nil, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int8:
		number = float64(typed)
	case int16:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint8:
		number = float64(typed)
	case uint16:
		number = float64(typed)
	case uint32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid tool arguments: %s must be a number", name)
		}
		number = parsed
	default:
		return nil, fmt.Errorf("invalid tool arguments: %s must be a number", name)
	}
	return &number, nil
}

func formatJSNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// jsSliceIndex applies Array.prototype.slice's ToIntegerOrInfinity and bounds
// handling for the finite numbers accepted by the tool schemas.
func jsSliceIndex(value float64, length int) int {
	if value >= float64(length) {
		return length
	}
	if value <= -float64(length) {
		return 0
	}
	integer := int(math.Trunc(value))
	if integer < 0 {
		return length + integer
	}
	return integer
}

func decodeNodeUTF8(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}

func encodeNodeUTF8(value string) string {
	return string(utf16.Decode(javascriptUTF16Units(value)))
}

func javascriptUTF16Length(value string) int {
	return len(javascriptUTF16Units(value))
}

func javascriptUTF16Units(value string) []uint16 {
	units := make([]uint16, 0, len(value))
	for index := 0; index < len(value); {
		if surrogate, ok := jsonwire.DecodeWTF8Surrogate(value[index:]); ok {
			units = append(units, surrogate)
			index += 3
			continue
		}
		character, size := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && size == 1 {
			units = append(units, uint16(utf8.RuneError))
			index++
			continue
		}
		if character <= 0xffff {
			units = append(units, uint16(character))
		} else {
			first, second := utf16.EncodeRune(character)
			units = append(units, uint16(first), uint16(second))
		}
		index += size
	}
	return units
}
