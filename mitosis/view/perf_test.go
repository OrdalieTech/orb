package view

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	attach "github.com/OrdalieTech/orb/agent/mitosis"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

func BenchmarkTranscriptStreamingAndDiff(b *testing.B) {
	b.Run("Stream500Deltas", func(b *testing.B) {
		events := make([]json.RawMessage, 0, 502)
		events = append(events, json.RawMessage(`{"type":"message_start","message":{"role":"assistant","content":[]}}`))
		var sb strings.Builder
		for i := range 500 {
			sb.WriteString("token ")
			if i%20 == 19 {
				sb.WriteByte('\n')
			}
			raw, _ := json.Marshal(map[string]any{
				"type": "message_update",
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "text", "text": sb.String()},
					},
				},
			})
			events = append(events, raw)
		}
		events = append(events, json.RawMessage(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"`+strings.ReplaceAll(sb.String(), "\n", `\n`)+`"}]}}`))

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			var tr transcript
			totalRows := 0
			for _, e := range events {
				tr.apply(e)
				rows, _ := tr.rows()
				totalRows += len(rows)
			}
			if totalRows == 0 {
				b.Fatal("no rows produced")
			}
		}
	})
}

func TestViewNetworkBudgetOpenIdleAndTurn(t *testing.T) {
	ctx := t.Context()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("Here is a concise answer after checking the workspace."),
	})
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })

	m, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	peers := mitosis.NewPeers(ctx, m)
	inst, token, err := m.Enroll("work")
	if err != nil {
		t.Fatal(err)
	}
	att, err := attach.Attach(ctx, host, attach.Options{
		InstanceID: inst.ID, Store: &document.Memory{},
		Authorize: func(mitosis.Request) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = att.Close() })
	gen, err := m.Attach(inst.ID, token, protocol.NewID(), mitosis.NewLocal(att.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	_ = att.SetGeneration(gen)

	type rpcStat struct {
		count    int
		reqBytes int
		resBytes int
	}
	var mu sync.Mutex
	stats := map[string]*rpcStat{}
	resetStats := func() map[string]rpcStat {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]rpcStat{}
		for k, v := range stats {
			out[k] = *v
		}
		clear(stats)
		return out
	}

	c := newClient()
	app := New(ctx, Options{
		Name: "bench-mac",
		Emit: func(msg any) { c.out <- mitosis.JSON(msg) },
		Call: func(callCtx context.Context, method string, params, result any) error {
			reqRaw := mitosis.JSON(params)
			label := method
			if method == "remote" {
				var r struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(reqRaw, &r)
				if r.Method != "" {
					label = "remote:" + r.Method
				}
			}
			var raw json.RawMessage
			raw, err := peers.Admin(callCtx, method, reqRaw)
			if err == nil && result != nil {
				err = json.Unmarshal(raw, result)
			}
			mu.Lock()
			st := stats[label]
			if st == nil {
				st = &rpcStat{}
				stats[label] = st
			}
			st.count++
			st.reqBytes += len(reqRaw)
			st.resBytes += len(raw)
			mu.Unlock()
			return err
		},
	})

	app.Do([]byte(`{"do":"hello","budget":4}`))
	app.Do([]byte(`{"do":"visible","on":true}`))
	sessID := host.Session().Manager().GetSessionID()
	c.until(t, "instance visible on Home", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:"+sessID })
	})
	resetStats()

	// Phase 1: Open tab & load initial transcript.
	app.Do([]byte(`{"id":"open1","do":"open","key":"i:` + sessID + `"}`))
	c.until(t, "open reply", func() bool { return c.replies["open1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["open1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "tab loaded", func() bool {
		tab := c.tab(opened.Tab)
		return tab.ID != "" && tab.Loaded && tab.Online
	})
	openStats := resetStats()

	// Phase 2: Sit completely idle for 2 seconds with tab open.
	time.Sleep(2 * time.Second)
	idleStats := resetStats()

	// Phase 3: Send one prompt and wait for answer.
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"hello"}`))
	c.until(t, "answer rendered", func() bool {
		rows := c.rows[opened.Tab]
		return len(rows) >= 2 && !c.tab(opened.Tab).Busy
	})
	time.Sleep(200 * time.Millisecond)
	turnStats := resetStats()

	logPhase := func(name string, m map[string]rpcStat) {
		var totalCalls, totalBytes int
		var parts []string
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			s := m[k]
			totalCalls += s.count
			totalBytes += s.reqBytes + s.resBytes
			parts = append(parts, fmt.Sprintf("%s=%dx(%dB)", k, s.count, s.reqBytes+s.resBytes))
		}
		t.Logf("%-14s total=%2d RPCs, %6d B | %s", name, totalCalls, totalBytes, strings.Join(parts, ", "))
	}
	logPhase("open+load:", openStats)
	logPhase("idle 2s:", idleStats)
	logPhase("1-turn prompt:", turnStats)
}
