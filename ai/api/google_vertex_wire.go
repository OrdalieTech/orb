package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

type googleVertexWireRequest struct {
	Contents          json.RawMessage             `json:"contents,omitempty"`
	SystemInstruction json.RawMessage             `json:"systemInstruction,omitempty"`
	SafetySettings    json.RawMessage             `json:"safetySettings,omitempty"`
	Tools             json.RawMessage             `json:"tools,omitempty"`
	ToolConfig        json.RawMessage             `json:"toolConfig,omitempty"`
	Labels            json.RawMessage             `json:"labels,omitempty"`
	CachedContent     json.RawMessage             `json:"cachedContent,omitempty"`
	ModelArmorConfig  json.RawMessage             `json:"modelArmorConfig,omitempty"`
	ServiceTier       json.RawMessage             `json:"serviceTier,omitempty"`
	GenerationConfig  *googleVertexGenerationWire `json:"generationConfig,omitempty"`
}

type googleVertexGenerationWire struct {
	Temperature        json.RawMessage `json:"temperature,omitempty"`
	TopP               json.RawMessage `json:"topP,omitempty"`
	TopK               json.RawMessage `json:"topK,omitempty"`
	CandidateCount     json.RawMessage `json:"candidateCount,omitempty"`
	MaxOutputTokens    json.RawMessage `json:"maxOutputTokens,omitempty"`
	StopSequences      json.RawMessage `json:"stopSequences,omitempty"`
	ResponseLogprobs   json.RawMessage `json:"responseLogprobs,omitempty"`
	Logprobs           json.RawMessage `json:"logprobs,omitempty"`
	PresencePenalty    json.RawMessage `json:"presencePenalty,omitempty"`
	FrequencyPenalty   json.RawMessage `json:"frequencyPenalty,omitempty"`
	Seed               json.RawMessage `json:"seed,omitempty"`
	ResponseMimeType   json.RawMessage `json:"responseMimeType,omitempty"`
	ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
	RoutingConfig      json.RawMessage `json:"routingConfig,omitempty"`
	ModelConfig        json.RawMessage `json:"modelConfig,omitempty"`
	ResponseModalities json.RawMessage `json:"responseModalities,omitempty"`
	MediaResolution    json.RawMessage `json:"mediaResolution,omitempty"`
	SpeechConfig       json.RawMessage `json:"speechConfig,omitempty"`
	AudioTimestamp     json.RawMessage `json:"audioTimestamp,omitempty"`
	ThinkingConfig     json.RawMessage `json:"thinkingConfig,omitempty"`
	ImageConfig        json.RawMessage `json:"imageConfig,omitempty"`
}

