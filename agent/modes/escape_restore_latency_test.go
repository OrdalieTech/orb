package modes

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/engine"
)

func TestAbortRestoresEditorBeforeRunSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mode := newRestoreMode(t)
		manager := mode.session.Manager()
		if err := os.Remove(manager.GetSessionFile()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if _, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"unanswered prompt"}]}`)); err != nil {
			t.Fatal(err)
		}
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		runningAgent := engine.NewAgent(nil, engine.WithSessionLoop(func(ctx context.Context, _ engine.AgentMessages, _ engine.AgentContext, _ engine.AgentLoopConfig, _ engine.EventSink) error {
			close(started)
			<-ctx.Done()
			<-release
			return ctx.Err()
		}))
		settings, err := config.NewSettingsManager(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		mode.session, err = agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: runningAgent, SessionManager: manager, Settings: settings})
		if err != nil {
			t.Fatal(err)
		}
		defer mode.session.Dispose()
		go func() {
			_ = runningAgent.Prompt(context.Background(), "unanswered prompt")
			close(done)
		}()
		<-started
		if err := mode.session.Steer("queued steering"); err != nil {
			t.Fatal(err)
		}
		mode.setActiveEditorText("draft")
		escapeAt := time.Now()
		mode.abortAndRestore(false)
		synctest.Wait()
		got := mode.activeEditorText(false)
		want := "unanswered prompt\n\nqueued steering\n\ndraft"
		t.Logf("editor restored after %s with backend cancellation still blocked", time.Since(escapeAt))
		mode.setActiveEditorText("edited before abort settled")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		close(release)
		<-done
		synctest.Wait()
		if got != want {
			t.Errorf("editor while cancelled run is still blocked = %q, want %q", got, want)
		}
		if after := mode.activeEditorText(false); after != "edited before abort settled" {
			t.Errorf("abort completion replaced the draft with %q", after)
		}
		time.Sleep(statusNoticeLifetime + time.Second)
		synctest.Wait()
	})
}
