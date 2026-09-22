package claudesessions

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/connect"
	connectagent "github.com/OrdalieTech/orb/connect/agent"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/bridge"
	plugins "github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/plugins/questions"
)

const fakeSDK = `
export function query({prompt,options:o}) {
 const abort = new AbortController();
 const gen = (async function*(){
  const {value:p}=await prompt[Symbol.asyncIterator]().next();
  if(o.permissionMode!=='default'||process.env.SDK_TEST_KEY!=='unchanged') throw new Error('options or environment lost');
  const id=o.sessionId||o.resume;
  yield {type:'system',subtype:'init',session_id:id};
  yield {type:'stream_event',event:{type:'message_start',message:{model:o.model,content:[]}}};
  yield {type:'stream_event',event:{type:'content_block_start',index:0,content_block:{type:'text'}}};
  yield {type:'stream_event',event:{type:'content_block_delta',index:0,delta:{type:'text_delta',text:'streamed'}}};
  const question = JSON.stringify(p.message.content).includes('question-fixture');
  const tool=question?'AskUserQuestion':'Write';
  const input=question?{questions:[
    {header:'Diagram',question:'What should be diagrammed?',options:[{label:'Layers',description:'Show the runtime boundaries'},{label:'Flow',description:'Show one request'}]},
    {header:'Details',question:'Which details?',multiSelect:true,options:[{label:'Sessions',description:'Include storage'},{label:'Bridge',description:'Include routing'}]}
  ]}:{file_path:'/fixture'};
  const hook=await o.hooks?.PreToolUse[0].hooks[0]({hook_event_name:'PreToolUse',tool_name:tool,tool_input:input,tool_use_id:'native-write',cwd:o.cwd},'native-write',{signal:abort.signal});
  const decision=hook?.hookSpecificOutput?.permissionDecision;
  const reply=decision==='deny'?{behavior:'deny'}:decision==='allow'&&!question?{behavior:'allow',updatedInput:input}:await o.canUseTool(tool,input,{signal:abort.signal});
  if(reply.behavior!=='allow') throw new Error('permission denied');
  const text=JSON.stringify({resume:o.resume??'',fork:o.forkSession??false,at:o.resumeSessionAt??'',content:p.message.content,reply,effort:o.effort,thinking:o.thinking});
  yield {type:'assistant',uuid:'assistant-checkpoint',session_id:id,message:{model:o.model,content:[{type:'text',text},{type:'tool_use',id:'tool-1',name:'Write',input:{file_path:'/fixture'}}],usage:{input_tokens:10,output_tokens:4},stop_reason:'tool_use'}};
  yield {type:'user',uuid:'tool-checkpoint',session_id:id,message:{content:[{type:'tool_result',tool_use_id:'tool-1',content:'written'}]}};
  yield {type:'assistant',uuid:'final-checkpoint',session_id:id,message:{model:o.model,content:[{type:'text',text:'done'}],usage:{input_tokens:12,output_tokens:2},stop_reason:'end_turn'}};
  yield {type:'result',subtype:'success',session_id:id,total_cost_usd:0.01};
 })();
 gen.supportedModels=async()=>[
 {value:'default',resolvedModel:'claude-native-default',displayName:'Default',supportsEffort:true,supportedEffortLevels:['low','high','max'],supportsAdaptiveThinking:true},
 {value:'sonnet',resolvedModel:'claude-sonnet-current',displayName:'Sonnet'},
 {value:'opus',resolvedModel:'claude-opus-current',displayName:'Opus',supportsEffort:true,supportedEffortLevels:['low','high','max'],supportsAdaptiveThinking:true}
 ];
 gen.close=()=>abort.abort();gen.interrupt=async()=>abort.abort();
 return gen;
}`

