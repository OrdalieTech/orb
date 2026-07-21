package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

const googleVertexCredentialMarker = "gcp-vertex-credentials"

var googleVertexAPIVersionPattern = regexp.MustCompile(`^v\d+(?:beta\d*)?$`)

type GoogleVertexOptions struct {
	ai.StreamOptions
	ToolChoice GoogleToolChoice       `json:"toolChoice,omitempty"`
	Thinking   *GoogleThinkingOptions `json:"thinking,omitempty"`
	Project    string                 `json:"project,omitempty"`
	Location   string                 `json:"location,omitempty"`
}

type googleVertexRequestAuth struct {
	apiKey   string
	project  string
	location string
	adc      bool
}

func StreamGoogleVertex(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Google Vertex model is nil")
	}
	options := &GoogleVertexOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamGoogleVertexWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleGoogleVertex(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Google Vertex model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if options == nil || options.Reasoning == nil {
		return StreamGoogleVertexWithOptions(ctx, model, requestContext, &GoogleVertexOptions{
			StreamOptions: base, Thinking: &GoogleThinkingOptions{Enabled: false},
		})
	}
	effort := clampGoogleReasoning(model, *options.Reasoning)
	if effort == ai.ThinkingLevel(ai.ModelThinkingOff) {
		effort = ai.ThinkingHigh
	}
	thinking := &GoogleThinkingOptions{Enabled: true}
	if isGemini3Pro(model) || isGemini3Flash(model) {
		thinking.Level = googleThinkingLevel(effort, model)
	} else {
		thinking.BudgetTokens = googleVertexThinkingBudget(model, effort, options.ThinkingBudgets)
	}
	return StreamGoogleVertexWithOptions(ctx, model, requestContext, &GoogleVertexOptions{
		StreamOptions: base, Thinking: thinking,
	})
}

func StreamGoogleVertexWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *GoogleVertexOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Google Vertex model is nil")
	}
	googleOptions := &GoogleOptions{}
	if options != nil {
		googleOptions.StreamOptions = options.StreamOptions
		googleOptions.ToolChoice = options.ToolChoice
		googleOptions.Thinking = options.Thinking
	}
	return streamGoogleWithOptions(ctx, model, requestContext, googleOptions, nil, func(
		ctx context.Context,
		model *ai.Model,
		streamOptions *ai.StreamOptions,
		parameters googleDecodedParameters,
	) (*http.Response, error) {
		return postGoogleVertexStream(ctx, model, streamOptions, options, parameters)
	})
}

func postGoogleVertexStream(
	ctx context.Context,
	model *ai.Model,
	streamOptions *ai.StreamOptions,
	options *GoogleVertexOptions,
	parameters googleDecodedParameters,
) (*http.Response, error) {
	auth, err := resolveGoogleVertexRequestAuth(options)
	if err != nil {
		return nil, err
	}
	project, location := auth.project, auth.location
	if !auth.adc {
		project, location = "undefined", "undefined"
	}
	wire, err := googleVertexWirePayload(parameters, project, location)
	if err != nil {
		return nil, err
	}
	payload, err := ai.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode Google Vertex request: %w", err)
	}
	endpoint, err := googleVertexEndpoint(model.BaseURL, parameters.Model, auth)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if !auth.adc && strings.HasPrefix(auth.apiKey, "auth_tokens/") {
		return nil, errors.New("Ephemeral tokens are only supported by the live API.") //nolint:staticcheck // Exact SDK text.
	}
	headers := googleProviderHeaders(model, streamOptions)
	if auth.adc {
		resolved, err := googleVertexAuthHeaders(ctx, streamOptions)
		if err != nil {
			return nil, err
		}
		if !googleHeaderPresent(headers, "Authorization") {
			headers.Set("Authorization", "Bearer "+resolved.accessToken)
		}
		if resolved.quotaProject != "" && !googleHeaderPresent(headers, "X-Goog-User-Project") {
			headers.Set("X-Goog-User-Project", resolved.quotaProject)
		}
	} else if !googleHeaderPresent(headers, "X-Goog-Api-Key") {
		headers.Set("X-Goog-Api-Key", auth.apiKey)
	}
	headers, err = applyHeadersHook(ctx, model, streamOptions, headers)
	if err != nil {
		return nil, err
	}
	request.Header = headers
	return doGoogleRequest(request)
}

func resolveGoogleVertexRequestAuth(options *GoogleVertexOptions) (googleVertexRequestAuth, error) {
	var apiKey string
	if options != nil && options.APIKey != nil {
		apiKey = strings.TrimSpace(*options.APIKey)
	}
	if apiKey != "" && apiKey != googleVertexCredentialMarker && !googleVertexPlaceholderKey(apiKey) {
		return googleVertexRequestAuth{apiKey: apiKey}, nil
	}
	project := ""
	location := ""
	if options != nil {
		project = options.Project
		location = options.Location
		if project == "" {
			project = providerEnvValue("GOOGLE_CLOUD_PROJECT", &options.StreamOptions)
		}
		if project == "" {
			project = providerEnvValue("GCLOUD_PROJECT", &options.StreamOptions)
		}
		if location == "" {
			location = providerEnvValue("GOOGLE_CLOUD_LOCATION", &options.StreamOptions)
		}
	} else {
		project = providerEnvValue("GOOGLE_CLOUD_PROJECT", nil)
		if project == "" {
			project = providerEnvValue("GCLOUD_PROJECT", nil)
		}
		location = providerEnvValue("GOOGLE_CLOUD_LOCATION", nil)
	}
	if project == "" {
		return googleVertexRequestAuth{}, errors.New("Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or pass project in options.") //nolint:staticcheck // Exact upstream text.
	}
	if location == "" {
		return googleVertexRequestAuth{}, errors.New("Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION or pass location in options.") //nolint:staticcheck // Exact upstream text.
	}
	return googleVertexRequestAuth{project: project, location: location, adc: true}, nil
}

