package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

var (
	googleMajorVersionPattern    = regexp.MustCompile(`^gemini(?:-live)?-(\d+)`)
	googleBase64SignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

type GoogleContent struct {
	Parts []GooglePart `json:"parts"`
	Role  string       `json:"role"`
}

type GooglePart struct {
	Text             *string                 `json:"text,omitempty"`
	InlineData       *GoogleInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature *string                 `json:"thoughtSignature,omitempty"`
}

type GoogleInlineData struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type GoogleFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Args json.RawMessage `json:"args"`
	Name string          `json:"name"`
}

type GoogleFunctionResponse struct {
	Name     string                       `json:"name"`
	Response GoogleFunctionResponseValue  `json:"response"`
	Parts    []GoogleFunctionResponsePart `json:"parts,omitempty"`
	ID       string                       `json:"id,omitempty"`
}

type GoogleFunctionResponsePart struct {
	InlineData *GoogleFunctionResponseInlineData `json:"inlineData,omitempty"`
}

type GoogleFunctionResponseInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type GoogleFunctionResponseValue struct {
	Output *string `json:"output,omitempty"`
	Error  *string `json:"error,omitempty"`
}

type GoogleFunctionDeclaration struct {
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	ParametersJSONSchema jsonschema.Schema `json:"parametersJsonSchema"`
}

type GoogleTool struct {
	ComputerUse           json.RawMessage             `json:"computerUse,omitempty"`
	FileSearch            json.RawMessage             `json:"fileSearch,omitempty"`
	GoogleSearch          json.RawMessage             `json:"googleSearch,omitempty"`
	GoogleMaps            json.RawMessage             `json:"googleMaps,omitempty"`
	CodeExecution         json.RawMessage             `json:"codeExecution,omitempty"`
	FunctionDeclarations  []GoogleFunctionDeclaration `json:"functionDeclarations,omitempty"`
	GoogleSearchRetrieval json.RawMessage             `json:"googleSearchRetrieval,omitempty"`
	URLContext            json.RawMessage             `json:"urlContext,omitempty"`
	MCPServers            json.RawMessage             `json:"mcpServers,omitempty"`
}

type GoogleFunctionCallingConfig struct {
	AllowedFunctionNames []string        `json:"allowedFunctionNames,omitempty"`
	Mode                 string          `json:"mode,omitempty"`
	StreamArguments      json.RawMessage `json:"streamFunctionCallArguments,omitempty"`
}

type GoogleToolConfig struct {
	RetrievalConfig                  json.RawMessage              `json:"retrievalConfig,omitempty"`
	FunctionCallingConfig            *GoogleFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	IncludeServerSideToolInvocations *bool                        `json:"includeServerSideToolInvocations,omitempty"`
}

