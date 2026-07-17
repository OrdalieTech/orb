package ai_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/internal/partialjson"
)

func TestF1Serialization(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name  string  `json:"name"`
			Kind  string  `json:"kind"`
			JSON  string  `json:"json"`
			Input *string `json:"input,omitempty"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F1", "cases.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			got, err := roundTripFixture(fixtureCase.Kind, []byte(fixtureCase.JSON), fixtureCase.Input)
			if err != nil {
				t.Fatal(err)
			}
			want, err := runner.CanonicalJSONLexemes([]byte(fixtureCase.JSON))
			if err != nil {
				t.Fatal(err)
			}
			got, err = runner.CanonicalJSONLexemes(got)
			if err != nil {
				t.Fatal(err)
			}
			if diff := runner.ByteDiff(want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func roundTripFixture(kind string, data []byte, input *string) ([]byte, error) {
	switch kind {
	case "message":
		message, err := ai.UnmarshalMessage(data)
		if err != nil {
			return nil, err
		}
		return ai.MarshalMessage(message)
	case "context":
		var context ai.Context
		if err := json.Unmarshal(data, &context); err != nil {
			return nil, err
		}
		return ai.Marshal(context)
	case "event":
		event, err := ai.UnmarshalAssistantMessageEvent(data)
		if err != nil {
			return nil, err
		}
		return ai.MarshalAssistantMessageEvent(event)
	case "images":
		var images ai.AssistantImages
		if err := json.Unmarshal(data, &images); err != nil {
			return nil, err
		}
		return ai.Marshal(images)
	case "model":
		var model ai.Model
		if err := json.Unmarshal(data, &model); err != nil {
			return nil, err
		}
		return ai.Marshal(model)
	case "imagesModel":
		var model ai.ImagesModel
		if err := json.Unmarshal(data, &model); err != nil {
			return nil, err
		}
		return ai.Marshal(model)
	case "partialToolEvent":
		if input == nil {
			return nil, fmt.Errorf("partial tool event is missing input")
		}
		event, err := ai.UnmarshalAssistantMessageEvent(data)
		if err != nil {
			return nil, err
		}
		delta, ok := event.(ai.ToolCallDeltaEvent)
		if !ok || delta.Partial == nil || len(delta.Partial.Content) == 0 {
			return nil, fmt.Errorf("partial tool event has shape %T", event)
		}
		call, ok := delta.Partial.Content[0].(*ai.ToolCall)
		if !ok {
			return nil, fmt.Errorf("partial tool content has shape %T", delta.Partial.Content[0])
		}
		arguments, ok := partialjson.ParseStreamingJSON(*input).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("partial tool arguments are not an object")
		}
		call.Arguments = arguments
		return ai.MarshalAssistantMessageEvent(delta)
	default:
		return nil, fmt.Errorf("unknown F1 fixture kind %q", kind)
	}
}