func googleVertexWirePayload(parameters googleDecodedParameters, project, location string) (googleVertexWireRequest, error) {
	contents, err := googleVertexContents(parameters.Contents)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	wire := googleVertexWireRequest{Contents: contents}
	if !googleRawPresent(parameters.Config) {
		return wire, nil
	}
	var config googleRawGenerateContentConfig
	if err := json.Unmarshal(parameters.Config, &config); err != nil {
		return googleVertexWireRequest{}, err
	}
	systemInstruction, err := googleVertexSystemInstruction(config.SystemInstruction)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	responseSchema := config.ResponseSchema
	responseJSONSchema := config.ResponseJSONSchema
	if googleSchemaHasDollar(responseSchema) && !googleJSTruthy(responseJSONSchema) {
		responseJSONSchema, responseSchema = responseSchema, nil
	}
	responseSchema, err = normalizeGoogleResponseSchema(responseSchema)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	tools, err := googleVertexTools(config.Tools)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	toolConfig, err := googleVertexToolConfig(config.ToolConfig)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	cachedContent, err := googleVertexCachedContent(config.CachedContent, project, location)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	speechConfig, err := googleSpeechConfig(config.SpeechConfig)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	imageConfig, err := googleVertexImageConfig(config.ImageConfig)
	if err != nil {
		return googleVertexWireRequest{}, err
	}
	if len(config.EnableEnhancedCivicAnswers) > 0 {
		return googleVertexWireRequest{}, errors.New("enableEnhancedCivicAnswers parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).") //nolint:staticcheck // Exact SDK text.
	}
	wire.SystemInstruction = systemInstruction
	wire.SafetySettings = googleNonNullRaw(config.SafetySettings)
	wire.Tools = tools
	wire.ToolConfig = toolConfig
	wire.Labels = googleNonNullRaw(config.Labels)
	wire.CachedContent = cachedContent
	wire.ModelArmorConfig = googleNonNullRaw(config.ModelArmorConfig)
	wire.ServiceTier = googleNonNullRaw(config.ServiceTier)
	wire.GenerationConfig = &googleVertexGenerationWire{
		Temperature: googleNonNullRaw(config.Temperature), TopP: googleNonNullRaw(config.TopP),
		TopK: googleNonNullRaw(config.TopK), CandidateCount: googleNonNullRaw(config.CandidateCount),
		MaxOutputTokens: googleNonNullRaw(config.MaxOutputTokens), StopSequences: googleNonNullRaw(config.StopSequences),
		ResponseLogprobs: googleNonNullRaw(config.ResponseLogprobs), Logprobs: googleNonNullRaw(config.Logprobs),
		PresencePenalty: googleNonNullRaw(config.PresencePenalty), FrequencyPenalty: googleNonNullRaw(config.FrequencyPenalty),
		Seed: googleNonNullRaw(config.Seed), ResponseMimeType: googleNonNullRaw(config.ResponseMimeType),
		ResponseSchema: responseSchema, ResponseJSONSchema: googleNonNullRaw(responseJSONSchema),
		RoutingConfig: googleNonNullRaw(config.RoutingConfig), ModelConfig: googleNonNullRaw(config.ModelSelectionConfig),
		ResponseModalities: googleNonNullRaw(config.ResponseModalities), MediaResolution: googleNonNullRaw(config.MediaResolution),
		SpeechConfig: speechConfig, AudioTimestamp: googleNonNullRaw(config.AudioTimestamp),
		ThinkingConfig: googleNonNullRaw(config.ThinkingConfig), ImageConfig: imageConfig,
	}
	return wire, nil
}

func googleVertexContents(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(value)
	if trimmed[0] != '[' {
		if googleRawHasField(value, "functionCall") || googleRawHasField(value, "functionResponse") {
			return nil, errors.New("To specify functionCall or functionResponse parts, please wrap them in a Content object, specifying the role for them") //nolint:staticcheck // Exact SDK text.
		}
		var content json.RawMessage
		var err error
		if googleRawIsContent(value) {
			content, err = googleVertexContent(value)
		} else {
			content, err = googleVertexContentFromParts([]json.RawMessage{value})
		}
		if err != nil {
			return nil, err
		}
		return ai.Marshal([]json.RawMessage{content})
	}
	var values []json.RawMessage
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("contents are required")
	}
	contentArray := googleRawIsContent(values[0])
	contents := make([]json.RawMessage, 0, len(values))
	parts := make([]json.RawMessage, 0, len(values))
	for _, item := range values {
		isContent := googleRawIsContent(item)
		if isContent != contentArray {
			return nil, errors.New("Mixing Content and Parts is not supported, please group the parts into a the appropriate Content objects and specify the roles for them") //nolint:staticcheck // Exact SDK text.
		}
		if isContent {
			content, err := googleVertexContent(item)
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
			continue
		}
		if googleRawHasField(item, "functionCall") || googleRawHasField(item, "functionResponse") {
			return nil, errors.New("To specify functionCall or functionResponse parts, please wrap them, and any other parts, in Content objects as appropriate, specifying the roles for them") //nolint:staticcheck // Exact SDK text.
		}
		parts = append(parts, item)
	}
	if !contentArray {
		content, err := googleVertexContentFromParts(parts)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	return ai.Marshal(contents)
}