type GoogleThinkingConfig struct {
	IncludeThoughts *bool                `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int64               `json:"thinkingBudget,omitempty"`
	ThinkingLevel   *GoogleThinkingLevel `json:"thinkingLevel,omitempty"`
}

type GoogleSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GoogleGenerateContentConfig struct {
	Temperature                *float64              `json:"temperature,omitempty"`
	TopP                       *float64              `json:"topP,omitempty"`
	TopK                       *float64              `json:"topK,omitempty"`
	CandidateCount             *int64                `json:"candidateCount,omitempty"`
	MaxOutputTokens            *float64              `json:"maxOutputTokens,omitempty"`
	StopSequences              []string              `json:"stopSequences,omitempty"`
	ResponseLogprobs           *bool                 `json:"responseLogprobs,omitempty"`
	Logprobs                   *int64                `json:"logprobs,omitempty"`
	PresencePenalty            *float64              `json:"presencePenalty,omitempty"`
	FrequencyPenalty           *float64              `json:"frequencyPenalty,omitempty"`
	Seed                       *int64                `json:"seed,omitempty"`
	ResponseMimeType           *string               `json:"responseMimeType,omitempty"`
	ResponseSchema             json.RawMessage       `json:"responseSchema,omitempty"`
	ResponseJSONSchema         json.RawMessage       `json:"responseJsonSchema,omitempty"`
	SafetySettings             []GoogleSafetySetting `json:"safetySettings,omitempty"`
	SystemInstruction          json.RawMessage       `json:"systemInstruction,omitempty"`
	Tools                      []GoogleTool          `json:"tools,omitempty"`
	ToolConfig                 *GoogleToolConfig     `json:"toolConfig,omitempty"`
	CachedContent              *string               `json:"cachedContent,omitempty"`
	ResponseModalities         []string              `json:"responseModalities,omitempty"`
	MediaResolution            *string               `json:"mediaResolution,omitempty"`
	SpeechConfig               json.RawMessage       `json:"speechConfig,omitempty"`
	ThinkingConfig             *GoogleThinkingConfig `json:"thinkingConfig,omitempty"`
	ImageConfig                json.RawMessage       `json:"imageConfig,omitempty"`
	EnableEnhancedCivicAnswers *bool                 `json:"enableEnhancedCivicAnswers,omitempty"`
	ServiceTier                *string               `json:"serviceTier,omitempty"`
}

type GoogleGenerateContentParameters struct {
	Model    string                      `json:"model"`
	Contents []GoogleContent             `json:"contents"`
	Config   GoogleGenerateContentConfig `json:"config"`
}

type googleDecodedParameters struct {
	Model    string          `json:"model"`
	Contents json.RawMessage `json:"contents"`
	Config   json.RawMessage `json:"config"`
}

type googleRawGenerateContentConfig struct {
	SystemInstruction          json.RawMessage `json:"systemInstruction"`
	Temperature                json.RawMessage `json:"temperature"`
	TopP                       json.RawMessage `json:"topP"`
	TopK                       json.RawMessage `json:"topK"`
	CandidateCount             json.RawMessage `json:"candidateCount"`
	MaxOutputTokens            json.RawMessage `json:"maxOutputTokens"`
	StopSequences              json.RawMessage `json:"stopSequences"`
	ResponseLogprobs           json.RawMessage `json:"responseLogprobs"`
	Logprobs                   json.RawMessage `json:"logprobs"`
	PresencePenalty            json.RawMessage `json:"presencePenalty"`
	FrequencyPenalty           json.RawMessage `json:"frequencyPenalty"`
	Seed                       json.RawMessage `json:"seed"`
	ResponseMimeType           json.RawMessage `json:"responseMimeType"`
	ResponseSchema             json.RawMessage `json:"responseSchema"`
	ResponseJSONSchema         json.RawMessage `json:"responseJsonSchema"`
	RoutingConfig              json.RawMessage `json:"routingConfig"`
	ModelSelectionConfig       json.RawMessage `json:"modelSelectionConfig"`
	SafetySettings             json.RawMessage `json:"safetySettings"`
	Tools                      json.RawMessage `json:"tools"`
	ToolConfig                 json.RawMessage `json:"toolConfig"`
	Labels                     json.RawMessage `json:"labels"`
	CachedContent              json.RawMessage `json:"cachedContent"`
	ResponseModalities         json.RawMessage `json:"responseModalities"`
	MediaResolution            json.RawMessage `json:"mediaResolution"`
	SpeechConfig               json.RawMessage `json:"speechConfig"`
	AudioTimestamp             json.RawMessage `json:"audioTimestamp"`
	ThinkingConfig             json.RawMessage `json:"thinkingConfig"`
	ImageConfig                json.RawMessage `json:"imageConfig"`
	EnableEnhancedCivicAnswers json.RawMessage `json:"enableEnhancedCivicAnswers"`
	ModelArmorConfig           json.RawMessage `json:"modelArmorConfig"`
	ServiceTier                json.RawMessage `json:"serviceTier"`
}

type googleWireRequest struct {
	Contents          json.RawMessage             `json:"contents,omitempty"`
	SystemInstruction json.RawMessage             `json:"systemInstruction,omitempty"`
	SafetySettings    json.RawMessage             `json:"safetySettings,omitempty"`
	Tools             json.RawMessage             `json:"tools,omitempty"`
	ToolConfig        json.RawMessage             `json:"toolConfig,omitempty"`
	CachedContent     json.RawMessage             `json:"cachedContent,omitempty"`
	ServiceTier       json.RawMessage             `json:"serviceTier,omitempty"`
	GenerationConfig  *googleWireGenerationConfig `json:"generationConfig,omitempty"`
}

type googleWireGenerationConfig struct {
	Temperature                json.RawMessage `json:"temperature,omitempty"`
	TopP                       json.RawMessage `json:"topP,omitempty"`
	TopK                       json.RawMessage `json:"topK,omitempty"`
	CandidateCount             json.RawMessage `json:"candidateCount,omitempty"`
	MaxOutputTokens            json.RawMessage `json:"maxOutputTokens,omitempty"`
	StopSequences              json.RawMessage `json:"stopSequences,omitempty"`
	ResponseLogprobs           json.RawMessage `json:"responseLogprobs,omitempty"`
	Logprobs                   json.RawMessage `json:"logprobs,omitempty"`
	PresencePenalty            json.RawMessage `json:"presencePenalty,omitempty"`
	FrequencyPenalty           json.RawMessage `json:"frequencyPenalty,omitempty"`
	Seed                       json.RawMessage `json:"seed,omitempty"`
	ResponseMimeType           json.RawMessage `json:"responseMimeType,omitempty"`
	ResponseSchema             json.RawMessage `json:"responseSchema,omitempty"`
	ResponseJSONSchema         json.RawMessage `json:"responseJsonSchema,omitempty"`
	ResponseModalities         json.RawMessage `json:"responseModalities,omitempty"`
	MediaResolution            json.RawMessage `json:"mediaResolution,omitempty"`
	SpeechConfig               json.RawMessage `json:"speechConfig,omitempty"`
	ThinkingConfig             json.RawMessage `json:"thinkingConfig,omitempty"`
	ImageConfig                json.RawMessage `json:"imageConfig,omitempty"`
	EnableEnhancedCivicAnswers json.RawMessage `json:"enableEnhancedCivicAnswers,omitempty"`
}

func buildGoogleParameters(model *ai.Model, requestContext ai.Context, options *GoogleOptions) (GoogleGenerateContentParameters, error) {
	contents, err := convertGoogleMessages(model, requestContext)
	if err != nil {
		return GoogleGenerateContentParameters{}, err
	}
	config := GoogleGenerateContentConfig{}
	if options != nil {
		config.Temperature = options.Temperature
		config.MaxOutputTokens = options.MaxTokens
	}
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		value := sanitizeText(*requestContext.SystemPrompt)
		encoded, err := ai.Marshal(value)
		if err != nil {
			return GoogleGenerateContentParameters{}, err
		}
		config.SystemInstruction = encoded
	}
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 {
		config.Tools = convertGoogleTools(*requestContext.Tools)
		if options != nil && options.ToolChoice != "" {
			config.ToolConfig = &GoogleToolConfig{FunctionCallingConfig: &GoogleFunctionCallingConfig{Mode: mapGoogleToolChoice(options.ToolChoice)}}
		}
	}
	if model.Reasoning && options != nil && options.Thinking != nil {
		if options.Thinking.Enabled {
			include := true
			config.ThinkingConfig = &GoogleThinkingConfig{IncludeThoughts: &include}
			if options.Thinking.Level != nil {
				config.ThinkingConfig.ThinkingLevel = options.Thinking.Level
			} else {
				config.ThinkingConfig.ThinkingBudget = options.Thinking.BudgetTokens
			}
		} else {
			config.ThinkingConfig = disabledGoogleThinkingConfig(model)
		}
	}
	return GoogleGenerateContentParameters{Model: model.ID, Contents: contents, Config: config}, nil
}

func googleWirePayload(parameters googleDecodedParameters) (googleWireRequest, error) {
	contents, err := googleWireContents(parameters.Contents)
	if err != nil {
		return googleWireRequest{}, err
	}
	wire := googleWireRequest{Contents: contents}
	if !googleRawPresent(parameters.Config) {
		return wire, nil
	}
	var config googleRawGenerateContentConfig
	if err := json.Unmarshal(parameters.Config, &config); err != nil {
		return googleWireRequest{}, err
	}
	systemInstruction, err := googleSystemInstruction(config.SystemInstruction)
	if err != nil {
		return googleWireRequest{}, err
	}
	responseSchema := config.ResponseSchema
	responseJSONSchema := config.ResponseJSONSchema
	if googleSchemaHasDollar(responseSchema) && !googleJSTruthy(responseJSONSchema) {
		responseJSONSchema, responseSchema = responseSchema, nil
	}
	responseSchema, err = normalizeGoogleResponseSchema(responseSchema)
	if err != nil {
		return googleWireRequest{}, err
	}
	if len(config.RoutingConfig) > 0 {
		return googleWireRequest{}, errors.New("routingConfig parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	if len(config.ModelSelectionConfig) > 0 {
		return googleWireRequest{}, errors.New("modelSelectionConfig parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	safetySettings, err := googleSafetySettings(config.SafetySettings)
	if err != nil {
		return googleWireRequest{}, err
	}
	tools, err := googleWireTools(config.Tools)
	if err != nil {
		return googleWireRequest{}, err
	}
	toolConfig, err := googleWireToolConfig(config.ToolConfig)
	if err != nil {
		return googleWireRequest{}, err
	}
	if len(config.Labels) > 0 {
		return googleWireRequest{}, errors.New("labels parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	cachedContent, err := googleCachedContentName(config.CachedContent)
	if err != nil {
		return googleWireRequest{}, err
	}
	speechConfig, err := googleSpeechConfig(config.SpeechConfig)
	if err != nil {
		return googleWireRequest{}, err
	}
	if len(config.AudioTimestamp) > 0 {
		return googleWireRequest{}, errors.New("audioTimestamp parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	imageConfig, err := googleImageConfig(config.ImageConfig)
	if err != nil {
		return googleWireRequest{}, err
	}
	if len(config.ModelArmorConfig) > 0 {
		return googleWireRequest{}, errors.New("modelArmorConfig parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	wire.SystemInstruction = systemInstruction
	wire.SafetySettings = safetySettings
	wire.Tools = tools
	wire.ToolConfig = toolConfig
	wire.CachedContent = cachedContent
	wire.ServiceTier = googleNonNullRaw(config.ServiceTier)
	wire.GenerationConfig = &googleWireGenerationConfig{
		Temperature: googleNonNullRaw(config.Temperature), TopP: googleNonNullRaw(config.TopP),
		TopK: googleNonNullRaw(config.TopK), CandidateCount: googleNonNullRaw(config.CandidateCount),
		MaxOutputTokens: googleNonNullRaw(config.MaxOutputTokens), StopSequences: googleNonNullRaw(config.StopSequences),
		ResponseLogprobs: googleNonNullRaw(config.ResponseLogprobs), Logprobs: googleNonNullRaw(config.Logprobs),
		PresencePenalty: googleNonNullRaw(config.PresencePenalty), FrequencyPenalty: googleNonNullRaw(config.FrequencyPenalty),
		Seed: googleNonNullRaw(config.Seed), ResponseMimeType: googleNonNullRaw(config.ResponseMimeType),
		ResponseSchema: responseSchema, ResponseJSONSchema: googleNonNullRaw(responseJSONSchema),
		ResponseModalities: googleNonNullRaw(config.ResponseModalities), MediaResolution: googleNonNullRaw(config.MediaResolution),
		SpeechConfig: speechConfig, ThinkingConfig: googleNonNullRaw(config.ThinkingConfig),
		ImageConfig: imageConfig, EnableEnhancedCivicAnswers: googleNonNullRaw(config.EnableEnhancedCivicAnswers),
	}
	return wire, nil
}

func googleSystemInstruction(value json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	type content struct {
		Parts []json.RawMessage `json:"parts"`
		Role  *string           `json:"role,omitempty"`
	}
	if trimmed[0] == '{' {
		if googleRawIsContent(value) {
			return googleWireContent(value)
		}
	}
	parts, err := googleSystemParts(value)
	if err != nil {
		return nil, err
	}
	role := "user"
	return ai.Marshal(content{Parts: parts, Role: &role})
}

func googleSystemParts(value json.RawMessage) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil, errors.New("PartListUnion is required") //nolint:staticcheck // Exact pinned SDK text.
	}
	parts := []json.RawMessage{value}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(value, &parts); err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			return nil, errors.New("PartListUnion is required") //nolint:staticcheck // Exact pinned SDK text.
		}
	}
	return googleWireParts(parts)
}

func googleJSONType(value string) string {
	switch value[0] {
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}

func googleImageConfig(value json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var input struct {
		AspectRatio              json.RawMessage `json:"aspectRatio"`
		ImageSize                json.RawMessage `json:"imageSize"`
		PersonGeneration         json.RawMessage `json:"personGeneration"`
		ProminentPeople          json.RawMessage `json:"prominentPeople"`
		OutputMimeType           json.RawMessage `json:"outputMimeType"`
		OutputCompressionQuality json.RawMessage `json:"outputCompressionQuality"`
		ImageOutputOptions       json.RawMessage `json:"imageOutputOptions"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	unsupported := []struct {
		name  string
		value json.RawMessage
	}{
		{"personGeneration", input.PersonGeneration}, {"prominentPeople", input.ProminentPeople},
		{"outputMimeType", input.OutputMimeType}, {"outputCompressionQuality", input.OutputCompressionQuality},
		{"imageOutputOptions", input.ImageOutputOptions},
	}
	for _, field := range unsupported {
		if len(field.value) > 0 {
			return nil, fmt.Errorf("%s parameter is not supported in Gemini API.", field.name) //nolint:staticcheck // Exact SDK text.
		}
	}
	return ai.Marshal(struct {
		AspectRatio json.RawMessage `json:"aspectRatio,omitempty"`
		ImageSize   json.RawMessage `json:"imageSize,omitempty"`
	}{AspectRatio: googleNonNullRaw(input.AspectRatio), ImageSize: googleNonNullRaw(input.ImageSize)})
}

