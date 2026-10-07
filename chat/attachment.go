package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// AttachmentKind maps a MIME type to an [AttachmentRef] Kind: image/* is
// "photo", audio/* "audio", video/* "video", and anything else "document".
func AttachmentKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	}
	return "document"
}

// FileSender is a [Delivery] whose platform takes files from the agent. Unlike
// the Delivery calls, SendFile runs on a tool call, alongside Preview.
type FileSender interface {
	// SendFile sends size bytes of content as the file name, or fails with
	// the platform's reason, a size limit included.
	SendFile(ctx context.Context, name string, size int64, content io.Reader) error
}

// turnDelivery carries the running turn's Delivery to its tool calls.
type turnDelivery struct{}

// SendFile is the send_file tool, for the sessions of chat conversations: it
// sends a file under the working directory to the person the agent is
// talking with, through the turn's delivery.
func SendFile(api extensions.API) error {
	api.RegisterTool(extensions.ToolDefinition{
		Name: "send_file", Label: "Send File",
		Description: "Send a file from the working directory to the person you are chatting with, as an attachment.",
		Parameters:  ai.JSONSchema(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"The file, under the working directory."}}}`),
		Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, session extensions.Context) (engine.AgentToolResult, error) {
			var input struct {
				Path string `json:"path"`
			}
			data, _ := json.Marshal(raw)
			if err := json.Unmarshal(data, &input); err != nil || input.Path == "" {
				return engine.AgentToolResult{}, errors.New("send_file: path is required")
			}
			sender, ok := ctx.Value(turnDelivery{}).(FileSender)
			if !ok {
				return engine.AgentToolResult{}, errors.New("send_file: this conversation's platform does not take files")
			}
			file, err := openUnder(session.CWD(), input.Path)
			if err != nil {
				return engine.AgentToolResult{}, fmt.Errorf("send_file: %w", err)
			}
			defer func() { _ = file.Close() }()
			info, err := file.Stat()
			if err == nil {
				err = sender.SendFile(ctx, filepath.Base(file.Name()), info.Size(), file)
			}
			if err != nil {
				return engine.AgentToolResult{}, fmt.Errorf("send_file: %w", err)
			}
			return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "Sent " + filepath.Base(file.Name())}}}, nil
		},
	})
	return nil
}

// openUnder opens the regular file path names under dir, links resolved.
func openUnder(dir, path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s is outside the working directory", path)
	}
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a file", path)
	}
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}
