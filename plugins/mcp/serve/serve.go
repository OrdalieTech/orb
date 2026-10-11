// Package serve offers an Orb session's tools to another agent harness over
// MCP, the protocol harnesses take outside tools through. The harness
// integration chooses the tools (those its harness lacks) and carries the
// messages, over its own channel with a Pipe or over any go-sdk transport.
// Each call runs through engine.RunToolCall, so argument validation and the
// session's tool-call hooks, Orb's permission policy among them, apply as in
// Orb's own loop.
package serve

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation is how Orb names itself to MCP peers, as client and as server.
var Implementation = mcp.Implementation{Name: "orb", Version: "0.1.0"}

// Server is one session's tool server; each harness connection is a session of it.
type Server struct {
	server *mcp.Server
	mu     sync.Mutex
	turn   context.Context
	run    engine.RunToolCallOptions
	// offered keys each tool on offer by what the harness saw of it.
	offered map[string]string
}

func New() *Server {
	implementation := Implementation
	// Tools come and go with the session, so the capability is declared up front.
	options := &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}}}
	return &Server{server: mcp.NewServer(&implementation, options), turn: context.Background(), offered: map[string]string{}}
}

// Offer puts options.Tools on offer, run with options from now on and stopped
// when ctx ends, as the harness's own request may be; connected harnesses hear
// of every change to the list.
func (s *Server) Offer(ctx context.Context, options engine.RunToolCallOptions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn, s.run = ctx, options
	stale := maps.Clone(s.offered)
	for _, tool := range options.Tools {
		spec := tool.Spec()
		schema, ok := inputSchema(spec.Parameters)
		if !ok {
			continue
		}
		description := cmp.Or(spec.Description, spec.Label, spec.Name)
		delete(stale, spec.Name)
		if key := description + "\x00" + string(schema); s.offered[spec.Name] != key {
			s.offered[spec.Name] = key
			s.server.AddTool(&mcp.Tool{Name: spec.Name, Description: description, InputSchema: schema}, s.call)
		}
	}
	if len(stale) > 0 {
		names := slices.Collect(maps.Keys(stale))
		for _, name := range names {
			delete(s.offered, name)
		}
		s.server.RemoveTools(names...)
	}
}

// Connect serves one harness connection until its transport closes.
func (s *Server) Connect(ctx context.Context, transport mcp.Transport) error {
	_, err := s.server.Connect(ctx, transport, nil)
	return err
}

func (s *Server) call(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	turn, options := s.turn, s.run
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(turn, cancel)()
	arguments := map[string]any{}
	if len(request.Params.Arguments) > 0 {
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			return failure("Arguments are not a JSON object: " + err.Error()), nil
		}
	}
	call := &ai.ToolCall{ID: "mcp-" + rand.Text(), Name: request.Params.Name, Arguments: arguments}
	options.AssistantMessage = &ai.AssistantMessage{Content: ai.AssistantContent{call}}
	return result(engine.RunToolCall(ctx, call, options)), nil
}

// inputSchema is a tool's parameters as MCP takes them: an object schema.
func inputSchema(parameters ai.JSONSchema) (json.RawMessage, bool) {
	raw, err := json.Marshal(parameters)
	var schema map[string]any
	if err != nil || json.Unmarshal(raw, &schema) != nil {
		return nil, false
	}
	if len(schema) == 0 {
		return json.RawMessage(`{"type":"object"}`), true
	}
	return raw, schema["type"] == "object"
}

func result(outcome engine.AgentToolCallOutcome) *mcp.CallToolResult {
	result := &mcp.CallToolResult{Content: []mcp.Content{}, IsError: outcome.IsError}
	for _, block := range outcome.Result.Content {
		switch block := block.(type) {
		case *ai.TextContent:
			result.Content = append(result.Content, &mcp.TextContent{Text: block.Text})
		case *ai.ImageContent:
			if data, err := base64.StdEncoding.DecodeString(block.Data); err == nil {
				result.Content = append(result.Content, &mcp.ImageContent{Data: data, MIMEType: block.MimeType})
			}
		}
	}
	return result
}

func failure(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}
}