func googleSpeechConfig(value json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return value, nil
	}
	if trimmed[0] != '"' {
		return nil, fmt.Errorf("Unsupported speechConfig type: %s", googleJSONType(trimmed)) //nolint:staticcheck // Exact SDK text.
	}
	var voice string
	if json.Unmarshal(value, &voice) != nil {
		return nil, errors.New("decode Google speechConfig")
	}
	encoded, err := ai.Marshal(map[string]any{
		"voiceConfig": map[string]any{
			"prebuiltVoiceConfig": map[string]any{"voiceName": voice},
		},
	})
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func convertGoogleTools(tools []ai.Tool) []GoogleTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]GoogleFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, GoogleFunctionDeclaration{
			Name: tool.Name, Description: tool.Description, ParametersJSONSchema: tool.Parameters,
		})
	}
	return []GoogleTool{{FunctionDeclarations: declarations}}
}

func mapGoogleToolChoice(choice GoogleToolChoice) string {
	switch choice {
	case GoogleToolChoiceNone:
		return "NONE"
	case GoogleToolChoiceAny:
		return "ANY"
	default:
		return "AUTO"
	}
}

func mapGoogleStopReason(reason string) ai.StopReason {
	switch reason {
	case "STOP":
		return ai.StopReasonStop
	case "MAX_TOKENS":
		return ai.StopReasonLength
	default:
		return ai.StopReasonError
	}
}

