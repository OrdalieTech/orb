package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// GenerateImages dispatches an image-generation model to its API-shape
// adapter, the images counterpart of StreamSimple. Only the OpenRouter shape
// exists upstream today.
func GenerateImages(
	ctx context.Context,
	model *ai.ImagesModel,
	imagesContext ai.ImagesContext,
	options *ai.ImagesOptions,
) (*ai.AssistantImages, error) {
	if model == nil {
		return nil, errors.New("ai: images model is nil")
	}
	switch model.API {
	case ai.ImagesAPIOpenRouter:
		return GenerateOpenRouterImages(ctx, model, imagesContext, options)
	default:
		return nil, fmt.Errorf("No API provider registered for api: %s", model.API) //nolint:staticcheck // Exact upstream error text is observable.
	}
}

// GenerateOpenRouterImages performs one non-streaming Chat Completions POST
// with image/text modalities. Provider failures are encoded in the returned
// result ("error"/"aborted" plus errorMessage), never as a Go error.
func GenerateOpenRouterImages(
	ctx context.Context,
	model *ai.ImagesModel,
	imagesContext ai.ImagesContext,
	options *ai.ImagesOptions,
) (*ai.AssistantImages, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenRouter images model is nil")
	}
	output := &ai.AssistantImages{
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		Output:     ai.ImagesContent{},
		StopReason: ai.ImagesStopReasonStop,
		Timestamp:  openAINowUnixMilli(),
	}
	if err := generateOpenRouterImages(ctx, model, imagesContext, options, output); err != nil {
		if ctx.Err() != nil {
			output.StopReason = ai.ImagesStopReasonAborted
		} else {
			output.StopReason = ai.ImagesStopReasonError
		}
		message := formatOpenAIError(err, "")
		output.ErrorMessage = &message
	}
	return output, nil
}

func generateOpenRouterImages(
	ctx context.Context,
	model *ai.ImagesModel,
	imagesContext ai.ImagesContext,
	options *ai.ImagesOptions,
	output *ai.AssistantImages,
) error {
	apiKey := ""
	if options != nil && options.APIKey != nil {
		apiKey = *options.APIKey
	}
	if apiKey == "" {
		return fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
	}
	payload := buildOpenRouterImagesPayload(model, imagesContext)
	payloadValue, err := applyImagesPayloadHook(ctx, model, options, payload)
	if err != nil {
		return err
	}
	body, err := ai.Marshal(openRouterImagesWireValue(payloadValue))
	if err != nil {
		return fmt.Errorf("encode OpenRouter images request: %w", err)
	}

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(model.BaseURL),
		option.WithHTTPClient(openAIHTTPClient),
	)
	requestOptions := []option.RequestOption{option.WithMaxRetries(0)}
	if options != nil {
		if options.MaxRetries != nil {
			requestOptions[0] = option.WithMaxRetries(*options.MaxRetries)
		}
		if options.TimeoutMS != nil {
			requestOptions = append(requestOptions, option.WithRequestTimeout(time.Duration(*options.TimeoutMS)*time.Millisecond))
		}
	}
	headers := make(http.Header)
	if model.Headers != nil {
		for name, value := range *model.Headers {
			headers.Set(name, value)
		}
	}
	if options != nil {
		// Nil-valued option headers suppress the model header of the same name
		// without disturbing SDK defaults, as upstream providerHeadersToRecord
		// drops nulls after the model/options spread merge.
		mergeProviderHeaders(headers, options.Headers)
	}
	for name, values := range headers {
		requestOptions = append(requestOptions, option.WithHeader(name, values[len(values)-1]))
	}

	var response *http.Response
	if err := client.Post(ctx, "chat/completions", json.RawMessage(body), &response, requestOptions...); err != nil {
		return normalizeOpenAIRequestError(response, err)
	}
	if response == nil {
		return errors.New("OpenAI API returned no HTTP response")
	}
	defer func() { _ = response.Body.Close() }()
	if options != nil && options.OnResponse != nil {
		if err := options.OnResponse(ctx, providerResponse(response), model); err != nil {
			return err
		}
	}
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(contents, &decoded); err != nil {
		return err
	}

	if id, ok := rawJSONString(decoded["id"]); ok {
		output.ResponseID = &id
	}
	if rawJSTruthy(decoded["usage"]) {
		usage := parseOpenRouterImagesUsage(decoded["usage"], model)
		output.Usage = &usage
	}

	choices := rawJSONArray(decoded["choices"])
	if len(choices) == 0 {
		return nil
	}
	var choice struct {
		Message map[string]json.RawMessage `json:"message"`
	}
	_ = json.Unmarshal(choices[0], &choice)
	if content, ok := rawJSONString(choice.Message["content"]); ok && content != "" {
		output.Output = append(output.Output, &ai.TextContent{Text: content})
	}
	for _, rawImage := range rawJSONArray(choice.Message["images"]) {
		var image map[string]json.RawMessage
		if json.Unmarshal(rawImage, &image) != nil {
			continue
		}
		imageURL, ok := rawJSONString(image["image_url"])
		if !ok {
			var wrapped map[string]json.RawMessage
			_ = json.Unmarshal(image["image_url"], &wrapped)
			imageURL, _ = rawJSONString(wrapped["url"])
		}
		matches := openRouterImagesDataURL.FindStringSubmatch(imageURL)
		if matches == nil {
			continue
		}
		output.Output = append(output.Output, &ai.ImageContent{MimeType: matches[1], Data: matches[2]})
	}
	return nil
}