func googleVertexContent(value json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Parts json.RawMessage `json:"parts"`
		Role  json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(input.Parts, &parts); err != nil {
		return nil, err
	}
	converted, err := googleVertexParts(parts)
	if err != nil {
		return nil, err
	}
	encodedParts, err := ai.Marshal(converted)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Parts json.RawMessage `json:"parts,omitempty"`
		Role  json.RawMessage `json:"role,omitempty"`
	}{Parts: encodedParts, Role: googleNonNullRaw(input.Role)})
}

func googleVertexContentFromParts(parts []json.RawMessage) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, errors.New("PartListUnion is required") //nolint:staticcheck // Exact SDK text.
	}
	converted, err := googleVertexParts(parts)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Parts []json.RawMessage `json:"parts"`
		Role  string            `json:"role"`
	}{Parts: converted, Role: "user"})
}

func googleVertexParts(parts []json.RawMessage) ([]json.RawMessage, error) {
	converted := make([]json.RawMessage, len(parts))
	for index, part := range parts {
		trimmed := bytes.TrimSpace(part)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return nil, errors.New("PartUnion is required") //nolint:staticcheck // Exact SDK text.
		}
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err != nil {
				return nil, err
			}
			encoded, err := ai.Marshal(struct {
				Text string `json:"text"`
			}{Text: text})
			if err != nil {
				return nil, err
			}
			converted[index] = encoded
			continue
		}
		if trimmed[0] != '{' && trimmed[0] != '[' {
			return nil, fmt.Errorf("Unsupported part type: %s", googleJSONType(string(trimmed))) //nolint:staticcheck // Exact SDK text.
		}
		wire, err := googleVertexPart(trimmed)
		if err != nil {
			return nil, err
		}
		converted[index] = wire
	}
	return converted, nil
}

