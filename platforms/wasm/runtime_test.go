package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestAPIProviderUsesExplicitEndpointAndWorkspaceTools(t *testing.T) {
	for _, apiName := range []string{"openrouter", "openai-completions", "anthropic-messages"} {
		t.Run(apiName, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Model string            `json:"model"`
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Model != "test-model" || len(payload.Tools) != 3 {
					t.Errorf("unexpected request: %#v", payload)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if apiName != "anthropic-messages" {
					if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("OpenAI endpoint/auth lost")
					}
					_, _ = fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"API works\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					if !strings.HasPrefix(r.URL.Path, "/v1/messages") || r.Header.Get("X-Api-Key") != "test-key" {
						t.Error("Anthropic endpoint/auth lost")
					}
					for _, frame := range []string{
						`{"type":"message_start","message":{"id":"test","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"API works"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
						`{"type":"message_stop"}`,
					} {
						var header struct {
							Type string `json:"type"`
						}
						if err := json.Unmarshal([]byte(frame), &header); err != nil {
							t.Error(err)
						}
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", header.Type, frame)
					}
				}
			}))
			defer server.Close()
			s, err := New(Config{API: apiName, Model: "test-model", BaseURL: server.URL, APIKey: "test-key"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Agent.Prompt(t.Context(), "hello"); err != nil {
				t.Fatal(err)
			}
			state := s.Agent.State()
			if state.ErrorMessage != nil {
				t.Fatal(*state.ErrorMessage)
			}
			last := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage)
			if ai.ContentText(last.Content) != "API works" {
				t.Fatalf("reply: %#v", last)
			}
		})
	}
}

func TestToolsUseIsolatedWorkspaces(t *testing.T) {
	first, err := New(Config{API: "openrouter", Model: "test", BaseURL: "http://localhost", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{API: "openrouter", Model: "test", BaseURL: "http://localhost", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range first.Agent.State().Tools {
		switch tool.Spec().Name {
		case "write":
			if _, err := tool.Execute(t.Context(), "write", map[string]any{"path": "note.txt", "content": "isolated"}, nil); err != nil {
				t.Fatal(err)
			}
		case "read", "edit":
		default:
			t.Fatalf("unexpected tool %q", tool.Spec().Name)
		}
	}
	if first.Workspace.Snapshot()["/workspace/note.txt"] != "isolated" || len(second.Workspace.Snapshot()) != 0 {
		t.Fatal("workspace isolation failed")
	}
}

func TestWorkspaceBoundsAndToolEditing(t *testing.T) {
	s, err := New(Config{API: "openrouter", Model: "test", BaseURL: "http://localhost", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/etc/passwd", "/workspace/../../escape", "/workspace-other/file", "/workspace/\x00"} {
		if err := s.Workspace.WriteFile(t.Context(), name, []byte("bad")); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if _, err := s.Workspace.ReadBinaryFile(t.Context(), "/workspace/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err := s.Workspace.WriteFile(t.Context(), "/workspace/a", []byte("before")); err != nil {
		t.Fatal(err)
	}
	for _, tool := range s.Agent.State().Tools {
		if tool.Spec().Name == "edit" {
			_, err := tool.Execute(t.Context(), "edit", map[string]any{"path": "a", "edits": []any{map[string]any{"oldText": "before", "newText": "after"}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if s.Workspace.Snapshot()["/workspace/a"] != "after" {
		t.Fatal("edit bypassed workspace")
	}
	if err := s.Workspace.WriteFile(t.Context(), "/workspace/huge", []byte(strings.Repeat("x", maxWorkspaceBytes))); err == nil {
		t.Fatal("workspace limit bypassed")
	}
	for index := 1; index < maxWorkspaceFiles; index++ {
		if err := s.Workspace.WriteFile(t.Context(), fmt.Sprintf("/workspace/f%d", index), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Workspace.WriteFile(t.Context(), "/workspace/one-too-many", nil); err == nil {
		t.Fatal("workspace file limit bypassed")
	}
	if err := s.Workspace.WriteFile(t.Context(), "/workspace/a/child", []byte("bad")); err == nil {
		t.Fatal("file treated as directory")
	}
	copy := s.Workspace.Snapshot()
	copy["/workspace/a"] = "changed"
	if s.Workspace.Snapshot()["/workspace/a"] != "after" {
		t.Fatal("snapshot aliases files")
	}
}

func TestCancellationAndProviderValidation(t *testing.T) {
	for _, config := range []Config{{API: "demo"}, {API: "openrouter", Model: "test", BaseURL: "http://localhost"}, {API: "unknown"}, {API: "anthropic-messages"}, {API: "openai-completions", Model: "test", BaseURL: "file:///tmp/api"}} {
		if _, err := New(config); err == nil {
			t.Fatalf("accepted %#v", config)
		}
	}
	s, err := New(Config{API: "openrouter", Model: "test", BaseURL: "http://localhost", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s.Agent.SetStreamFn(func(ctx context.Context, _ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- s.Agent.Prompt(t.Context(), "cancel") }()
	<-entered
	s.Agent.Abort()
	<-done
	if !s.Agent.IsIdle() {
		t.Fatal("agent did not settle after cancellation")
	}
}
