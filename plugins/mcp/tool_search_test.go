package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTokenizeSplitsCamelCaseAndSingularizes(t *testing.T) {
	got := tokenize("listGitHubIssues for the searches, HTTPServer")
	want := []string{"list", "git", "hub", "issue", "search", "http", "server"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize = %q", got)
	}
}

func TestRankBM25PrefersSpecificMatches(t *testing.T) {
	documents := []string{"read a file from disk", "create a github issue", "list github issues and pull requests"}
	if got := rankBM25("github issues", documents, 8); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("rank = %v", got)
	}
	if got := rankBM25("the and", documents, 8); got != nil {
		t.Fatalf("stop words matched %v", got)
	}
}

func TestToolSearchLoadsDeferredMatches(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "tracker", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "create_issue", Description: "Create a GitHub issue"}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	addTextTool(server, "ping")
	manager := NewManager(t.TempDir(), []testServer{{Name: "tracker", Command: "in-memory"}})
	manager.servers["tracker"].entry.Config.Exposure = ExposureCodemode
	manager.load = func(string, bool) ([]Entry, []string) { return []Entry{manager.servers["tracker"].entry}, nil }
	manager.connect = inMemoryConnector(server)

	registry := extensions.NewRegistry(t.TempDir())
	_ = registry.Register("<tool-search>", ToolSearchExtension())
	_ = registry.Register("<mcp>", manager.Extension())
	active := &activeTools{}
	var runner *extensions.Runner
	runner = extensions.NewRunner(registry, extensions.RunnerOptions{Actions: extensions.Actions{
		GetActiveTools: func() ([]string, error) { return active.get(), nil },
		SetActiveTools: func(names []string) error {
			active.set(names)
			return nil
		},
		GetAllTools: func() ([]extensions.ToolInfo, error) {
			var tools []extensions.ToolInfo
			for _, tool := range runner.AllRegisteredTools() {
				definition := tool.Definition
				tools = append(tools, extensions.ToolInfo{Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters, Exposure: definition.EffectiveExposure(), Namespace: definition.Namespace})
			}
			return tools, nil
		},
	}})
	defer closeManager(t, manager)
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	if got := active.get(); !reflect.DeepEqual(got, []string{ToolSearchName}) {
		t.Fatalf("tool_search not activated from the config: %v", got)
	}
	// tool_search waits for the servers still connecting.
	runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{ToolCallID: "1", ToolName: ToolSearchName, Input: map[string]any{"query": "issue"}})
	definition := runner.ToolDefinition(ToolSearchName)
	result, err := definition.Execute(context.Background(), "1", map[string]any{"query": "github issue"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*ai.TextContent).Text
	if !strings.HasPrefix(text, "Loaded 1 tool.") || !strings.Contains(text, "- mcp__tracker__create_issue: Create a GitHub issue") {
		t.Fatalf("result = %q", text)
	}
	if got := active.get(); !reflect.DeepEqual(got, []string{ToolSearchName, "mcp__tracker__create_issue"}) {
		t.Fatalf("active = %v", got)
	}
	if _, err := definition.Execute(context.Background(), "2", map[string]any{"query": " "}, nil, runner.CreateContext()); err == nil {
		t.Fatal("empty query accepted")
	}

	options := extensions.SystemPromptOptions{Sections: map[string]string{}}
	runner.EmitBeforeAgentStart(context.Background(), "hi", nil, "", options)
	want := "MCP servers whose tools are not declared to you. Load the tools of `tool_search` servers with `tool_search`.\n- mcp__tracker (tool_search)"
	if got := options.Sections[MCPServersSection]; got != want {
		t.Fatalf("section = %q", got)
	}
}

func TestServersSectionFitsItsBudget(t *testing.T) {
	var servers []testServer
	for index := range 300 {
		servers = append(servers, testServer{Name: "server" + strings.Repeat("x", index%3) + string(rune('a'+index%26)) + string(rune('a'+index/26)), Command: "x"})
	}
	manager := NewManager(t.TempDir(), servers)
	for _, connection := range manager.servers {
		connection.entry.Config.Exposure = ExposureDeferred
		connection.entry.Config.Description = strings.Repeat("Long description. ", 40)
	}
	section := manager.serversSection()
	if len([]rune(section)) > maxServersSection || !strings.Contains(section, "more servers; find their tools with tool_search") {
		t.Fatalf("section (%d chars) =\n%s", len([]rune(section)), section)
	}
}