func retainGoogleThoughtSignature(existing, incoming *string) *string {
	if incoming != nil && *incoming != "" {
		value := *incoming
		return &value
	}
	return existing
}

func resolveGoogleThoughtSignature(sameProviderAndModel bool, signature *string) *string {
	if !sameProviderAndModel || signature == nil || len(*signature)%4 != 0 || !googleBase64SignaturePattern.MatchString(*signature) {
		return nil
	}
	value := *signature
	return &value
}

func convertGoogleMessages(model *ai.Model, requestContext ai.Context) ([]GoogleContent, error) {
	messages := transformMessages(requestContext.Messages, model, normalizeGoogleToolCallIDForModel)
	contents := make([]GoogleContent, 0, len(messages))
	for _, message := range messages {
		switch value := message.(type) {
		case *ai.UserMessage:
			parts := googleUserParts(value)
			if value.Content.Text == nil && len(parts) == 0 {
				continue
			}
			contents = append(contents, GoogleContent{Parts: parts, Role: "user"})
		case *ai.AssistantMessage:
			parts, err := googleAssistantParts(model, value)
			if err != nil {
				return nil, err
			}
			if len(parts) > 0 {
				contents = append(contents, GoogleContent{Parts: parts, Role: "model"})
			}
		case *ai.ToolResultMessage:
			contents = appendGoogleToolResult(contents, model, value)
		}
	}
	return contents, nil
}

