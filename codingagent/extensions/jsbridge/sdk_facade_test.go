package jsbridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

func TestPioliumSDKFacadeRunsChildSession(t *testing.T) {
	type capturedRequest struct {
		path          string
		authorization string
		body          string
		err           error
	}
	requests := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		requests <- capturedRequest{
			path:          request.URL.Path,
			authorization: request.Header.Get("Authorization"),
			body:          string(body),
			err:           err,
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response,
			"data: {\"id\":\"chatcmpl-piolium\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"piolium runner OK\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"chatcmpl-piolium\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	t.Cleanup(server.Close)

	project, agentDir := pioliumSDKProject(t)

	source := fmt.Sprintf(`
import { DefaultResourceLoader, SessionManager, createAgentSession, getAgentDir } from "@earendil-works/pi-coding-agent";

export default function(pi) {
  pi.registerCommand("piolium-sdk-smoke", {
    description: "exercise the child-agent SDK facade",
    handler: async (_args, ctx) => {
      if (getAgentDir() !== %q) throw new Error("getAgentDir mismatch: " + getAgentDir());
      const resourceLoader = new DefaultResourceLoader({
        cwd: ctx.cwd,
        agentDir: getAgentDir(),
        systemPrompt: "PIOLIUM_RESOURCE_RELOAD_MARKER",
        additionalSkillPaths: [],
        noExtensions: true,
        noSkills: false,
        noPromptTemplates: true,
        noThemes: true,
        noContextFiles: true,
      });
      await resourceLoader.reload();

      const sessionManager = SessionManager.inMemory();
      if (sessionManager.getCwd() !== ctx.cwd) {
        throw new Error("in-memory cwd mismatch: " + sessionManager.getCwd());
      }
      const { session } = await createAgentSession({
        cwd: ctx.cwd,
        agentDir: getAgentDir(),
        model: {
          id: "piolium-fixture",
          name: "Piolium Fixture",
          api: "openai-completions",
          provider: "openai",
          baseUrl: %q,
          reasoning: false,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 8192,
          maxTokens: 1024,
        },
        modelRegistry: new Proxy({}, { get() { throw new Error("stale modelRegistry was accessed"); } }),
        thinkingLevel: "off",
        tools: [],
        noTools: "all",
        sessionManager,
        resourceLoader,
        customTools: [{
          name: "unused_web_tool",
          label: "Unused Web Tool",
          description: "Piolium always supplies custom web tools, even when disabled.",
          parameters: { type: "object", properties: {} },
          execute: async () => ({ content: [{ type: "text", text: "unused" }] }),
        }],
      });

      let finalText = "";
      let stopReason = "";
      const unsubscribe = session.subscribe((event) => {
        if (event.type !== "message_end" || event.message?.role !== "assistant") return;
        finalText = event.message.content
          .filter((block) => block.type === "text")
          .map((block) => block.text)
          .join("");
        stopReason = event.message.stopReason;
      });
      try {
        await session.prompt("run the piolium child");
        await session.agent.waitForIdle();
      } finally {
        unsubscribe();
        session.dispose();
      }
      if (finalText !== "piolium runner OK") throw new Error("final text mismatch: " + finalText);
      if (stopReason !== "stop") throw new Error("stop reason mismatch: " + stopReason);
    },
  });
}
`, agentDir, server.URL+"/v1")

	runner := loadBridgeRunner(t, project, []bridgeSource{{name: "piolium-sdk-smoke.ts", source: source}}, extensions.RunnerOptions{})
	command := runner.Command("piolium-sdk-smoke")
	if command == nil {
		t.Fatal("piolium SDK smoke command was not registered")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}

	captured := <-requests
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	if captured.path != "/v1/chat/completions" {
		t.Fatalf("request path = %q", captured.path)
	}
	if captured.authorization != "Bearer fixture-key" {
		t.Fatalf("authorization = %q", captured.authorization)
	}
	if !strings.Contains(captured.body, "PIOLIUM_RESOURCE_RELOAD_MARKER") {
		t.Fatalf("reloaded system prompt missing from request: %s", captured.body)
	}
}

func TestPioliumSDKFacadeRunsCustomToolAndAbortsChild(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response,
			"data: {\"id\":\"chatcmpl-piolium-tool\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_child\",\"type\":\"function\",\"function\":{\"name\":\"child_web_tool\",\"arguments\":\"{\\\"query\\\":\\\"needle\\\"}\"}}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"chatcmpl-piolium-tool\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	t.Cleanup(server.Close)

	project, agentDir := pioliumSDKProject(t)
	source := fmt.Sprintf(`
import { DefaultResourceLoader, SessionManager, createAgentSession } from "@earendil-works/pi-coding-agent";

export default function(pi) {
  pi.registerCommand("piolium-sdk-abort", {
    description: "exercise child custom tools and abort",
    handler: async (_args, ctx) => {
      const resourceLoader = new DefaultResourceLoader({
        cwd: ctx.cwd,
        agentDir: %q,
        noExtensions: true,
        noSkills: true,
        noPromptTemplates: true,
        noThemes: true,
        noContextFiles: true,
      });
      await resourceLoader.reload();

      let childSession;
      let toolExecuted = false;
      let toolStart = false;
      let toolEnd = false;
      let abortedMessage = false;
      const { session } = await createAgentSession({
        cwd: ctx.cwd,
        agentDir: %q,
        model: {
          id: "piolium-tool-fixture",
          name: "Piolium Tool Fixture",
          api: "openai-completions",
          provider: "openai",
          baseUrl: %q,
          reasoning: false,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 8192,
          maxTokens: 1024,
        },
        thinkingLevel: "off",
        tools: ["child_web_tool"],
        sessionManager: SessionManager.inMemory(ctx.cwd),
        resourceLoader,
        customTools: [{
          name: "child_web_tool",
          label: "Child Web Tool",
          description: "Abort from a running child tool.",
          parameters: {
            type: "object",
            required: ["query"],
            properties: { query: { type: "string" } },
          },
          execute: (_id, params) => {
            if (params.query !== "needle") throw new Error("tool args mismatch");
            toolExecuted = true;
            childSession.agent.abort();
            return { content: [{ type: "text", text: "aborted" }] };
          },
        }],
      });
      childSession = session;

      const unsubscribe = session.subscribe((event) => {
        if (event.type === "tool_execution_start") {
          if (event.toolName !== "child_web_tool" || event.args.query !== "needle") {
            throw new Error("tool start shape mismatch");
          }
          toolStart = true;
        }
        if (event.type === "tool_execution_end" && event.toolName === "child_web_tool") toolEnd = true;
        if (event.type === "message_end" && event.message?.stopReason === "aborted") abortedMessage = true;
      });
      try {
        await session.prompt("call the child web tool");
        await session.agent.waitForIdle();
      } finally {
        unsubscribe();
        session.dispose();
      }
      if (!toolExecuted) throw new Error("custom tool did not execute");
      if (!toolStart || !toolEnd) throw new Error("tool lifecycle events were incomplete");
      if (!abortedMessage) throw new Error("aborted message_end was not emitted");
    },
  });
}
`, agentDir, agentDir, server.URL+"/v1")

	runner := loadBridgeRunner(t, project, []bridgeSource{{name: "piolium-sdk-abort.ts", source: source}}, extensions.RunnerOptions{})
	command := runner.Command("piolium-sdk-abort")
	if command == nil {
		t.Fatal("piolium SDK abort command was not registered")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	<-requests
}

func pioliumSDKProject(t *testing.T) (string, string) {
	t.Helper()
	project := t.TempDir()
	agentDir := filepath.Join(project, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(`{"openai":{"type":"api_key","key":"fixture-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return project, agentDir
}
