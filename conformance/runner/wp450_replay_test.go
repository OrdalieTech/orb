package runner_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type wp450ReplayFixture struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Frames        []modes.ConformanceReplayFrame `json:"frames"`
}

func TestWP450SideBySideReplayMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "WP450")
	if manifest.Family != "WP450" || manifest.Generator != "conformance/extract/wp450-replay.ts" {
		t.Fatalf("unexpected WP450 manifest: %+v", manifest)
	}
	var fixture wp450ReplayFixture
	runner.LoadJSON(t, "WP450", "replay.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Frames) != 20 {
		t.Fatalf("WP450 replay header = version %d, frames %d", fixture.SchemaVersion, len(fixture.Frames))
	}

	want := make(map[string]modes.ConformanceReplayFrame, len(fixture.Frames))
	for _, frame := range fixture.Frames {
		key := wp450FrameKey(frame)
		if _, exists := want[key]; exists {
			t.Fatalf("duplicate upstream frame %s", key)
		}
		want[key] = frame
	}
	gotFrames := modes.RenderWP450ConformanceReplay()
	got := make(map[string]modes.ConformanceReplayFrame, len(gotFrames))
	for _, frame := range gotFrames {
		key := wp450FrameKey(frame)
		if _, exists := got[key]; exists {
			t.Fatalf("duplicate Go frame %s", key)
		}
		got[key] = frame
	}

	for _, expected := range fixture.Frames {
		expected := expected
		key := wp450FrameKey(expected)
		t.Run(key, func(t *testing.T) {
			actual, ok := got[key]
			if !ok {
				t.Fatalf("Go replay omitted frame %s", key)
			}
			if diff := linesDiff(expected.Lines, actual.Lines); diff != "" {
				t.Fatalf("normalized visible frame differs:\n%s", diff)
			}
		})
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("Go replay emitted unexpected frame %s", key)
		}
	}
}

func TestWP450CtxUIDemosRetainInitializationState(t *testing.T) {
	var expected modes.ConformanceUIDemoArtifact
	runner.LoadJSON(t, "WP450", "ui-demos.json", &expected)
	if expected.SchemaVersion != 1 || len(expected.StatusLine.Events) != 3 {
		t.Fatalf("unexpected WP450 ctx.ui fixture header: %+v", expected)
	}
	actual := modes.RenderWP450UIDemoArtifact()
	if reflect.DeepEqual(actual, expected) {
		return
	}
	wantJSON, _ := json.MarshalIndent(expected, "", "  ")
	gotJSON, _ := json.MarshalIndent(actual, "", "  ")
	t.Fatalf("ctx.ui demo state differs\nwant: %s\n got: %s", wantJSON, gotJSON)
}

func wp450FrameKey(frame modes.ConformanceReplayFrame) string {
	return fmt.Sprintf("%d/%s", frame.Width, frame.ID)
}