func googleUserParts(message *ai.UserMessage) []GooglePart {
	if message.Content.Text != nil {
		text := sanitizeText(*message.Content.Text)
		return []GooglePart{{Text: &text}}
	}
	parts := make([]GooglePart, 0, len(message.Content.Blocks))
	for _, item := range message.Content.Blocks {
		switch block := item.(type) {
		case *ai.TextContent:
			text := sanitizeText(block.Text)
			parts = append(parts, GooglePart{Text: &text})
		case *ai.ImageContent:
			parts = append(parts, GooglePart{InlineData: &GoogleInlineData{Data: block.Data, MimeType: block.MimeType}})
		}
	}
	return parts
}

func googleAssistantParts(model *ai.Model, message *ai.AssistantMessage) ([]GooglePart, error) {
	sameProviderAndModel := message.Provider == model.Provider && message.Model == model.ID
	parts := make([]GooglePart, 0, len(message.Content))
	for _, item := range message.Content {
		switch block := item.(type) {
		case *ai.TextContent:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			text := sanitizeText(block.Text)
			parts = append(parts, GooglePart{Text: &text, ThoughtSignature: resolveGoogleThoughtSignature(sameProviderAndModel, block.TextSignature)})
		case *ai.ThinkingContent:
			if strings.TrimSpace(block.Thinking) == "" {
				continue
			}
			text := sanitizeText(block.Thinking)
			part := GooglePart{Text: &text}
			if sameProviderAndModel {
				part.Thought = true
				part.ThoughtSignature = resolveGoogleThoughtSignature(true, block.ThinkingSignature)
			}
			parts = append(parts, part)
		case *ai.ToolCall:
			arguments, err := ai.MarshalToolCallArguments(block)
			if err != nil {
				return nil, err
			}
			call := &GoogleFunctionCall{Args: arguments, Name: block.Name}
			if googleRequiresToolCallID(model.ID) {
				call.ID = block.ID
			}
			parts = append(parts, GooglePart{
				FunctionCall: call, ThoughtSignature: resolveGoogleThoughtSignature(sameProviderAndModel, block.ThoughtSignature),
			})
		}
	}
	return parts, nil
}