func fixture(t *testing.T, policy ...*plugins.Policy) (*agent.AgentSessionRuntime, *Driver) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for SDK host test", err)
	}
	dir := t.TempDir()
	sdk := filepath.Join(dir, "sdk.mjs")
	if err = os.WriteFile(sdk, []byte(fakeSDK), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := session.Create(dir, filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	options := Options{RenderText: func(text string) extensions.Component { return testText(text) }, Node: node, Claude: filepath.Join(dir, "unused-native"), SDK: sdk, Env: []string{"SDK_TEST_KEY=unchanged"}, Manager: manager}
	registry := extensions.NewRegistry(dir)
	if len(policy) > 0 {
		if err := registry.Register("permissions", plugins.Extension(policy[0], nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	host, err := agent.NewAgentSessionRuntime(context.Background(), agent.AgentSessionOptions{CWD: dir, AgentDir: dir, SessionManager: manager, ExtensionRegistry: registry}, Factory(options))
	driver, _ := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	return host, driver
}
func awaitInput(t *testing.T, s *agent.SessionRuntime) *agent.InputRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := s.PendingInput(); p != nil {
			return p
		}
		time.Sleep(time.Millisecond * 5)
	}
	if msg := s.State().ErrorMessage; msg != nil {
		t.Fatal(*msg)
	}
	t.Fatalf("no approval request; state: %#v", s.State())
	return nil
}

func TestSDKSessionResumeEventsAndApprovalIsolation(t *testing.T) {
	host, driver := fixture(t)
	if _, err := host.EnableControl(); err != nil {
		t.Fatal(err)
	}
	s := host.Session()
	models := s.AvailableModels()
	if len(models) != 3 || models[0].ID != "default" || !strings.Contains(models[2].Name, "claude-opus-current") {
		t.Fatalf("native catalog: %+v", models)
	}
	if err := s.SetModel(t.Context(), models[2]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThinkingLevel(engine.ThinkingMax); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel(t.Context(), ai.Model{ID: "foreign", Provider: "openai"}); err == nil {
		t.Fatal("executor changed in place")
	}
	if selectedModel(models, "claude-opus-current").ID != "claude-opus-current" {
		t.Fatal("explicit model ID lost")
	}
	updates, tools := 0, 0
	s.Subscribe(func(e any) {
		if _, ok := e.(engine.MessageUpdateEvent); ok {
			updates++
		}
		if _, ok := e.(engine.ToolExecutionEndEvent); ok {
			tools++
		}
	})
	for i := 0; i < 2; i++ {
		done := make(chan error, 1)
		go func() { done <- s.Prompt(context.Background(), "hello") }()
		input := awaitInput(t, s)
		if err := s.SetModel(t.Context(), models[1]); err != agent.ErrControlBusy {
			t.Fatalf("active model mutation: %v", err)
		}
		if err := s.ReplyInput("old", "Allow once"); err == nil {
			t.Fatal("stale approval accepted")
		}
		if err := s.ReplyInput(input.ID, "always"); err == nil {
			t.Fatal("unknown permission choice accepted")
		}
		if err := s.ReplyInput(input.ID, "Allow once"); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplyInput(input.ID, "Allow once"); err == nil {
			t.Fatal("approval reused")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if e := s.State().ErrorMessage; e != nil {
			t.Fatal(*e)
		}
	}
	saved, err := driver.checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Session != s.Manager().GetSessionID() || saved.At != "final-checkpoint" {
		t.Fatalf("checkpoint %#v", saved)
	}
	if updates != 2 || tools != 2 {
		t.Fatalf("updates %d tools %d", updates, tools)
	}
	messages := s.State().Messages
	raw, _ := json.Marshal(messages)
	if !strings.Contains(string(raw), `\"effort\":\"max\"`) || !strings.Contains(string(raw), `\"thinking\":{\"type\":\"adaptive\"}`) {
		t.Fatalf("native effort settings missing: %s", raw)
	}
	if !strings.Contains(string(raw), `\"resume\":\"`+saved.Session) {
		t.Fatalf("explicit resume missing: %s", raw)
	}
}

func TestSDKBridgeApprovalFencesAndCancellation(t *testing.T) {
	host, _ := fixture(t)
	attachment, err := connectagent.Attach(context.Background(), host, connectagent.Options{InstanceID: protocol.NewID(), Store: &testStore{}, Authorize: func(connect.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	// Use the exact control seam consumed by the attachment for execution replies.
	control, err := host.EnableControl()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Session().Prompt(context.Background(), "wait for permission") }()
	input := awaitInput(t, host.Session())
	target := control.Target()
	stale := target
	stale.ExecutionID = protocol.NewID()
	payload := string(connect.JSON(map[string]string{"id": input.ID, "value": "Allow once"}))
	if err = control.Execution(stale, "input.reply", payload); err == nil {
		t.Fatal("cross-execution reply accepted")
	}
	if err = control.Execution(target, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("SDK process did not stop")
	}
	if host.Session().PendingInput() != nil {
		t.Fatal("cancelled approval retained")
	}
	if err = control.Execution(target, "input.reply", payload); err == nil {
		t.Fatal("cancelled approval accepted")
	}
	if !host.Session().IsIdle() {
		t.Fatal("runtime remained busy")
	}
}

func TestModelSelectionKeepsExistingProvider(t *testing.T) {
	settings, _ := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(t.TempDir()))
	settings.SetPluginEnabled(Name, true)
	if Model(ptr("openai"), nil, settings) != nil {
		t.Fatal("existing provider replaced")
	}
	if Model(nil, nil, settings) != nil {
		t.Fatal("ordinary launch implicitly selected Claude")
	}
	for _, restored := range []session.SessionModel{{Provider: "openai", ModelID: "normal"}, {Provider: "unknown", ModelID: "unknown"}} {
		provider, model := ptr(Name), ptr("sonnet")
		RestoreSelection(session.SessionContext{Model: &restored}, &provider, &model)
		if restored.Provider == "unknown" {
			if provider != nil || model != nil {
				t.Fatal("Claude startup selection survived exit")
			}
		} else if provider == nil || *provider != restored.Provider || model == nil || *model != restored.ModelID {
			t.Fatal("could not return to Orb model")
		}
	}
}

type testStore struct{ data []byte }

func (s *testStore) Load() ([]byte, error) { return s.data, nil }
func (s *testStore) Save(b []byte) error   { s.data = append([]byte(nil), b...); return nil }

// Opt-in: uses the executing user's official Claude login and subscription limits.
func TestSDKLiveSession(t *testing.T) {
	sdk := os.Getenv("ORB_CLAUDE_LIVE_SDK")
	if sdk == "" {
		t.Skip("set ORB_CLAUDE_LIVE_SDK to the official sdk.mjs for a native account test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manager, err := session.Create(dir, filepath.Join(dir, "orb-sessions"))
	if err != nil {
		t.Fatal(err)
	}
	driver, err := New(Options{Node: node, Claude: claude, SDK: sdk, Env: os.Environ(), Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{CWD: dir, AgentDir: filepath.Join(dir, "orb-config"), SessionManager: manager, Model: &ai.Model{ID: "sonnet", Provider: Name, API: Name}, SessionLoop: driver.Loop, NoTools: "all", Resources: &agent.Resources{}})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, prompt := range []string{"Reply exactly ORB_CLAUDE_OK. Do not use tools.", "Reply exactly with the token from your previous response. Do not use tools."} {
		if err = result.Session.Prompt(ctx, prompt); err != nil {
			t.Fatal(err)
		}
		state := result.Session.State()
		if state.ErrorMessage != nil {
			t.Fatal(*state.ErrorMessage)
		}
		last := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage)
		raw, _ := json.Marshal(last.Content)
		if !strings.Contains(string(raw), "ORB_CLAUDE_OK") {
			t.Fatalf("native reply: %s", raw)
		}
	}
	saved, err := driver.checkpoint()
	if err != nil || saved.Session == "" || saved.At == "" {
		t.Fatalf("native checkpoint %#v %v", saved, err)
	}
	t.Logf("Native create/resume passed, session %s", saved.Session)
}

func TestSDKInstanceProtocolResumeForkAndDeduplication(t *testing.T) {
	host, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id := protocol.NewID()
	a, err := connectagent.Attach(ctx, host, connectagent.Options{InstanceID: id, Store: &testStore{}, Authorize: func(connect.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err = a.SetGeneration("1"); err != nil {
		t.Fatal(err)
	}
	control, _ := host.EnableControl()
	principal := connect.Principal{PeerID: "fixture", Subject: connect.Subject{Kind: "controller"}}
	call := func(method string, args any) connect.Request {
		target := control.Target()
		request := connect.Request{Principal: principal, Generation: "1", Call: connect.Call{InstanceID: id, Service: protocol.Service, Method: method, SessionID: target.SessionID, Expected: connect.Expected{Generation: "1", Revision: target.Revision}, OperationID: protocol.NewID(), Args: connect.JSON(args)}}
		if _, e := a.Invoke(ctx, "call", connect.JSON(request)); e != nil {
			t.Fatal(e)
		}
		return request
	}
	wait := func(request connect.Request) {
		for {
			raw, e := a.Invoke(ctx, "operations.get", connect.JSON(map[string]any{"principal": principal, "params": map[string]string{"instance_id": id, "operation_id": request.Call.OperationID}}))
			if e != nil {
				t.Fatal(e)
			}
			var receipt connect.Receipt
			if e = json.Unmarshal(raw, &receipt); e != nil {
				t.Fatal(e)
			}
			if receipt.Status == "succeeded" {
				return
			}
			if receipt.Status != "accepted" && receipt.Status != "running" {
				t.Fatalf("operation: %#v", receipt)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond * 5):
			}
		}
	}
	prompt := func() {
		request := call("prompt", map[string]string{"text": "bridge prompt"})
		input := awaitInput(t, host.Session())
		snapshot, e := a.Invoke(ctx, "instances.describe", connect.JSON(map[string]any{"principal": principal, "params": map[string]string{"instance_id": id}}))
		if e != nil || !strings.Contains(string(snapshot), input.ID) {
			t.Fatalf("input missing from descriptor: %s %v", snapshot, e)
		}
		reply := call("input.reply", map[string]string{"execution_id": control.Target().ExecutionID, "id": input.ID, "value": "Allow once"})
		wait(reply)
		wait(request)
		if _, e = a.Invoke(ctx, "call", connect.JSON(request)); e != nil {
			t.Fatal("identical prompt retry", e)
		}
		request.Call.Args = connect.JSON(map[string]string{"text": "conflicting prompt"})
		if _, e = a.Invoke(ctx, "call", connect.JSON(request)); connect.Code(e) != "operation_conflict" {
			t.Fatal("conflicting retry accepted", e)
		}
	}
	wait(call("session.model", map[string]string{"provider": Name, "model": "opus", "thinking": "high"}))
	if state := host.Session().State(); state.Model.ID != "opus" || state.ThinkingLevel != engine.ThinkingHigh {
		t.Fatal("remote model/effort not applied")
	}

	prompt()
	prompt()
	original := host.Session().Manager().GetSessionID()
	var secondUser string
	for _, entry := range host.Session().Manager().GetBranch() {
		if entry.Type == "message" && strings.Contains(string(entry.Message), `"role":"user"`) {
			secondUser = entry.ID
		}
	}
	if secondUser == "" {
		t.Fatal("missing fork point")
	}
	wait(call("session.fork", map[string]string{"entry_id": secondUser}))
	forkID := host.Session().Manager().GetSessionID()
	if forkID == original {
		t.Fatal("fork reused original ID")
	}
	prompt()
	raw, _ := json.Marshal(host.Session().State().Messages)
	if !strings.Contains(string(raw), `\"fork\":true`) {
		t.Fatal("native fork option missing")
	}
	wait(call("session.switch", map[string]string{"session_id": original}))
	if host.Session().Manager().GetSessionID() != original {
		t.Fatal("wrong resumed session")
	}
	prompt()
	wait(call("session.new", struct{}{}))
	if host.Session().Manager().GetSessionID() == original {
		t.Fatal("new session reused native ID")
	}
	prompt()
}

func TestSDKLiveBridgeToolsAndFork(t *testing.T) {
	sdk := os.Getenv("ORB_CLAUDE_LIVE_SDK")
	if sdk == "" {
		t.Skip("native account test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manager, err := session.Create(dir, filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(dir)
	policy := &plugins.Policy{Mode: "enforce", AskFallback: plugins.Deny, Rules: []plugins.Rule{
		{Tool: "write", Path: filepath.Join(dir, "proof.txt"), Action: plugins.Ask},
		{Tool: "read", Path: filepath.Join(dir, "blocked.txt"), Action: plugins.Deny},
	}}
	if err := registry.Register("permissions", plugins.Extension(policy, nil, nil)); err != nil {
		t.Fatal(err)
	}
	factory := Factory(Options{Node: node, Claude: cli, SDK: sdk, Env: os.Environ()})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{CWD: dir, AgentDir: filepath.Join(dir, "config"), SessionManager: manager, ExtensionRegistry: registry}, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	server, err := bridge.Open(&testStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	client, err := bridge.Open(&testStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	permissions := []string{"instance.list", "instance.inspect", "instance.prompt", "instance.input.reply", "instance.cancel", "instance.session.manage"}
	invite, err := server.Invite([]bridge.Grant{{GroupID: server.PersonalGroup(), IncludeFuture: true, Permissions: permissions}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Claim(client.PeerID(), invite.ID, invite.Token); err != nil {
		t.Fatal(err)
	}
	if err = server.Approve(invite.ID, client.PeerID()); err != nil {
		t.Fatal(err)
	}
	instance, token, err := server.Enroll("claude-live", server.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	a, err := connectagent.Attach(ctx, host, connectagent.Options{InstanceID: instance.ID, Store: &testStore{}, Authorize: func(r connect.Request) bool {
		permission := "instance." + r.Call.Method
		if strings.HasPrefix(r.Call.Method, "session.") {
			permission = "instance.session.manage"
		}
		return server.Allowed(r.Principal, instance.ID, permission)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	generation, err := server.Attach(instance.ID, token, protocol.NewID(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SetGeneration(generation); err != nil {
		t.Fatal(err)
	}
	control, _ := host.EnableControl()
	invoke := func(method string, args any) string {
		target := control.Target()
		id := protocol.NewID()
		_, e := server.Call(ctx, client.Principal(), connect.Call{InstanceID: instance.ID, Service: protocol.Service, Method: method, SessionID: target.SessionID, Expected: connect.Expected{Generation: generation, Revision: target.Revision}, OperationID: id, Args: connect.JSON(args)})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	approvals, questions := 0, 0
	wait := func(id string, allowWrite bool) {
		for {
			if p := host.Session().PendingInput(); p != nil {
				value := "Deny"
				if allowWrite && strings.HasPrefix(p.Title, "Permission requested for write") && strings.Contains(p.Title, dir) {
					value = "s approve for this session"
					approvals++
				}
				if strings.Contains(p.Title, "Which diagram should I draw?") {
					value = `{"answers":[{"id":"1","selected":["Architecture"]}]}`
					questions++
				}
				invoke("input.reply", map[string]string{"execution_id": control.Target().ExecutionID, "id": p.ID, "value": value})
			}
			raw, e := a.Invoke(ctx, "operations.get", connect.JSON(map[string]any{"principal": client.Principal(), "params": map[string]string{"instance_id": instance.ID, "operation_id": id}}))
			if e != nil {
				t.Fatal(e)
			}
			var receipt connect.Receipt
			if e = json.Unmarshal(raw, &receipt); e != nil {
				t.Fatal(e)
			}
			if receipt.Status == "succeeded" {
				return
			}
			if receipt.Status != "accepted" && receipt.Status != "running" {
				t.Fatalf("receipt %#v", receipt)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond * 100):
			}
		}
	}
	models := host.Session().AvailableModels()
	if len(models) < 2 {
		t.Fatalf("native model catalog incomplete: %+v", models)
	}
	t.Logf("Native catalog: %d models; default %s", len(models), models[0].Name)
	wait(invoke("session.model", map[string]string{"provider": Name, "model": "sonnet", "thinking": "low"}), false)
	target := filepath.Join(dir, "proof.txt")
	id := invoke("prompt", map[string]string{"text": "Use the Write tool to create " + target + " containing exactly ORB_BRIDGE_TOOL_OK. Then reply DONE. Do not run any other tools."})
	wait(id, true)
	data, err := os.ReadFile(target)
	if err != nil || strings.TrimSpace(string(data)) != "ORB_BRIDGE_TOOL_OK" {
		t.Fatalf("native tool proof %q %v", data, err)
	}
	if approvals != 1 {
		t.Fatalf("expected one Orb approval, got %d", approvals)
	}
	auto := filepath.Join(dir, "auto.txt")
	wait(invoke("prompt", map[string]string{"text": "Use Write to create " + auto + " containing exactly AUTO_ALLOWED. No other tools."}), false)
	if data, err := os.ReadFile(auto); err != nil || strings.TrimSpace(string(data)) != "AUTO_ALLOWED" {
		t.Fatalf("Orb allow did not authorize native Write: %q %v", data, err)
	}
	blocked := filepath.Join(dir, "blocked.txt")
	if err := os.WriteFile(blocked, []byte("PRIVATE_TEST_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(invoke("prompt", map[string]string{"text": "Use Read exactly once on " + blocked + ". If denied, report denial and do not try any other tool."}), false)
	denied := false
	for _, entry := range manager.GetEntries() {
		if entry.CustomType != "orb.permissions.decision" {
			continue
		}
		var decision plugins.Decision
		if json.Unmarshal(entry.Data, &decision) == nil && decision.Tool == "read" && decision.Resolved == plugins.Deny {
			denied = true
		}
	}
	if !denied {
		t.Fatal("Orb deny did not intercept an ordinarily auto-approved native Read")
	}
	t.Log("Orb policy: asked once, auto-approved native Write, denied native Read")
	wait(invoke("prompt", map[string]string{"text": "Use AskUserQuestion to ask exactly 'Which diagram should I draw?' with two options: Architecture (show the system structure) and Sequence (show request flow). After I answer, reply exactly SELECTED_ARCHITECTURE if I choose Architecture. Do not use any other tools."}), false)
	questionMessages := host.Session().State().Messages
	questionResult, _ := json.Marshal(questionMessages[len(questionMessages)-1])
	if questions != 1 || !strings.Contains(string(questionResult), "SELECTED_ARCHITECTURE") {
		t.Fatalf("native Bridge question did not resume: %d replies, %s", questions, questionResult)
	}
	t.Log("Native AskUserQuestion answered through Bridge input.reply and resumed")
	wait(invoke("session.model", map[string]string{"provider": Name, "model": "haiku", "thinking": "off"}), false)
	original := host.Session().Manager().GetSessionID()
	id = invoke("prompt", map[string]string{"text": "Reply exactly SECOND_TURN. Do not use tools."})
	wait(id, false)
	messages := host.Session().State().Messages
	assistant, ok := messages[len(messages)-1].(*ai.AssistantMessage)
	if !ok || !strings.Contains(assistant.Model, "haiku") {
		t.Fatalf("model switch did not reach Claude: %#v", messages[len(messages)-1])
	}

	var forkAt string
	for _, entry := range host.Session().Manager().GetBranch() {
		if entry.Type == "message" && strings.Contains(string(entry.Message), `"role":"user"`) {
			forkAt = entry.ID
		}
	}
	wait(invoke("session.fork", map[string]string{"entry_id": forkAt}), false)
	if host.Session().Manager().GetSessionID() == original {
		t.Fatal("fork identity unchanged")
	}
	wait(invoke("prompt", map[string]string{"text": "What exact text did you write into proof.txt? Answer only that text from memory, no tools."}), false)
	state := host.Session().State()
	if state.ErrorMessage != nil {
		t.Fatal(*state.ErrorMessage)
	}
	raw, _ := json.Marshal(state.Messages[len(state.Messages)-1])
	if !strings.Contains(string(raw), "ORB_BRIDGE_TOOL_OK") {
		t.Fatalf("fork lost native context: %s", raw)
	}
	t.Logf("Pair, Bridge routing, native Write, resume and fork passed; %d explicit Write approvals", approvals)
	started := make(chan struct{}, 1)
	stop := host.Session().Subscribe(func(event any) {
		if tool, ok := event.(engine.ToolExecutionStartEvent); ok && tool.ToolName == "Bash" {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	defer stop()
	running := invoke("prompt", map[string]string{"text": "Use Bash to run exactly sleep 30, then reply DONE. No other tools."})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("native Bash did not start")
	}
	began := time.Now()
	wait(invoke("cancel", map[string]string{"execution_id": control.Target().ExecutionID}), false)
	wait(running, false)
	if time.Since(began) > 7*time.Second {
		t.Fatal("native cancellation exceeded cleanup bound")
	}
	state = host.Session().State()
	last, ok := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage)
	if !ok || last.StopReason != ai.StopReasonAborted {
		t.Fatalf("cancel did not settle aborted: %#v", last)
	}
	t.Logf("Native Bridge cancellation settled in %s", time.Since(began).Round(time.Millisecond))
}

func TestSDKIndependentInstancesCancelSeparately(t *testing.T) {
	first, _ := fixture(t)
	second, _ := fixture(t)
	_, _ = first.EnableControl()
	_, _ = second.EnableControl()
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- first.Session().Prompt(context.Background(), "first") }()
	go func() { doneB <- second.Session().Prompt(context.Background(), "second") }()
	a, b := awaitInput(t, first.Session()), awaitInput(t, second.Session())
	first.Session().Abort()
	<-doneA
	if first.Session().PendingInput() != nil || second.Session().PendingInput() == nil {
		t.Fatal("cancellation crossed instance boundary")
	}
	if err := second.Session().ReplyInput(a.ID, "Allow once"); err == nil {
		t.Fatal("foreign approval accepted")
	}
	if err := second.Session().ReplyInput(b.ID, "Allow once"); err != nil {
		t.Fatal(err)
	}
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	if err := second.Session().State().ErrorMessage; err != nil {
		t.Fatal(*err)
	}
}

func TestAutomaticSDKSetup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf 'attempt\n' >> "$3/attempts"
mkdir -p "$3/node_modules/@anthropic-ai/claude-agent-sdk"
printf 'fixture' > "$3/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs"
if [ ! -f "$3/retry" ]; then touch "$3/retry"; exit 1; fi
`
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if err := installSDK(t.Context(), dir, env); err == nil {
		t.Fatal("failed installation accepted")
	}
	for range 2 {
		if err := installSDK(t.Context(), dir, env); err != nil {
			t.Fatal(err)
		}
	}
	attempts, err := os.ReadFile(filepath.Join(dir, "plugins", Name, "attempts"))
	if err != nil || string(attempts) != "attempt\nattempt\n" {
		t.Fatalf("setup did not retry once and reuse: %q %v", attempts, err)
	}
	settings, err := config.NewSettingsManager(dir, config.WithAgentDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetPluginSetting(Name, "node", filepath.Join(bin, "npm"))
	settings.SetPluginSetting(Name, "claude", filepath.Join(bin, "npm"))
	options, err := configuredOptions(t.Context(), settings, dir, env)
	if err != nil || options.SDK == "" {
		t.Fatalf("automatic startup: %v", err)
	}
	settings.SetPluginSetting(Name, "sdk", filepath.Join(dir, "custom-missing.mjs"))
	if _, err = configuredOptions(t.Context(), settings, dir, env); err == nil {
		t.Fatal("custom SDK path silently replaced")
	}
}

func TestSDKQuestionsPreserveCustomAndMultipleAnswers(t *testing.T) {
	host, _ := fixture(t)
	if _, err := host.EnableControl(); err != nil {
		t.Fatal(err)
	}
	s := host.Session()
	done := make(chan error, 1)
	go func() { done <- s.Prompt(t.Context(), "question-fixture") }()
	p := awaitInput(t, s)
	if p.Presentation == nil || p.Presentation.Kind != questions.Kind {
		t.Fatal("shared question presentation missing")
	}
	var request questions.Request
	if err := json.Unmarshal(p.Presentation.Data, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Questions) != 2 || !strings.Contains(request.Questions[0].Options[0].Description, "runtime boundaries") {
		t.Fatal("question details lost")
	}
	if err := s.ReplyInput(p.ID, `{"answers":[]}`); err == nil {
		t.Fatal("incomplete answer consumed the pending question")
	}
	rawAnswer, _ := json.Marshal(questions.Result{Answers: []questions.Answer{{ID: "1", Selected: []string{}, Custom: "Custom diagram"}, {ID: "2", Selected: []string{"Bridge"}}}})
	if err := s.ReplyInput(p.ID, string(rawAnswer)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.State().Messages)
	if !strings.Contains(string(raw), `\"What should be diagrammed?\":\"Custom diagram\"`) || !strings.Contains(string(raw), `\"Which details?\":\"Bridge\"`) {
		t.Fatalf("native answers lost: %s", raw)
	}
	definition := s.GetToolDefinition("AskUserQuestion")
	if definition == nil || definition.RenderCall == nil {
		t.Fatal("native question renderer missing")
	}
	summary := toolSummary("AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "Question text", "options": []any{map[string]string{"label": "Choice", "description": "Details"}}}}})
	if !strings.Contains(summary, "Question text") || !strings.Contains(summary, "Details") {
		t.Fatal(summary)
	}
}

type testText string

func (t testText) Render(int) []string { return []string{string(t)} }

func TestSDKUsesOrbPermissionPolicy(t *testing.T) {
	for _, action := range []plugins.Action{plugins.Allow, plugins.Deny, plugins.Ask} {
		t.Run(string(action), func(t *testing.T) {
			policy := &plugins.Policy{Mode: "enforce", AskFallback: plugins.Deny, Rules: []plugins.Rule{{Tool: "write", Path: "/fixture", Action: action}}}
			host, _ := fixture(t, policy)
			if _, err := host.EnableControl(); err != nil {
				t.Fatal(err)
			}
			s := host.Session()
			done := make(chan error, 1)
			go func() { done <- s.Prompt(t.Context(), "native write") }()
			if action == plugins.Ask {
				p := awaitInput(t, s)
				if !strings.Contains(p.Title, "Permission requested for write") {
					t.Fatal(p.Title)
				}
				if err := s.ReplyInput(p.ID, "s approve for this session"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("policy did not resolve native call")
			}
			if denied := s.State().ErrorMessage != nil; denied != (action == plugins.Deny) {
				t.Fatalf("denied=%v, action=%s", denied, action)
			}
			if action == plugins.Ask {
				go func() { done <- s.Prompt(t.Context(), "same native write") }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("session approval was not reused")
				}
			}
			found := false
			for _, entry := range s.Manager().GetEntries() {
				if entry.CustomType == "orb.permissions.decision" {
					found = true
				}
			}
			if !found {
				t.Fatal("native decision was not audited")
			}
		})
	}
}