// openRouterImagesDataURL mirrors upstream /^data:([^;]+);base64,(.+)$/ where
// the JS dot excludes line terminators.
var openRouterImagesDataURL = regexp.MustCompile(`^data:([^;]+);base64,([^\n\r\x{2028}\x{2029}]+)$`)

func buildOpenRouterImagesPayload(model *ai.ImagesModel, imagesContext ai.ImagesContext) map[string]any {
	content := make([]any, 0, len(imagesContext.Input))
	for _, rawBlock := range imagesContext.Input {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			content = append(content, map[string]any{"type": "text", "text": sanitizeText(block.Text)})
		case *ai.ImageContent:
			content = append(content, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + block.MimeType + ";base64," + block.Data},
			})
		}
	}
	modalities := []any{"image"}
	if slices.Contains(model.Output, ai.InputText) {
		modalities = []any{"image", "text"}
	}
	return map[string]any{
		"model":      model.ID,
		"messages":   []any{map[string]any{"role": "user", "content": content}},
		"stream":     false,
		"modalities": modalities,
	}
}

// openRouterImagesWirePayload preserves the property order produced by
// upstream's params construction. Hooks receive the mutable map; wrapping
// happens only after the hook has returned.
type openRouterImagesWirePayload struct {
	value map[string]any
}

func openRouterImagesWireValue(value any) any {
	if object, ok := value.(map[string]any); ok {
		return openRouterImagesWirePayload{value: object}
	}
	return value
}

func (payload openRouterImagesWirePayload) MarshalJSON() ([]byte, error) {
	return marshalOpenAICompletionsObjectWithKeys(
		payload.value,
		orderedOpenAICompletionsKeys(payload.value, []string{"model", "messages", "stream", "modalities"}),
	)
}

func applyImagesPayloadHook(
	ctx context.Context,
	model *ai.ImagesModel,
	options *ai.ImagesOptions,
	payload any,
) (any, error) {
	if options == nil || options.OnPayload == nil {
		return payload, nil
	}
	replacement, replace, err := options.OnPayload(ctx, payload, model)
	if err != nil {
		return nil, err
	}
	if replace {
		return replacement, nil
	}
	return payload, nil
}

func parseOpenRouterImagesUsage(raw json.RawMessage, model *ai.ImagesModel) ai.Usage {
	var usage map[string]json.RawMessage
	_ = json.Unmarshal(raw, &usage)
	promptTokens, _ := rawJSONInt64(usage["prompt_tokens"])
	completionTokens, _ := rawJSONInt64(usage["completion_tokens"])
	var promptDetails map[string]json.RawMessage
	_ = json.Unmarshal(usage["prompt_tokens_details"], &promptDetails)
	reportedCachedTokens, _ := rawJSONInt64(promptDetails["cached_tokens"])
	cacheWrite, _ := rawJSONInt64(promptDetails["cache_write_tokens"])
	cacheRead := reportedCachedTokens
	if cacheWrite > 0 {
		cacheRead = reportedCachedTokens - cacheWrite
		if cacheRead < 0 {
			cacheRead = 0
		}
	}
	input := promptTokens - cacheRead - cacheWrite
	if input < 0 {
		input = 0
	}
	result := ai.Usage{
		Input:       input,
		Output:      completionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: input + completionTokens + cacheRead + cacheWrite,
	}
	// Images usage prices from the flat model rates; the chat-side tier and
	// long-cache logic does not apply upstream.
	result.Cost.Input = model.Cost.Input / 1_000_000 * float64(input)
	result.Cost.Output = model.Cost.Output / 1_000_000 * float64(completionTokens)
	result.Cost.CacheRead = model.Cost.CacheRead / 1_000_000 * float64(cacheRead)
	result.Cost.CacheWrite = model.Cost.CacheWrite / 1_000_000 * float64(cacheWrite)
	result.Cost.Total = result.Cost.Input + result.Cost.Output + result.Cost.CacheRead + result.Cost.CacheWrite
	return result
}
