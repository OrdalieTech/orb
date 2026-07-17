package faux

import (
	"context"
	"errors"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
)

func (provider *Provider) streamWithDeltas(
	ctx context.Context,
	yield func(ai.AssistantMessageEvent, error) bool,
	message *ai.AssistantMessage,
) error {
	partial := *message
	partial.Content = ai.AssistantContent{}
	if ctx.Err() != nil {
		yield(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: provider.createAbortedMessage(&partial)}, nil)
		return nil
	}
	if !yield(ai.StartEvent{Partial: shallowSnapshot(&partial)}, nil) {
		return nil
	}

	for index, rawBlock := range message.Content {
		if ctx.Err() != nil {
			yield(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: provider.createAbortedMessage(&partial)}, nil)
			return nil
		}

		switch block := rawBlock.(type) {
		case *ai.ThinkingContent:
			streaming := &ai.ThinkingContent{}
			partial.Content = appendCopy(partial.Content, streaming)
			if !yield(ai.ThinkingStartEvent{ContentIndex: index, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}
			var accumulated []uint16
			for _, chunk := range splitUTF16ByTokenSize(block.Thinking, provider.minTokenSize, provider.maxTokenSize) {
				provider.scheduleChunk(chunk.text)
				if ctx.Err() != nil {
					yield(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: provider.createAbortedMessage(&partial)}, nil)
					return nil
				}
				accumulated = append(accumulated, chunk.units...)
				streaming.Thinking = stringFromUTF16(accumulated)
				if !yield(ai.ThinkingDeltaEvent{ContentIndex: index, Delta: chunk.text, Partial: shallowSnapshot(&partial)}, nil) {
					return nil
				}
			}
			if !yield(ai.ThinkingEndEvent{ContentIndex: index, Content: block.Thinking, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}

		case *ai.TextContent:
			streaming := &ai.TextContent{}
			partial.Content = appendCopy(partial.Content, streaming)
			if !yield(ai.TextStartEvent{ContentIndex: index, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}
			var accumulated []uint16
			for _, chunk := range splitUTF16ByTokenSize(block.Text, provider.minTokenSize, provider.maxTokenSize) {
				provider.scheduleChunk(chunk.text)
				if ctx.Err() != nil {
					yield(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: provider.createAbortedMessage(&partial)}, nil)
					return nil
				}
				accumulated = append(accumulated, chunk.units...)
				streaming.Text = stringFromUTF16(accumulated)
				if !yield(ai.TextDeltaEvent{ContentIndex: index, Delta: chunk.text, Partial: shallowSnapshot(&partial)}, nil) {
					return nil
				}
			}
			if !yield(ai.TextEndEvent{ContentIndex: index, Content: block.Text, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}

		case *ai.ToolCall:
			streaming := &ai.ToolCall{ID: block.ID, Name: block.Name, Arguments: map[string]any{}}
			partial.Content = appendCopy(partial.Content, streaming)
			if !yield(ai.ToolCallStartEvent{ContentIndex: index, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}
			arguments, err := ai.MarshalToolCallArguments(block)
			if err != nil {
				return err
			}
			for _, chunk := range splitUTF16ByTokenSize(string(arguments), provider.minTokenSize, provider.maxTokenSize) {
				provider.scheduleChunk(chunk.text)
				if ctx.Err() != nil {
					yield(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: provider.createAbortedMessage(&partial)}, nil)
					return nil
				}
				if !yield(ai.ToolCallDeltaEvent{ContentIndex: index, Delta: chunk.text, Partial: shallowSnapshot(&partial)}, nil) {
					return nil
				}
			}
			if err := ai.SetToolCallArgumentsJSON(streaming, arguments); err != nil {
				return err
			}
			if !yield(ai.ToolCallEndEvent{ContentIndex: index, ToolCall: block, Partial: shallowSnapshot(&partial)}, nil) {
				return nil
			}

		default:
			return errors.New("faux: unsupported assistant content block")
		}
	}

	if message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted {
		yield(ai.ErrorEvent{Reason: message.StopReason, Error: message}, nil)
		return nil
	}
	yield(ai.DoneEvent{Reason: message.StopReason, Message: message}, nil)
	return nil
}

func (provider *Provider) scheduleChunk(chunk string) {
	if provider.tokensPerSecond <= 0 {
		return
	}
	delay := time.Duration(float64(estimateTokens(chunk)) / provider.tokensPerSecond * float64(time.Second))
	if delay > 0 {
		time.Sleep(delay)
	}
}

func shallowSnapshot(message *ai.AssistantMessage) *ai.AssistantMessage {
	copy := *message
	return &copy
}

func appendCopy(content ai.AssistantContent, block ai.AssistantContentBlock) ai.AssistantContent {
	result := make(ai.AssistantContent, len(content), len(content)+1)
	copy(result, content)
	return append(result, block)
}