func googleVertexPart(value json.RawMessage) (json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage(`{}`), nil
	}
	var input struct {
		MediaResolution     json.RawMessage `json:"mediaResolution"`
		CodeExecutionResult json.RawMessage `json:"codeExecutionResult"`
		ExecutableCode      json.RawMessage `json:"executableCode"`
		FileData            json.RawMessage `json:"fileData"`
		FunctionCall        json.RawMessage `json:"functionCall"`
		FunctionResponse    json.RawMessage `json:"functionResponse"`
		InlineData          json.RawMessage `json:"inlineData"`
		Text                json.RawMessage `json:"text"`
		Thought             json.RawMessage `json:"thought"`
		ThoughtSignature    json.RawMessage `json:"thoughtSignature"`
		VideoMetadata       json.RawMessage `json:"videoMetadata"`
		ToolCall            json.RawMessage `json:"toolCall"`
		ToolResponse        json.RawMessage `json:"toolResponse"`
		PartMetadata        json.RawMessage `json:"partMetadata"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value json.RawMessage
	}{{"toolCall", input.ToolCall}, {"toolResponse", input.ToolResponse}, {"partMetadata", input.PartMetadata}} {
		if len(field.value) > 0 {
			return nil, fmt.Errorf("%s parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).", field.name) //nolint:staticcheck // Exact SDK text.
		}
	}
	return ai.Marshal(struct {
		MediaResolution     json.RawMessage `json:"mediaResolution,omitempty"`
		CodeExecutionResult json.RawMessage `json:"codeExecutionResult,omitempty"`
		ExecutableCode      json.RawMessage `json:"executableCode,omitempty"`
		FileData            json.RawMessage `json:"fileData,omitempty"`
		FunctionCall        json.RawMessage `json:"functionCall,omitempty"`
		FunctionResponse    json.RawMessage `json:"functionResponse,omitempty"`
		InlineData          json.RawMessage `json:"inlineData,omitempty"`
		Text                json.RawMessage `json:"text,omitempty"`
		Thought             json.RawMessage `json:"thought,omitempty"`
		ThoughtSignature    json.RawMessage `json:"thoughtSignature,omitempty"`
		VideoMetadata       json.RawMessage `json:"videoMetadata,omitempty"`
	}{
		MediaResolution: googleNonNullRaw(input.MediaResolution), CodeExecutionResult: googleNonNullRaw(input.CodeExecutionResult),
		ExecutableCode: googleNonNullRaw(input.ExecutableCode), FileData: googleNonNullRaw(input.FileData),
		FunctionCall: googleNonNullRaw(input.FunctionCall), FunctionResponse: googleNonNullRaw(input.FunctionResponse),
		InlineData: googleNonNullRaw(input.InlineData), Text: googleNonNullRaw(input.Text), Thought: googleNonNullRaw(input.Thought),
		ThoughtSignature: googleNonNullRaw(input.ThoughtSignature), VideoMetadata: googleNonNullRaw(input.VideoMetadata),
	})
}

func googleVertexSystemInstruction(value json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '{' && googleRawIsContent(value) {
		return googleVertexContent(value)
	}
	parts := []json.RawMessage{value}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(value, &parts); err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			return nil, errors.New("PartListUnion is required") //nolint:staticcheck // Exact SDK text.
		}
	}
	return googleVertexContentFromParts(parts)
}

func googleVertexTools(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(value, &tools); err != nil {
		return nil, errors.New("tools is required and must be an array of Tools") //nolint:staticcheck // Exact SDK text.
	}
	converted := make([]json.RawMessage, len(tools))
	for index, tool := range tools {
		wire, err := googleVertexTool(tool)
		if err != nil {
			return nil, err
		}
		converted[index] = wire
	}
	return ai.Marshal(converted)
}

func googleVertexTool(value json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Retrieval             json.RawMessage `json:"retrieval"`
		ComputerUse           json.RawMessage `json:"computerUse"`
		FileSearch            json.RawMessage `json:"fileSearch"`
		GoogleSearch          json.RawMessage `json:"googleSearch"`
		GoogleMaps            json.RawMessage `json:"googleMaps"`
		CodeExecution         json.RawMessage `json:"codeExecution"`
		EnterpriseWebSearch   json.RawMessage `json:"enterpriseWebSearch"`
		FunctionDeclarations  json.RawMessage `json:"functionDeclarations"`
		GoogleSearchRetrieval json.RawMessage `json:"googleSearchRetrieval"`
		ParallelAISearch      json.RawMessage `json:"parallelAiSearch"`
		URLContext            json.RawMessage `json:"urlContext"`
		MCPServers            json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.FileSearch) > 0 {
		return nil, errors.New("fileSearch parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).") //nolint:staticcheck // Exact SDK text.
	}
	if len(input.MCPServers) > 0 {
		return nil, errors.New("mcpServers parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).") //nolint:staticcheck // Exact SDK text.
	}
	declarations, err := googleVertexFunctionDeclarations(input.FunctionDeclarations)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Retrieval             json.RawMessage `json:"retrieval,omitempty"`
		ComputerUse           json.RawMessage `json:"computerUse,omitempty"`
		GoogleSearch          json.RawMessage `json:"googleSearch,omitempty"`
		GoogleMaps            json.RawMessage `json:"googleMaps,omitempty"`
		CodeExecution         json.RawMessage `json:"codeExecution,omitempty"`
		EnterpriseWebSearch   json.RawMessage `json:"enterpriseWebSearch,omitempty"`
		FunctionDeclarations  json.RawMessage `json:"functionDeclarations,omitempty"`
		GoogleSearchRetrieval json.RawMessage `json:"googleSearchRetrieval,omitempty"`
		ParallelAISearch      json.RawMessage `json:"parallelAiSearch,omitempty"`
		URLContext            json.RawMessage `json:"urlContext,omitempty"`
	}{
		Retrieval: googleNonNullRaw(input.Retrieval), ComputerUse: googleNonNullRaw(input.ComputerUse),
		GoogleSearch: googleNonNullRaw(input.GoogleSearch), GoogleMaps: googleNonNullRaw(input.GoogleMaps),
		CodeExecution: googleNonNullRaw(input.CodeExecution), EnterpriseWebSearch: googleNonNullRaw(input.EnterpriseWebSearch),
		FunctionDeclarations: declarations, GoogleSearchRetrieval: googleNonNullRaw(input.GoogleSearchRetrieval),
		ParallelAISearch: googleNonNullRaw(input.ParallelAISearch), URLContext: googleNonNullRaw(input.URLContext),
	})
}

func googleVertexFunctionDeclarations(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	decoded, err := decodeGoogleOrderedJSON(value)
	if err != nil {
		return nil, err
	}
	declarations, ok := decoded.(googleJSONArray)
	if !ok {
		return nil, errors.New("functionDeclarations must be an array")
	}
	result := make(googleJSONArray, 0, len(declarations))
	for _, value := range declarations {
		declaration, ok := value.(googleJSONObject)
		if !ok {
			return nil, errors.New("function declaration must be an object")
		}
		if err := normalizeGoogleDeclarationSchema(&declaration, "parameters", "parametersJsonSchema"); err != nil {
			return nil, err
		}
		if err := normalizeGoogleDeclarationSchema(&declaration, "response", "responseJsonSchema"); err != nil {
			return nil, err
		}
		if _, exists := declaration.Value("behavior"); exists {
			return nil, errors.New("behavior parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).") //nolint:staticcheck // Exact SDK text.
		}
		ordered := googleJSONObject{}
		for _, name := range []string{"description", "name", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"} {
			if field, exists := declaration.Value(name); exists && field != nil {
				ordered.Set(name, field)
			}
		}
		result = append(result, ordered)
	}
	return ai.Marshal(result)
}

func googleVertexToolConfig(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		RetrievalConfig                  json.RawMessage `json:"retrievalConfig"`
		FunctionCallingConfig            json.RawMessage `json:"functionCallingConfig"`
		IncludeServerSideToolInvocations json.RawMessage `json:"includeServerSideToolInvocations"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.IncludeServerSideToolInvocations) > 0 {
		return nil, errors.New("includeServerSideToolInvocations parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		RetrievalConfig       json.RawMessage `json:"retrievalConfig,omitempty"`
		FunctionCallingConfig json.RawMessage `json:"functionCallingConfig,omitempty"`
	}{RetrievalConfig: googleNonNullRaw(input.RetrievalConfig), FunctionCallingConfig: googleNonNullRaw(input.FunctionCallingConfig)})
}