func appendGoogleToolResult(contents []GoogleContent, model *ai.Model, message *ai.ToolResultMessage) []GoogleContent {
	textParts := make([]string, 0)
	images := make([]GooglePart, 0)
	for _, item := range message.Content {
		switch block := item.(type) {
		case *ai.TextContent:
			textParts = append(textParts, block.Text)
		case *ai.ImageContent:
			if modelSupportsImage(model) {
				images = append(images, GooglePart{InlineData: &GoogleInlineData{Data: block.Data, MimeType: block.MimeType}})
			}
		}
	}
	text := strings.Join(textParts, "\n")
	if text == "" && len(images) > 0 {
		text = "(see attached image)"
	}
	text = sanitizeText(text)
	responseValue := GoogleFunctionResponseValue{Output: &text}
	if message.IsError {
		responseValue = GoogleFunctionResponseValue{Error: &text}
	}
	response := &GoogleFunctionResponse{Name: message.ToolName, Response: responseValue}
	if googleSupportsMultimodalFunctionResponse(model.ID) && len(images) > 0 {
		response.Parts = make([]GoogleFunctionResponsePart, 0, len(images))
		for _, image := range images {
			response.Parts = append(response.Parts, GoogleFunctionResponsePart{InlineData: &GoogleFunctionResponseInlineData{
				MimeType: image.InlineData.MimeType,
				Data:     image.InlineData.Data,
			}})
		}
	}
	if googleRequiresToolCallID(model.ID) {
		response.ID = message.ToolCallID
	}
	part := GooglePart{FunctionResponse: response}
	last := len(contents) - 1
	if last >= 0 && contents[last].Role == "user" && googleContentHasFunctionResponse(contents[last]) {
		contents[last].Parts = append(contents[last].Parts, part)
	} else {
		contents = append(contents, GoogleContent{Parts: []GooglePart{part}, Role: "user"})
	}
	if len(images) > 0 && !googleSupportsMultimodalFunctionResponse(model.ID) {
		label := "Tool result image:"
		parts := append([]GooglePart{{Text: &label}}, images...)
		contents = append(contents, GoogleContent{Parts: parts, Role: "user"})
	}
	return contents
}