func googleVertexPlaceholderKey(value string) bool {
	return len(value) >= 3 && value[0] == '<' && value[len(value)-1] == '>' && !strings.Contains(value[1:len(value)-1], ">")
}

func googleVertexEndpoint(baseURL, model string, auth googleVertexRequestAuth) (string, error) {
	modelPath, err := googleVertexModelPath(model)
	if err != nil {
		return "", err
	}
	customBase := googleVertexCustomBaseURL(baseURL)
	base := customBase
	version := "v1"
	if base == "" {
		switch {
		case !auth.adc || auth.location == "global":
			base = "https://aiplatform.googleapis.com/"
		case auth.location == "us" || auth.location == "eu":
			base = "https://aiplatform." + auth.location + ".rep.googleapis.com/"
		default:
			base = "https://" + auth.location + "-aiplatform.googleapis.com/"
		}
	} else if googleVertexBaseHasAPIVersion(base) {
		version = ""
	}
	segments := []string{strings.TrimRight(base, "/")}
	if version != "" {
		segments = append(segments, version)
	}
	if auth.adc && customBase == "" && !strings.HasPrefix(modelPath, "projects/") {
		segments = append(segments, "projects/"+auth.project+"/locations/"+auth.location)
	}
	segments = append(segments, modelPath+":streamGenerateContent?alt=sse")
	return strings.Join(segments, "/"), nil
}

func googleVertexCustomBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" || strings.Contains(trimmed, "{location}") {
		return ""
	}
	return trimmed
}

func googleVertexBaseHasAPIVersion(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	path := baseURL
	if err == nil {
		path = parsed.Path
	}
	for _, part := range strings.Split(path, "/") {
		if googleVertexAPIVersionPattern.MatchString(part) {
			return true
		}
	}
	return false
}

func googleVertexModelPath(model string) (string, error) {
	if model == "" {
		return "", errors.New("model is required and must be a string")
	}
	if strings.Contains(model, "..") || strings.ContainsAny(model, "?&") {
		return "", errors.New("invalid model parameter")
	}
	switch {
	case strings.HasPrefix(model, "publishers/"), strings.HasPrefix(model, "projects/"), strings.HasPrefix(model, "models/"):
		return model, nil
	case strings.Contains(model, "/"):
		parts := strings.SplitN(model, "/", 3)
		return "publishers/" + parts[0] + "/models/" + parts[1], nil
	default:
		return "publishers/google/models/" + model, nil
	}
}

func disabledGoogleVertexThinkingConfig(model *ai.Model) *GoogleThinkingConfig {
	if isGemini3Pro(model) {
		level := GoogleThinkingLow
		return &GoogleThinkingConfig{ThinkingLevel: &level}
	}
	if isGemini3Flash(model) {
		level := GoogleThinkingMinimal
		return &GoogleThinkingConfig{ThinkingLevel: &level}
	}
	zero := int64(0)
	return &GoogleThinkingConfig{ThinkingBudget: &zero}
}

// googleVertexThinkingBudget mirrors google-vertex.ts getGoogleBudget; efforts
// without an explicit table entry resolve to undefined, omitting the
// thinkingConfig budget instead of sending 0. (OT-m9)
func googleVertexThinkingBudget(model *ai.Model, effort ai.ThinkingLevel, custom *ai.ThinkingBudgets) *int64 {
	if custom != nil {
		var value *int
		switch effort {
		case ai.ThinkingMinimal:
			value = custom.Minimal
		case ai.ThinkingLow:
			value = custom.Low
		case ai.ThinkingMedium:
			value = custom.Medium
		case ai.ThinkingHigh:
			value = custom.High
		}
		if value != nil {
			budget := int64(*value)
			return &budget
		}
	}
	var budgets map[ai.ThinkingLevel]int64
	switch {
	case strings.Contains(model.ID, "2.5-pro"):
		budgets = map[ai.ThinkingLevel]int64{ai.ThinkingMinimal: 128, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192, ai.ThinkingHigh: 32768}
	case strings.Contains(model.ID, "2.5-flash"):
		budgets = map[ai.ThinkingLevel]int64{ai.ThinkingMinimal: 128, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192, ai.ThinkingHigh: 24576}
	default:
		fallback := int64(-1)
		return &fallback
	}
	if budget, ok := budgets[effort]; ok {
		return &budget
	}
	return nil
}
