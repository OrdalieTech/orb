package codingagent

import (
	"context"
	"errors"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

type PromptOptions struct {
	ExpandPromptTemplates *bool
	Images                []*ai.ImageContent
	StreamingBehavior     extensions.DeliveryMode
	Source                extensions.InputSource
	PreflightResult       func(bool)
}

type CustomMessage = extensions.CustomMessage
type SendCustomMessageOptions = extensions.SendMessageOptions
type SendUserMessageOptions = extensions.SendUserMessageOptions

func (runtime *SessionRuntime) Agent() *agent.Agent {
	if runtime == nil {
		return nil
	}
	return runtime.agent
}

func (runtime *SessionRuntime) GetActiveToolNames() []string {
	if runtime == nil {
		return []string{}
	}
	names, _ := runtime.extensionActiveTools()
	return names
}

func (runtime *SessionRuntime) SetActiveToolsByName(names []string) error {
	if runtime == nil {
		return nil
	}
	return runtime.setActiveToolsByName(names)
}

func (runtime *SessionRuntime) PromptWithOptions(ctx context.Context, text string, options *PromptOptions) error {
	expand := true
	var images []*ai.ImageContent
	source := extensions.InputInteractive
	var streamingBehavior *extensions.DeliveryMode
	var preflightResult func(bool)
	if options != nil {
		if options.ExpandPromptTemplates != nil {
			expand = *options.ExpandPromptTemplates
		}
		images = options.Images
		if options.Source != "" {
			source = options.Source
		}
		if options.StreamingBehavior != "" {
			behavior := options.StreamingBehavior
			streamingBehavior = &behavior
		}
		preflightResult = options.PreflightResult
	}
	if runtime == nil {
		if preflightResult != nil {
			preflightResult(false)
		}
		return errors.New("codingagent: nil session runtime")
	}
	if runtime.extensionState != nil {
		return runtime.promptExtensionInput(ctx, text, images, source, expand, streamingBehavior, true, preflightResult)
	}
	if expand && runtime.slashResolver != nil {
		var handled bool
		text, handled = runtime.slashResolver.ResolvePrompt(text)
		if handled {
			if preflightResult != nil {
				preflightResult(true)
			}
			return nil
		}
	}
	if !runtime.agent.IsIdle() {
		if streamingBehavior == nil {
			if preflightResult != nil {
				preflightResult(false)
			}
			return errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.") //nolint:staticcheck // User-visible error matches upstream.
		}
		if preflightResult != nil {
			preflightResult(true)
		}
		message := userMessageWithImagesAt(text, images, runtime.clock())
		if *streamingBehavior == extensions.DeliverFollowUp {
			runtime.agent.FollowUp(message)
		} else {
			runtime.agent.Steer(message)
		}
		return nil
	}
	if err := runtime.PromptPreflight(ctx); err != nil {
		if preflightResult != nil {
			preflightResult(false)
		}
		return err
	}
	if preflightResult != nil {
		preflightResult(true)
	}
	return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, text, images...) })
}

func (runtime *SessionRuntime) SendUserMessage(ctx context.Context, content ai.UserContent, options *SendUserMessageOptions) error {
	return runtime.sendExtensionUserMessage(ctx, content, options)
}

func (runtime *SessionRuntime) SendCustomMessage(ctx context.Context, message CustomMessage, options *SendCustomMessageOptions) error {
	return runtime.sendExtensionMessage(ctx, message, options)
}
