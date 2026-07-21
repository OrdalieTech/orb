package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

const defaultAzureOpenAIAPIVersion = "v1"

type azureOpenAIHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var azureOpenAIHTTPClient azureOpenAIHTTPDoer = http.DefaultClient

type AzureOpenAIResponsesOptions struct {
	ai.StreamOptions
	ReasoningEffort     *string `json:"reasoningEffort,omitempty"`
	ReasoningSummary    *string `json:"reasoningSummary,omitempty"`
	AzureAPIVersion     *string `json:"azureApiVersion,omitempty"`
	AzureResourceName   *string `json:"azureResourceName,omitempty"`
	AzureBaseURL        *string `json:"azureBaseUrl,omitempty"`
	AzureDeploymentName *string `json:"azureDeploymentName,omitempty"`
}

func StreamAzureOpenAIResponses(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	options := &AzureOpenAIResponsesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamAzureOpenAIResponsesWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleAzureOpenAIResponses(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if err := assertAzureOpenAIAPIKey(model, &base); err != nil {
		return nil, err
	}
	var requested *ai.ThinkingLevel
	if options != nil {
		requested = options.Reasoning
	}
	level := clampSimpleReasoning(model, requested)
	var effort *string
	if level != nil {
		value := string(*level)
		effort = &value
	}
	return StreamAzureOpenAIResponsesWithOptions(ctx, model, requestContext, &AzureOpenAIResponsesOptions{
		StreamOptions: base, ReasoningEffort: effort,
	})
}

func StreamAzureOpenAIResponsesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *AzureOpenAIResponsesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := azureOpenAIStreamOptions(options)
	deploymentName := resolveAzureOpenAIDeploymentName(model, options)

	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		httpContext := ctx
		cancel := func() {}
		if streamOptions != nil && streamOptions.TimeoutMS != nil {
			httpContext, cancel = context.WithTimeout(ctx, time.Duration(*streamOptions.TimeoutMS)*time.Millisecond)
		}
		defer cancel()
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearResponsesStreamingFields(output)
			sink(streamFailure(ctx, output, err, "Azure OpenAI API error"))
		}

		apiKey, err := azureOpenAIAPIKey(model, streamOptions)
		if err != nil {
			fail(err)
			return
		}
		config, err := resolveAzureOpenAIConfig(model, options)
		if err != nil {
			fail(err)
			return
		}
		payload, err := buildAzureOpenAIResponsesPayload(model, requestContext, options, deploymentName)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		response, err := postAzureOpenAIStream(httpContext, model, streamOptions, config, apiKey, hookedPayload)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if streamOptions != nil && streamOptions.OnResponse != nil {
			if err := streamOptions.OnResponse(ctx, providerResponse(response), model); err != nil {
				fail(err)
				return
			}
		}
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processor := newOpenAIResponsesProcessor(model, output, nil, sink)
		err = readSSE(response.Body, processor.handle)
		if errors.Is(err, errStopSSE) {
			return
		}
		if err == nil && !processor.sawTerminalResponseEvent {
			err = errors.New("OpenAI Responses stream ended before a terminal response event")
		}
		if err == nil && ctx.Err() != nil {
			err = errors.New("Request was aborted") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err == nil && (output.StopReason == ai.StopReasonAborted || output.StopReason == ai.StopReasonError) {
			err = errors.New("An unknown error occurred") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err != nil {
			fail(err)
			return
		}
		clearResponsesStreamingFields(output)
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func azureOpenAIStreamOptions(options *AzureOpenAIResponsesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func assertAzureOpenAIAPIKey(model *ai.Model, options *ai.StreamOptions) error {
	_, err := azureOpenAIAPIKey(model, options)
	return err
}

func azureOpenAIAPIKey(model *ai.Model, options *ai.StreamOptions) (string, error) {
	if options != nil && options.APIKey != nil && *options.APIKey != "" {
		return *options.APIKey, nil
	}
	return "", fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
}

type azureOpenAIConfig struct {
	baseURL    string
	apiVersion string
}

func resolveAzureOpenAIConfig(model *ai.Model, options *AzureOpenAIResponsesOptions) (azureOpenAIConfig, error) {
	streamOptions := azureOpenAIStreamOptions(options)
	apiVersion := ""
	if options != nil && options.AzureAPIVersion != nil {
		apiVersion = *options.AzureAPIVersion
	}
	if apiVersion == "" {
		apiVersion = providerEnvValue("AZURE_OPENAI_API_VERSION", streamOptions)
	}
	if apiVersion == "" {
		apiVersion = defaultAzureOpenAIAPIVersion
	}

	baseURL := ""
	resourceName := ""
	if options != nil && options.AzureBaseURL != nil {
		baseURL = strings.TrimSpace(*options.AzureBaseURL)
	}
	if options != nil && options.AzureResourceName != nil {
		resourceName = *options.AzureResourceName
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(providerEnvValue("AZURE_OPENAI_BASE_URL", streamOptions))
	}
	if resourceName == "" {
		resourceName = providerEnvValue("AZURE_OPENAI_RESOURCE_NAME", streamOptions)
	}
	if baseURL == "" && resourceName != "" {
		baseURL = buildAzureOpenAIDefaultBaseURL(resourceName)
	}
	if baseURL == "" {
		baseURL = model.BaseURL
	}
	if baseURL == "" {
		return azureOpenAIConfig{}, errors.New("Azure OpenAI base URL is required. Set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME, or pass azureBaseUrl, azureResourceName, or model.baseUrl.") //nolint:staticcheck // Exact upstream error text is observable.
	}
	normalized, err := normalizeAzureOpenAIBaseURL(baseURL)
	if err != nil {
		return azureOpenAIConfig{}, err
	}
	return azureOpenAIConfig{baseURL: normalized, apiVersion: apiVersion}, nil
}

func normalizeAzureOpenAIBaseURL(baseURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("Invalid Azure OpenAI base URL: %s", baseURL) //nolint:staticcheck // Exact upstream error text is observable.
	}
	host := strings.ToLower(parsed.Hostname())
	isAzureHost := strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".cognitiveservices.azure.com") ||
		strings.HasSuffix(host, ".ai.azure.com")
	normalizedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if isAzureHost && (normalizedPath == "" || normalizedPath == "/" || normalizedPath == "/openai" || normalizedPath == "/openai/v1/responses") {
		parsed.Path = "/openai/v1"
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.ForceQuery = false
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func buildAzureOpenAIDefaultBaseURL(resourceName string) string {
	return "https://" + resourceName + ".openai.azure.com/openai/v1"
}

func parseAzureOpenAIDeploymentNameMap(value string) map[string]string {
	result := make(map[string]string)
	for _, entry := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return result
}

func resolveAzureOpenAIDeploymentName(model *ai.Model, options *AzureOpenAIResponsesOptions) string {
	if options != nil && options.AzureDeploymentName != nil && *options.AzureDeploymentName != "" {
		return *options.AzureDeploymentName
	}
	mapping := parseAzureOpenAIDeploymentNameMap(providerEnvValue("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", azureOpenAIStreamOptions(options)))
	if deployment := mapping[model.ID]; deployment != "" {
		return deployment
	}
	return model.ID
}

func resolveAzureDeploymentName(model *ai.Model, options *AzureOpenAIResponsesOptions) string {
	return resolveAzureOpenAIDeploymentName(model, options)
}

func buildAzureOpenAIResponsesPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *AzureOpenAIResponsesOptions,
	deploymentName string,
) (*OpenAIResponsesPayload, error) {
	compat, err := getOpenAIResponsesCompat(model)
	if err != nil {
		return nil, err
	}
	input, err := convertResponsesMessages(model, requestContext, map[string]ai.Tool{}, compat.supportsDeveloperRole)
	if err != nil {
		return nil, err
	}
	payload := &OpenAIResponsesPayload{Model: deploymentName, Input: input, Stream: true, Store: false}
	streamOptions := azureOpenAIStreamOptions(options)
	if streamOptions != nil {
		if streamOptions.SessionID != nil {
			if value, ok := clampOpenAIPromptCacheKey(streamOptions.SessionID).(string); ok {
				payload.PromptCacheKey = &value
			}
		}
		if streamOptions.MaxTokens != nil && *streamOptions.MaxTokens != 0 {
			value := max(*streamOptions.MaxTokens, openAIResponsesMinOutputTokens)
			payload.MaxOutputTokens = &value
		}
		payload.Temperature = streamOptions.Temperature
	}
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 {
		payload.Tools = convertResponsesTools(*requestContext.Tools, false)
	}
	applyAzureOpenAIReasoning(payload, model, options)
	return payload, nil
}

func applyAzureOpenAIReasoning(payload *OpenAIResponsesPayload, model *ai.Model, options *AzureOpenAIResponsesOptions) {
	if !model.Reasoning {
		return
	}
	effortSet := options != nil && options.ReasoningEffort != nil && *options.ReasoningEffort != ""
	summarySet := options != nil && options.ReasoningSummary != nil && *options.ReasoningSummary != ""
	if effortSet || summarySet {
		effort := "medium"
		if effortSet {
			effort = mappedThinkingLevel(model, *options.ReasoningEffort, *options.ReasoningEffort)
		}
		summary := "auto"
		if summarySet {
			summary = *options.ReasoningSummary
		}
		payload.Reasoning = &OpenAIReasoningParams{Effort: effort, Summary: &summary}
		payload.Include = []string{"reasoning.encrypted_content"}
	} else if supportsOffReasoning(model) {
		payload.Reasoning = &OpenAIReasoningParams{Effort: mappedThinkingLevel(model, "off", "none")}
	}
}

func postAzureOpenAIStream(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	config azureOpenAIConfig,
	apiKey string,
	payload any,
) (*http.Response, error) {
	body, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Azure OpenAI request: %w", err)
	}
	endpoint, err := url.Parse(strings.TrimRight(config.baseURL, "/") + "/responses")
	if err != nil {
		return nil, err
	}
	// The pinned TypeScript SDK replaces the base URL's query when it applies
	// api-version. If the configured proxy URL already has a query, /responses
	// was parsed into that query and is discarded with it.
	endpoint.RawQuery = url.Values{"api-version": []string{config.apiVersion}}.Encode()
	maxRetries := 0
	if options != nil && options.MaxRetries != nil {
		maxRetries = max(0, *options.MaxRetries)
	}
	headers := copyModelHeaders(model)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("api-key", apiKey)
	if options != nil {
		mergeProviderHeaders(headers, options.Headers)
	}
	headers, err = applyHeadersHook(ctx, model, options, headers)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for name, values := range headers {
			request.Header[name] = append([]string(nil), values...)
		}
		response, requestErr := azureOpenAIHTTPClient.Do(request)
		if attempt < maxRetries && shouldRetryAzureOpenAI(response, requestErr) {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if err := waitAzureOpenAIRetry(ctx, response, attempt); err != nil {
				return response, err
			}
			continue
		}
		if requestErr != nil {
			return response, requestErr
		}
		if response == nil {
			return nil, errors.New("ai/api: Azure OpenAI API returned no HTTP response")
		}
		if response.StatusCode >= http.StatusBadRequest {
			contents, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				return response, readErr
			}
			return response, newOpenAIStatusError(response.StatusCode, contents)
		}
		return response, nil
	}
}

func shouldRetryAzureOpenAI(response *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if value := response.Header.Get("x-should-retry"); value == "true" {
		return true
	} else if value == "false" {
		return false
	}
	return response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusConflict ||
		response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
}

func waitAzureOpenAIRetry(ctx context.Context, response *http.Response, attempt int) error {
	delay := azureOpenAIRetryAfter(response)
	if delay < 0 {
		delay = 500 * time.Millisecond * time.Duration(1<<min(attempt, 4))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func azureOpenAIRetryAfter(response *http.Response) time.Duration {
	if response == nil {
		return -1
	}
	if value := response.Header.Get("retry-after-ms"); value != "" {
		if milliseconds, err := strconv.ParseFloat(value, 64); err == nil && milliseconds >= 0 && milliseconds <= 60_000 {
			return time.Duration(milliseconds * float64(time.Millisecond))
		}
	}
	if value := response.Header.Get("retry-after"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 && seconds <= 60 {
			return time.Duration(seconds * float64(time.Second))
		}
		if retryAt, err := http.ParseTime(value); err == nil {
			delay := time.Until(retryAt)
			if delay >= 0 && delay <= time.Minute {
				return delay
			}
		}
	}
	return -1
}