func googleContentHasFunctionResponse(content GoogleContent) bool {
	for _, part := range content.Parts {
		if part.FunctionResponse != nil {
			return true
		}
	}
	return false
}

func googleRequiresToolCallID(modelID string) bool {
	return strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-")
}

func googleSupportsMultimodalFunctionResponse(modelID string) bool {
	match := googleMajorVersionPattern.FindStringSubmatch(strings.ToLower(modelID))
	if len(match) == 2 {
		major, err := strconv.Atoi(match[1])
		return err == nil && major >= 3
	}
	return true
}

func normalizeGoogleToolCallID(value string) string {
	units := utf16.Encode([]rune(value))
	var builder strings.Builder
	builder.Grow(min(len(units), 64))
	for _, unit := range units[:min(len(units), 64)] {
		if unit <= 0x7f && ((unit >= 'a' && unit <= 'z') || (unit >= 'A' && unit <= 'Z') ||
			(unit >= '0' && unit <= '9') || unit == '_' || unit == '-') {
			builder.WriteByte(byte(unit))
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func normalizeGoogleToolCallIDForModel(value string, model *ai.Model, _ *ai.AssistantMessage) string {
	if !googleRequiresToolCallID(model.ID) {
		return value
	}
	return normalizeGoogleToolCallID(value)
}

func validateGoogleParameters(parameters googleDecodedParameters) error {
	if parameters.Model == "" {
		return errors.New("model is required and must be a string")
	}
	if strings.Contains(parameters.Model, "..") || strings.ContainsAny(parameters.Model, "?&") {
		return errors.New("invalid model parameter")
	}
	return nil
}
