package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	ProtocolName    = "pigo-extension-host"
	ProtocolVersion = 1
	MaxFrameSize    = 4 << 20
)

var (
	ErrFrameTooLarge   = errors.New("extension host: frame exceeds 4 MiB")
	ErrIncompleteFrame = errors.New("extension host: unterminated JSONL frame")
)

type frameKind string

const (
	frameRequest  frameKind = "request"
	frameResponse frameKind = "response"
	frameEvent    frameKind = "event"
)

type protocolError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (err *protocolError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

type frame struct {
	Protocol string          `json:"protocol"`
	Version  int             `json:"version"`
	Kind     frameKind       `json:"kind"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *protocolError  `json:"error,omitempty"`
}

type codec struct {
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex
	maxSize int
}

func newCodec(reader io.Reader, writer io.Writer) *codec {
	return &codec{reader: bufio.NewReader(reader), writer: writer, maxSize: MaxFrameSize}
}

func (codec *codec) read() (frame, error) {
	var encoded []byte
	for {
		fragment, err := codec.reader.ReadSlice('\n')
		if len(encoded)+len(fragment) > codec.maxSize+1 {
			return frame{}, ErrFrameTooLarge
		}
		encoded = append(encoded, fragment...)
		switch {
		case err == nil:
			encoded = bytes.TrimSuffix(encoded, []byte{'\n'})
			if len(encoded) == 0 {
				return frame{}, errors.New("extension host: empty JSONL frame")
			}
			var value frame
			if err := json.Unmarshal(encoded, &value); err != nil {
				return frame{}, fmt.Errorf("extension host: decode frame: %w", err)
			}
			if err := validateFrame(value); err != nil {
				return frame{}, err
			}
			return value, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(encoded) == 0:
			return frame{}, io.EOF
		case errors.Is(err, io.EOF):
			return frame{}, ErrIncompleteFrame
		default:
			return frame{}, err
		}
	}
}

func (codec *codec) write(value frame) error {
	encoded, err := ai.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > codec.maxSize {
		return ErrFrameTooLarge
	}
	encoded = append(encoded, '\n')
	codec.writeMu.Lock()
	defer codec.writeMu.Unlock()
	_, err = codec.writer.Write(encoded)
	return err
}

func validateFrame(value frame) error {
	if value.Protocol != ProtocolName {
		return fmt.Errorf("extension host: unexpected protocol %q", value.Protocol)
	}
	if value.Version != ProtocolVersion {
		return fmt.Errorf("extension host: unsupported protocol version %d", value.Version)
	}
	switch value.Kind {
	case frameRequest:
		if value.ID == "" || value.Method == "" || len(value.Params) == 0 {
			return errors.New("extension host: invalid request frame")
		}
	case frameResponse:
		if value.ID == "" || (len(value.Result) == 0) == (value.Error == nil) {
			return errors.New("extension host: invalid response frame")
		}
	case frameEvent:
		if value.ID != "" || value.Method == "" || len(value.Params) == 0 {
			return errors.New("extension host: invalid event frame")
		}
	default:
		return fmt.Errorf("extension host: invalid frame kind %q", value.Kind)
	}
	return nil
}

func requestFrame(id, method string, params any) (frame, error) {
	encoded, err := ai.Marshal(params)
	if err != nil {
		return frame{}, err
	}
	return frame{Protocol: ProtocolName, Version: ProtocolVersion, Kind: frameRequest, ID: id, Method: method, Params: encoded}, nil
}

func eventFrame(method string, params any) (frame, error) {
	encoded, err := ai.Marshal(params)
	if err != nil {
		return frame{}, err
	}
	return frame{Protocol: ProtocolName, Version: ProtocolVersion, Kind: frameEvent, Method: method, Params: encoded}, nil
}

func successFrame(id string, result any) (frame, error) {
	encoded, err := ai.Marshal(result)
	if err != nil {
		return frame{}, err
	}
	return frame{Protocol: ProtocolName, Version: ProtocolVersion, Kind: frameResponse, ID: id, Result: encoded}, nil
}

func errorFrame(id, code, message string) frame {
	return frame{
		Protocol: ProtocolName,
		Version:  ProtocolVersion,
		Kind:     frameResponse,
		ID:       id,
		Error:    &protocolError{Code: code, Message: message},
	}
}