func googleVertexCachedContent(value json.RawMessage, project, location string) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var name string
	if err := json.Unmarshal(value, &name); err != nil {
		return nil, errors.New("name must be a string") //nolint:staticcheck // Exact SDK text.
	}
	switch {
	case strings.HasPrefix(name, "projects/"):
	case strings.HasPrefix(name, "locations/"):
		name = "projects/" + project + "/" + name
	case strings.HasPrefix(name, "cachedContents/"):
		name = "projects/" + project + "/locations/" + location + "/" + name
	case !strings.Contains(name, "/"):
		name = "projects/" + project + "/locations/" + location + "/cachedContents/" + name
	}
	return ai.Marshal(name)
}

func googleVertexImageConfig(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	decoded, err := decodeGoogleOrderedJSON(value)
	if err != nil {
		return nil, err
	}
	input, ok := decoded.(googleJSONObject)
	if !ok {
		return nil, errors.New("imageConfig must be an object")
	}
	output := googleJSONObject{}
	for _, name := range []string{"aspectRatio", "imageSize", "personGeneration", "prominentPeople"} {
		if field, exists := input.Value(name); exists && field != nil {
			output.Set(name, field)
		}
	}
	imageOutput := googleJSONObject{}
	if field, exists := input.Value("outputMimeType"); exists && field != nil {
		imageOutput.Set("mimeType", field)
	}
	if field, exists := input.Value("outputCompressionQuality"); exists && field != nil {
		imageOutput.Set("compressionQuality", field)
	}
	if field, exists := input.Value("imageOutputOptions"); exists && field != nil {
		provided, ok := field.(googleJSONObject)
		if !ok {
			return nil, errors.New("imageOutputOptions must be an object")
		}
		for _, member := range provided {
			imageOutput.Set(member.Name, member.Value)
		}
	}
	if len(imageOutput) > 0 {
		output.Set("imageOutputOptions", imageOutput)
	}
	return ai.Marshal(output)
}
