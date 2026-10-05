package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/partialjson"
)

var (
	errUnknownMessageRole = errors.New("ai: unknown message role")
)

func MarshalMessage(message Message) ([]byte, error) {
	if message == nil {
		return nil, errors.New("ai: nil message")
	}
	return Marshal(message)
}

// Marshal encodes the ai wire format with JSON.stringify-compatible string
// escaping. Internal protocol and persistence surfaces must use this instead
// of encoding/json's HTML-safe default.
func Marshal(value any) ([]byte, error) {
	// Messages encode themselves into compact wire JSON already; handing that
	// to the encoder again only revalidated it.
	switch message := value.(type) {
	case *AssistantMessage:
		if message != nil {
			return message.MarshalJSON()
		}
	case *ToolResultMessage:
		if message != nil {
			return message.MarshalJSON()
		}
	case *UserMessage:
		if message != nil {
			return message.MarshalJSON()
		}
	case *SystemMessage:
		if message != nil {
			return message.MarshalJSON()
		}
	}
	return marshalJSON(value)
}

const (
	usageOptionalsDefault uint8 = iota
	usageOptionalsBeforeTotals
	usageOptionalsAfterCost
)

func (usage Usage) MarshalJSON() ([]byte, error) { return usage.appendWire(nil) }

func (usage Usage) appendWire(dst []byte) ([]byte, error) {
	beforeTotals := usage.optionalOrder == usageOptionalsBeforeTotals ||
		usage.optionalOrder == usageOptionalsDefault && usage.CacheWrite1h == nil
	dst = strconv.AppendInt(append(dst, `{"input":`...), usage.Input, 10)
	dst = strconv.AppendInt(append(dst, `,"output":`...), usage.Output, 10)
	dst = strconv.AppendInt(append(dst, `,"cacheRead":`...), usage.CacheRead, 10)
	dst = strconv.AppendInt(append(dst, `,"cacheWrite":`...), usage.CacheWrite, 10)
	optionals := func(dst []byte) []byte {
		dst = appendOptionalInt(dst, `,"cacheWrite1h":`, usage.CacheWrite1h)
		return appendOptionalInt(dst, `,"reasoning":`, usage.Reasoning)
	}
	if beforeTotals {
		dst = optionals(dst)
	}
	dst = strconv.AppendInt(append(dst, `,"totalTokens":`...), usage.TotalTokens, 10)
	cost := usage.Cost
	for index, value := range [...]float64{cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite, cost.Total} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			_, err := marshalJSON(cost)
			return nil, err
		}
		dst = jsonwire.AppendFloat(append(dst, costMembers[index]...), value)
	}
	dst = append(dst, '}')
	if !beforeTotals {
		dst = optionals(dst)
	}
	return append(dst, '}'), nil
}

var costMembers = [...]string{`,"cost":{"input":`, `,"output":`, `,"cacheRead":`, `,"cacheWrite":`, `,"total":`}

func (usage *Usage) UnmarshalJSON(data []byte) error {
	type plainUsage Usage
	var decoded plainUsage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*usage = Usage(decoded)
	if usage.CacheWrite1h != nil || usage.Reasoning != nil {
		usage.optionalOrder = usageOptionalsAfterCost
		if topLevelMemberBefore(data, "cacheWrite1h", "totalTokens") || topLevelMemberBefore(data, "reasoning", "totalTokens") {
			usage.optionalOrder = usageOptionalsBeforeTotals
		}
	}
	return nil
}

// SetUsageOptionalsBeforeTotals preserves the order of upstream usage objects built by compaction aggregation.
func SetUsageOptionalsBeforeTotals(usage *Usage) {
	if usage != nil && (usage.CacheWrite1h != nil || usage.Reasoning != nil) {
		usage.optionalOrder = usageOptionalsBeforeTotals
	}
}

func UnmarshalMessage(data []byte) (Message, error) {
	var header struct {
		Role string `json:"role"`
	}
	role, plain := plainRole(data)
	if !plain {
		if err := json.Unmarshal(data, &header); err != nil {
			return nil, fmt.Errorf("ai: decode message role: %w", err)
		}
		role = header.Role
	}
	message, err := unmarshalMessageAs(role, data)
	if err != nil && plain {
		// plainRole read the role without validating data: invalid JSON
		// fails as decoding the role did.
		if roleErr := json.Unmarshal(data, &header); roleErr != nil {
			return nil, fmt.Errorf("ai: decode message role: %w", roleErr)
		}
	}
	return message, err
}

func unmarshalMessageAs(role string, data []byte) (Message, error) {
	var message Message
	switch role {
	case "system":
		message = &SystemMessage{}
	case "user":
		message = &UserMessage{}
	case "assistant":
		message = &AssistantMessage{}
	case "toolResult":
		message = &ToolResultMessage{}
	default:
		return nil, fmt.Errorf("%w %q", errUnknownMessageRole, role)
	}
	if err := json.Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("ai: decode %s message: %w", role, err)
	}
	return message, nil
}

func (sections SystemPromptSections) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, section := range sections {
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := marshalJSON(section.Name)
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		if section.Text == nil {
			output.WriteString("null")
		} else {
			text, err := marshalJSON(*section.Text)
			if err != nil {
				return nil, err
			}
			output.Write(text)
		}
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func (sections *SystemPromptSections) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("ai: system prompt sections must be an object")
	}
	result := SystemPromptSections{}
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		name, ok := nameToken.(string)
		if !ok {
			return errors.New("ai: invalid system prompt section name")
		}
		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return decodeErr
		}
		section := SystemPromptSection{Name: name}
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			var text string
			if decodeErr := json.Unmarshal(raw, &text); decodeErr != nil {
				return decodeErr
			}
			section.Text = &text
		}
		result = append(result, section)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*sections = result
	return nil
}

func (message SystemMessage) MarshalJSON() ([]byte, error) { return marshalWire(message) }

func (message SystemMessage) appendWire(dst []byte) ([]byte, error) {
	dst = append(dst, `{"role":"system","content":`...)
	if text, ok := message.Content.(string); ok && utf8.ValidString(text) {
		dst = jsonwire.AppendString(dst, text)
	} else if message.Content == nil {
		dst = append(dst, `""`...)
	} else {
		// Blocks, and strings encoding/json coerces to valid UTF-8.
		content, err := marshalJSON(message.Content)
		if err != nil {
			return nil, err
		}
		dst = append(dst, content...)
	}
	if len(message.Sections) > 0 {
		sections, err := message.Sections.MarshalJSON()
		if err != nil {
			return nil, err
		}
		dst = append(append(dst, `,"sections":`...), sections...)
	}
	timestamp := func(dst []byte) []byte {
		return strconv.AppendInt(append(dst, `,"timestamp":`...), message.Timestamp, 10)
	}
	if message.toolFieldsAfterTimestamp {
		dst = timestamp(dst)
	}
	if len(message.ToolsAdded) > 0 {
		dst = append(dst, `,"toolsAdded":[`...)
		for index, tool := range message.ToolsAdded {
			if index > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = tool.appendWire(dst); err != nil {
				return nil, err
			}
		}
		dst = append(dst, ']')
	}
	if len(message.ToolsRemoved) > 0 {
		removed, err := marshalJSON(message.ToolsRemoved)
		if err != nil {
			return nil, err
		}
		dst = append(append(dst, `,"toolsRemoved":`...), removed...)
	}
	if !message.toolFieldsAfterTimestamp {
		dst = timestamp(dst)
	}
	return append(dst, '}'), nil
}

// appendWire appends tool as encoding/json and Marshal's fixes encode it.
func (tool Tool) appendWire(dst []byte) ([]byte, error) {
	parameters := []byte(tool.Parameters)
	if len(parameters) == 0 {
		parameters = []byte("{}")
	}
	encoded, err := jsonwire.AppendCompact(nil, parameters)
	if err != nil || !utf8.ValidString(tool.Name) || !utf8.ValidString(tool.Label) || !utf8.ValidString(tool.Description) {
		// encoding/json coerces invalid UTF-8 and reports an invalid schema.
		encoded, err := marshalJSON(tool)
		return append(dst, encoded...), err
	}
	dst = jsonwire.AppendString(append(dst, `{"name":`...), tool.Name)
	if tool.Label != "" {
		dst = jsonwire.AppendString(append(dst, `,"label":`...), tool.Label)
	}
	dst = jsonwire.AppendString(append(dst, `,"description":`...), tool.Description)
	dst = append(append(dst, `,"parameters":`...), encoded...)
	if config := tool.ConstrainedSampling; config != nil {
		dst = jsonwire.AppendString(append(dst, `,"constrainedSampling":{"type":`...), string(config.Type))
		if config.Strict != "" {
			dst = jsonwire.AppendString(append(dst, `,"strict":`...), string(config.Strict))
		}
		if variants := config.Variants; variants != nil {
			dst = append(dst, `,"variants":{`...)
			if variants.OpenAILark != nil {
				dst = jsonwire.AppendString(append(dst, `"openai_lark":`...), *variants.OpenAILark)
			}
			if variants.OpenAIRegex != nil {
				if variants.OpenAILark != nil {
					dst = append(dst, ',')
				}
				dst = jsonwire.AppendString(append(dst, `"openai_regex":`...), *variants.OpenAIRegex)
			}
			dst = append(dst, '}')
		}
		dst = append(dst, '}')
	}
	return append(dst, '}'), nil
}

func (message *SystemMessage) UnmarshalJSON(data []byte) error {
	var payload struct {
		Content      json.RawMessage      `json:"content"`
		Sections     SystemPromptSections `json:"sections"`
		ToolsAdded   []Tool               `json:"toolsAdded"`
		ToolsRemoved []ToolReference      `json:"toolsRemoved"`
		Timestamp    int64                `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	var content any = ""
	if len(payload.Content) > 0 && !bytes.Equal(bytes.TrimSpace(payload.Content), []byte("null")) {
		var text string
		if err := json.Unmarshal(payload.Content, &text); err == nil {
			content = text
		} else {
			var blocks []TextContent
			if blockErr := json.Unmarshal(payload.Content, &blocks); blockErr != nil {
				return blockErr
			}
			content = blocks
		}
	}
	*message = SystemMessage{Content: content, Sections: payload.Sections, ToolsAdded: payload.ToolsAdded, ToolsRemoved: payload.ToolsRemoved, Timestamp: payload.Timestamp}
	message.toolFieldsAfterTimestamp = bytes.Index(data, []byte(`"timestamp"`)) < bytes.Index(data, []byte(`"toolsAdded"`)) && bytes.Contains(data, []byte(`"toolsAdded"`))
	return nil
}

func (message UserMessage) MarshalJSON() ([]byte, error) { return marshalWire(message) }

func (message UserMessage) appendWire(dst []byte) ([]byte, error) {
	dst, err := message.Content.appendWire(append(dst, `{"role":"user","content":`...))
	if err != nil {
		return nil, err
	}
	return append(strconv.AppendInt(append(dst, `,"timestamp":`...), message.Timestamp, 10), '}'), nil
}
func (message AssistantMessage) MarshalJSON() ([]byte, error) { return marshalWire(message) }

// The members after "model", in the orders assistantMemberOrders lists.
const (
	assistantUsage = iota
	assistantStopReason
	assistantTimestamp
	assistantResponseID
	assistantProviderThinkingLevel
	assistantResponseModel
	assistantDiagnostics
	assistantEndTurn
	assistantRawStopReason
	assistantErrorMessage
	assistantThinkingLevel
)

// assistantMemberOrders are pi's order, then the ones its error paths write,
// with errorMessage before timestamp or before responseId.
var assistantMemberOrders = [...][11]uint8{
	{assistantUsage, assistantStopReason, assistantTimestamp, assistantResponseID, assistantProviderThinkingLevel, assistantResponseModel, assistantDiagnostics, assistantEndTurn, assistantRawStopReason, assistantErrorMessage, assistantThinkingLevel},
	{assistantUsage, assistantStopReason, assistantErrorMessage, assistantResponseID, assistantProviderThinkingLevel, assistantResponseModel, assistantDiagnostics, assistantTimestamp, assistantEndTurn, assistantRawStopReason, assistantThinkingLevel},
	{assistantUsage, assistantStopReason, assistantTimestamp, assistantEndTurn, assistantRawStopReason, assistantErrorMessage, assistantResponseID, assistantProviderThinkingLevel, assistantResponseModel, assistantDiagnostics, assistantThinkingLevel},
}

func (message AssistantMessage) appendWire(dst []byte) ([]byte, error) {
	dst, err := appendWireBlocks(append(dst, `{"role":"assistant","content":`...), message.Content)
	if err != nil {
		return nil, err
	}
	dst = jsonwire.AppendString(append(dst, `,"api":`...), string(message.API))
	dst = jsonwire.AppendString(append(dst, `,"provider":`...), string(message.Provider))
	if !message.modelOmitted {
		dst = jsonwire.AppendString(append(dst, `,"model":`...), message.Model)
	}
	order := assistantMemberOrders[0]
	if message.ErrorMessage != nil && message.errorBeforeTimestamp {
		order = assistantMemberOrders[1]
	} else if message.ErrorMessage != nil && message.errorBeforeResponseID {
		order = assistantMemberOrders[2]
	}
	// Moves recorded from a decoded message apply when both members are present.
	moveBefore := func(member, before uint8) {
		from, to := slices.Index(order[:], member), slices.Index(order[:], before)
		if message.hasMember(member) && message.hasMember(before) && from > to {
			copy(order[to+1:from+1], order[to:from])
			order[to] = member
		}
	}
	if message.providerThinkingLevelBeforeUsage {
		moveBefore(assistantProviderThinkingLevel, assistantUsage)
	}
	if message.rawStopBeforeDiagnostics {
		moveBefore(assistantRawStopReason, assistantDiagnostics)
	}
	for _, member := range order {
		switch member {
		case assistantUsage:
			if dst, err = message.Usage.appendWire(append(dst, `,"usage":`...)); err != nil {
				return nil, err
			}
		case assistantStopReason:
			dst = jsonwire.AppendString(append(dst, `,"stopReason":`...), string(message.StopReason))
		case assistantTimestamp:
			dst = strconv.AppendInt(append(dst, `,"timestamp":`...), message.Timestamp, 10)
		case assistantResponseID:
			dst = appendOptionalString(dst, `,"responseId":`, message.ResponseID)
		case assistantProviderThinkingLevel:
			dst = appendOptionalString(dst, `,"providerThinkingLevel":`, message.ProviderThinkingLevel)
		case assistantResponseModel:
			dst = appendOptionalString(dst, `,"responseModel":`, message.ResponseModel)
		case assistantDiagnostics:
			if message.Diagnostics != nil {
				diagnostics, err := marshalJSON(message.Diagnostics)
				if err != nil {
					return nil, err
				}
				dst = append(append(dst, `,"diagnostics":`...), diagnostics...)
			}
		case assistantEndTurn:
			dst = appendOptionalBool(dst, `,"endTurn":`, message.EndTurn)
		case assistantRawStopReason:
			dst = appendOptionalString(dst, `,"rawStopReason":`, message.RawStopReason)
		case assistantErrorMessage:
			dst = appendOptionalString(dst, `,"errorMessage":`, message.ErrorMessage)
		case assistantThinkingLevel:
			dst = appendOptionalString(dst, `,"thinkingLevel":`, (*string)(message.ThinkingLevel))
		}
	}
	return append(dst, '}'), nil
}

// hasMember reports whether appendWire writes member.
func (message *AssistantMessage) hasMember(member uint8) bool {
	switch member {
	case assistantResponseID:
		return message.ResponseID != nil
	case assistantProviderThinkingLevel:
		return message.ProviderThinkingLevel != nil
	case assistantResponseModel:
		return message.ResponseModel != nil
	case assistantDiagnostics:
		return message.Diagnostics != nil
	case assistantEndTurn:
		return message.EndTurn != nil
	case assistantRawStopReason:
		return message.RawStopReason != nil
	case assistantErrorMessage:
		return message.ErrorMessage != nil
	case assistantThinkingLevel:
		return message.ThinkingLevel != nil
	}
	return true
}

func (message *AssistantMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Content               AssistantContent              `json:"content"`
		API                   json.RawMessage               `json:"api"`
		Provider              json.RawMessage               `json:"provider"`
		Model                 json.RawMessage               `json:"model"`
		Usage                 Usage                         `json:"usage"`
		StopReason            json.RawMessage               `json:"stopReason"`
		Timestamp             int64                         `json:"timestamp"`
		ResponseID            json.RawMessage               `json:"responseId"`
		ProviderThinkingLevel json.RawMessage               `json:"providerThinkingLevel"`
		ResponseModel         json.RawMessage               `json:"responseModel"`
		Diagnostics           *[]AssistantMessageDiagnostic `json:"diagnostics"`
		EndTurn               *bool                         `json:"endTurn"`
		RawStopReason         json.RawMessage               `json:"rawStopReason"`
		ErrorMessage          json.RawMessage               `json:"errorMessage"`
		ThinkingLevel         *ModelThinkingLevel           `json:"thinkingLevel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	api, err := unmarshalWireString(raw.API)
	if err != nil {
		return err
	}
	provider, err := unmarshalWireString(raw.Provider)
	if err != nil {
		return err
	}
	model, err := unmarshalWireString(raw.Model)
	if err != nil {
		return err
	}
	stopReason, err := unmarshalWireString(raw.StopReason)
	if err != nil {
		return err
	}
	providerThinkingLevel, err := unmarshalOptionalWireString(raw.ProviderThinkingLevel)
	if err != nil {
		return err
	}
	responseID, err := unmarshalOptionalWireString(raw.ResponseID)
	if err != nil {
		return err
	}
	responseModel, err := unmarshalOptionalWireString(raw.ResponseModel)
	if err != nil {
		return err
	}
	errorMessage, err := unmarshalOptionalWireString(raw.ErrorMessage)
	if err != nil {
		return err
	}
	rawStopReason, err := unmarshalOptionalWireString(raw.RawStopReason)
	if err != nil {
		return err
	}
	*message = AssistantMessage{
		Content:               raw.Content,
		API:                   API(api),
		Provider:              ProviderID(provider),
		Model:                 model,
		Usage:                 raw.Usage,
		StopReason:            StopReason(stopReason),
		Timestamp:             raw.Timestamp,
		ResponseID:            responseID,
		ProviderThinkingLevel: providerThinkingLevel, ResponseModel: responseModel,
		Diagnostics:   raw.Diagnostics,
		ErrorMessage:  errorMessage,
		RawStopReason: rawStopReason,
		EndTurn:       raw.EndTurn,
		ThinkingLevel: raw.ThinkingLevel,
	}
	// Each order flag needs its first member present; rescanning messages
	// without it dominated decoding.
	message.rawStopBeforeDiagnostics = len(raw.RawStopReason) > 0 && topLevelMemberBefore(data, "rawStopReason", "diagnostics")
	message.modelOmitted = len(raw.Model) == 0
	message.providerThinkingLevelBeforeUsage = len(raw.ProviderThinkingLevel) > 0 && topLevelMemberBefore(data, "providerThinkingLevel", "usage")
	if len(raw.ErrorMessage) > 0 {
		message.errorBeforeTimestamp = topLevelMemberBefore(data, "errorMessage", "timestamp")
		message.errorBeforeResponseID = !message.errorBeforeTimestamp && topLevelMemberBefore(data, "errorMessage", "responseId")
	}
	return nil
}

// topLevelMemberBefore reports whether member first precedes member second in
// the JSON object data, which has been validated.
func topLevelMemberBefore(data []byte, first, second string) bool {
	seenFirst, before := false, false
	jsonwire.EachMember(data, func(name, _ []byte) bool {
		switch string(name) {
		case first:
			seenFirst = true
		case second:
			before = seenFirst
			return false
		}
		return true
	})
	return before
}

// plainRole is the role of the JSON object data when it has one "role"
// member holding an unescaped string, the shape Orb and pi write; anything
// else is left to encoding/json. data is not validated.
func plainRole(data []byte) (string, bool) {
	var role []byte
	roles := 0
	jsonwire.EachMember(data, func(name, value []byte) bool {
		if strings.EqualFold(string(name), "role") {
			roles++
			role = value
		}
		return true
	})
	if roles != 1 || len(role) < 2 || role[0] != '"' || bytes.IndexByte(role, '\\') >= 0 {
		return "", false
	}
	return string(role[1 : len(role)-1]), true
}

// SetAssistantMessageErrorBeforeTimestamp preserves the member order of
// upstream message constructors that insert errorMessage before timestamp.
func SetAssistantMessageErrorBeforeTimestamp(message *AssistantMessage, enabled bool) {
	if message != nil {
		message.errorBeforeTimestamp = enabled
	}
}

// SetAssistantMessageErrorBeforeResponseID preserves the order produced when a
// streaming backend appends errorMessage before responseId to an existing message.
func SetAssistantMessageErrorBeforeResponseID(message *AssistantMessage, enabled bool) {
	if message != nil {
		message.errorBeforeResponseID = enabled
	}
}

func (message ToolResultMessage) MarshalJSON() ([]byte, error) { return marshalWire(message) }

func (message ToolResultMessage) appendWire(dst []byte) ([]byte, error) {
	dst = jsonwire.AppendString(append(dst, `{"role":"toolResult","toolCallId":`...), message.ToolCallID)
	dst = jsonwire.AppendString(append(dst, `,"toolName":`...), message.ToolName)
	dst, err := appendWireBlocks(append(dst, `,"content":`...), message.Content)
	if err == nil && len(message.Details) > 0 {
		dst, err = jsonwire.AppendCompact(append(dst, `,"details":`...), message.Details)
	}
	if err == nil && message.Usage != nil {
		dst, err = message.Usage.appendWire(append(dst, `,"usage":`...))
	}
	if err != nil {
		return nil, err
	}
	if names := message.AddedToolNames; names != nil && *names == nil {
		dst = append(dst, `,"addedToolNames":null`...)
	} else if names != nil {
		dst = append(dst, `,"addedToolNames":[`...)
		for index, name := range *names {
			if index > 0 {
				dst = append(dst, ',')
			}
			dst = jsonwire.AppendString(dst, name)
		}
		dst = append(dst, ']')
	}
	dst = strconv.AppendBool(append(dst, `,"isError":`...), message.IsError)
	dst = strconv.AppendInt(append(dst, `,"timestamp":`...), message.Timestamp, 10)
	if message.NestedCalls != nil {
		nested, err := marshalJSON(message.NestedCalls)
		if err != nil {
			return nil, err
		}
		dst = append(append(dst, `,"nestedCalls":`...), nested...)
	}
	return append(dst, '}'), nil
}
func (message *ToolResultMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		ToolCallID     json.RawMessage   `json:"toolCallId"`
		ToolName       json.RawMessage   `json:"toolName"`
		Content        ToolResultContent `json:"content"`
		Details        json.RawMessage   `json:"details"`
		Usage          *Usage            `json:"usage"`
		AddedToolNames json.RawMessage   `json:"addedToolNames"`
		IsError        bool              `json:"isError"`
		Timestamp      int64             `json:"timestamp"`
		NestedCalls    *NestedToolCalls  `json:"nestedCalls"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	toolCallID, err := unmarshalWireString(raw.ToolCallID)
	if err != nil {
		return err
	}
	toolName, err := unmarshalWireString(raw.ToolName)
	if err != nil {
		return err
	}
	addedToolNames, err := unmarshalOptionalWireStringSlice(raw.AddedToolNames)
	if err != nil {
		return err
	}
	*message = ToolResultMessage{
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Content:        raw.Content,
		Details:        bytes.Clone(raw.Details),
		Usage:          cloneUsage(raw.Usage),
		AddedToolNames: addedToolNames,
		IsError:        raw.IsError,
		Timestamp:      raw.Timestamp,
		NestedCalls:    raw.NestedCalls,
	}
	return nil
}

func cloneUsage(usage *Usage) *Usage {
	if usage == nil {
		return nil
	}
	copy := *usage
	if usage.Reasoning != nil {
		value := *usage.Reasoning
		copy.Reasoning = &value
	}
	if usage.CacheWrite1h != nil {
		value := *usage.CacheWrite1h
		copy.CacheWrite1h = &value
	}
	return &copy
}

func (info DiagnosticErrorInfo) MarshalJSON() ([]byte, error) {
	name, err := marshalOptionalWireString(info.Name)
	if err != nil {
		return nil, err
	}
	message, err := jsonwire.MarshalString(info.Message)
	if err != nil {
		return nil, err
	}
	stack, err := marshalOptionalWireString(info.Stack)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Name    json.RawMessage `json:"name,omitempty"`
		Message json.RawMessage `json:"message"`
		Stack   json.RawMessage `json:"stack,omitempty"`
		Code    json.RawMessage `json:"code,omitempty"`
	}{Name: name, Message: message, Stack: stack, Code: info.Code})
}

func (info *DiagnosticErrorInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name    json.RawMessage `json:"name"`
		Message json.RawMessage `json:"message"`
		Stack   json.RawMessage `json:"stack"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	name, err := unmarshalOptionalWireString(raw.Name)
	if err != nil {
		return err
	}
	message, err := unmarshalWireString(raw.Message)
	if err != nil {
		return err
	}
	stack, err := unmarshalOptionalWireString(raw.Stack)
	if err != nil {
		return err
	}
	*info = DiagnosticErrorInfo{Name: name, Message: message, Stack: stack, Code: bytes.Clone(raw.Code)}
	return nil
}

func (diagnostic AssistantMessageDiagnostic) MarshalJSON() ([]byte, error) {
	typeValue, err := jsonwire.MarshalString(diagnostic.Type)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type      json.RawMessage      `json:"type"`
		Timestamp int64                `json:"timestamp"`
		Error     *DiagnosticErrorInfo `json:"error,omitempty"`
		Details   json.RawMessage      `json:"details,omitempty"`
	}{Type: typeValue, Timestamp: diagnostic.Timestamp, Error: diagnostic.Error, Details: diagnostic.Details})
}

func (diagnostic *AssistantMessageDiagnostic) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type      json.RawMessage      `json:"type"`
		Timestamp int64                `json:"timestamp"`
		Error     *DiagnosticErrorInfo `json:"error"`
		Details   json.RawMessage      `json:"details"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	typeValue, err := unmarshalWireString(raw.Type)
	if err != nil {
		return err
	}
	*diagnostic = AssistantMessageDiagnostic{
		Type:      typeValue,
		Timestamp: raw.Timestamp,
		Error:     raw.Error,
		Details:   bytes.Clone(raw.Details),
	}
	return nil
}

func (content TextContent) MarshalJSON() ([]byte, error) { return content.appendWire(nil) }

func (content TextContent) appendWire(dst []byte) ([]byte, error) {
	dst = jsonwire.AppendString(append(dst, `{"type":"text","text":`...), content.Text)
	dst = appendOptionalString(dst, `,"textSignature":`, content.TextSignature)
	return append(appendOptionalInt(dst, `,"index":`, content.Index), '}'), nil
}
func (content *TextContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Text          json.RawMessage `json:"text"`
		TextSignature json.RawMessage `json:"textSignature"`
		Index         *int            `json:"index"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	text, err := unmarshalWireString(raw.Text)
	if err != nil {
		return err
	}
	signature, err := unmarshalOptionalWireString(raw.TextSignature)
	if err != nil {
		return err
	}
	*content = TextContent{Text: text, TextSignature: signature, Index: raw.Index}
	return nil
}

func (content ThinkingContent) MarshalJSON() ([]byte, error) { return content.appendWire(nil) }

func (content ThinkingContent) appendWire(dst []byte) ([]byte, error) {
	dst = jsonwire.AppendString(append(dst, `{"type":"thinking","thinking":`...), content.Thinking)
	dst = appendOptionalString(dst, `,"thinkingSignature":`, content.ThinkingSignature)
	if len(content.RedactedChunks) == 0 {
		dst = appendOptionalBool(dst, `,"redacted":`, content.Redacted)
		return append(appendOptionalInt(dst, `,"index":`, content.Index), '}'), nil
	}
	dst = appendOptionalInt(dst, `,"index":`, content.Index)
	dst = append(appendOptionalBool(dst, `,"redacted":`, content.Redacted), `,"redactedChunks":[`...)
	for index, chunk := range content.RedactedChunks {
		if index > 0 {
			dst = append(dst, ',')
		}
		var err error
		if dst, err = jsonwire.AppendCompact(dst, chunk); err != nil {
			return nil, err
		}
	}
	return append(dst, ']', '}'), nil
}
func (content *ThinkingContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		RedactedChunks    []json.RawMessage `json:"redactedChunks"`
		Thinking          json.RawMessage   `json:"thinking"`
		ThinkingSignature json.RawMessage   `json:"thinkingSignature"`
		Redacted          *bool             `json:"redacted"`
		Index             *int              `json:"index"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	thinking, err := unmarshalWireString(raw.Thinking)
	if err != nil {
		return err
	}
	signature, err := unmarshalOptionalWireString(raw.ThinkingSignature)
	if err != nil {
		return err
	}
	*content = ThinkingContent{RedactedChunks: raw.RedactedChunks, Thinking: thinking, ThinkingSignature: signature, Redacted: raw.Redacted, Index: raw.Index}
	return nil
}

func (content ImageContent) MarshalJSON() ([]byte, error) { return content.appendWire(nil) }

func (content ImageContent) appendWire(dst []byte) ([]byte, error) {
	dst = jsonwire.AppendString(append(dst, `{"type":"image","data":`...), content.Data)
	return append(jsonwire.AppendString(append(dst, `,"mimeType":`...), content.MimeType), '}'), nil
}
func (content *ImageContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Data     json.RawMessage `json:"data"`
		MimeType json.RawMessage `json:"mimeType"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	imageData, err := unmarshalWireString(raw.Data)
	if err != nil {
		return err
	}
	mimeType, err := unmarshalWireString(raw.MimeType)
	if err != nil {
		return err
	}
	*content = ImageContent{Data: imageData, MimeType: mimeType}
	return nil
}

func (content UnknownContentBlock) MarshalJSON() ([]byte, error) {
	if len(bytes.TrimSpace(content.Raw)) == 0 {
		return []byte("null"), nil
	}
	return NormalizeJSONStringifyJSON(content.Raw)
}

func (content ToolCall) MarshalJSON() ([]byte, error) { return content.appendWire(nil) }

func (content ToolCall) appendWire(dst []byte) ([]byte, error) {
	arguments, stringified, err := toolCallArguments(&content)
	if err != nil {
		return nil, err
	}
	dst = jsonwire.AppendString(append(dst, `{"type":"toolCall","id":`...), content.ID)
	dst = jsonwire.AppendString(append(dst, `,"name":`...), content.Name)
	if stringified {
		dst = append(append(dst, `,"arguments":`...), arguments...)
	} else if dst, err = jsonwire.AppendCompact(append(dst, `,"arguments":`...), arguments); err != nil {
		return nil, err
	}
	// The streaming members are set together or not at all.
	dst = appendOptionalString(dst, `,"namespace":`, content.Namespace)
	dst = appendOptionalString(dst, `,"partialJson":`, content.PartialJSON)
	dst = appendOptionalString(dst, `,"partialArgs":`, content.PartialArgs)
	dst = appendOptionalInt(dst, `,"streamIndex":`, content.StreamIndex)
	dst = appendOptionalInt(dst, `,"index":`, content.Index)
	return append(appendOptionalString(dst, `,"thoughtSignature":`, content.ThoughtSignature), '}'), nil
}
func (content *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               json.RawMessage `json:"id"`
		Name             json.RawMessage `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		Namespace        json.RawMessage `json:"namespace"`
		ThoughtSignature json.RawMessage `json:"thoughtSignature"`
		PartialJSON      json.RawMessage `json:"partialJson"`
		PartialArgs      json.RawMessage `json:"partialArgs"`
		StreamIndex      *int            `json:"streamIndex,omitempty"`
		Index            *int            `json:"index,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := unmarshalWireString(raw.ID)
	if err != nil {
		return err
	}
	name, err := unmarshalWireString(raw.Name)
	if err != nil {
		return err
	}
	thoughtSignature, err := unmarshalOptionalWireString(raw.ThoughtSignature)
	if err != nil {
		return err
	}
	partialJSON, err := unmarshalOptionalWireString(raw.PartialJSON)
	if err != nil {
		return err
	}
	partialArgs, err := unmarshalOptionalWireString(raw.PartialArgs)
	if err != nil {
		return err
	}
	namespace, err := unmarshalOptionalWireString(raw.Namespace)
	if err != nil {
		return err
	}
	*content = ToolCall{
		ID:               id,
		Name:             name,
		ThoughtSignature: thoughtSignature,
		Namespace:        namespace,
		PartialJSON:      partialJSON,
		PartialArgs:      partialArgs,
		StreamIndex:      raw.StreamIndex,
		Index:            raw.Index,
	}
	if err := SetToolCallArgumentsJSON(content, raw.Arguments); err != nil {
		return fmt.Errorf("tool call arguments: %w", err)
	}
	return nil
}

// SetToolCallArgumentsJSON records a complete provider-emitted argument value
// so a later replay preserves JSON.stringify's original shape and member order.
// ToolCall.Arguments remains an object-oriented Go convenience; malformed
// provider values are retained in the wire representation and exposed through
// ToolCallArgumentsValue.
func SetToolCallArgumentsJSON(content *ToolCall, data []byte) error {
	if content == nil {
		return errors.New("ai: nil tool call")
	}
	normalizedArguments, err := NormalizeJSONStringifyJSON(data)
	if err != nil {
		return err
	}
	return content.setNormalizedArguments(normalizedArguments)
}

// SetToolCallPartialJSON parses a streamed argument prefix, retaining the same
// ordered wire representation as SetToolCallArgumentsJSON without normalizing twice.
func SetToolCallPartialJSON(content *ToolCall, partial string) error {
	if content == nil {
		return errors.New("ai: nil tool call")
	}
	encoded, err := partialjson.StringifyStreamingJSON(partial)
	if err != nil {
		return err
	}
	return content.setNormalizedArguments(encoded)
}

func (content *ToolCall) setNormalizedArguments(normalizedArguments []byte) error {
	value, err := decodeJSONValue(normalizedArguments)
	if err != nil {
		return err
	}
	arguments, ok := copyJSONContainers(value).(map[string]any)
	if !ok {
		arguments = map[string]any{}
	}
	content.Arguments = arguments
	content.rawArguments = normalizedArguments
	content.rawValue = value
	return nil
}

// copyJSONContainers copies a decoded JSON value's objects and arrays; the
// immutable scalars are shared.
func copyJSONContainers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, item := range typed {
			copied[key] = copyJSONContainers(item)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for index, item := range typed {
			copied[index] = copyJSONContainers(item)
		}
		return copied
	}
	return value
}

// jsonValuesEqual is reflect.DeepEqual for decoded JSON values, without the
// allocation DeepEqual makes on every call.
func jsonValuesEqual(left, right any) bool {
	switch typed := left.(type) {
	case map[string]any:
		other, ok := right.(map[string]any)
		if !ok || (typed == nil) != (other == nil) || len(typed) != len(other) {
			return false
		}
		for key, item := range typed {
			if value, exists := other[key]; !exists || !jsonValuesEqual(item, value) {
				return false
			}
		}
		return true
	case []any:
		other, ok := right.([]any)
		if !ok || (typed == nil) != (other == nil) || len(typed) != len(other) {
			return false
		}
		for index := range typed {
			if !jsonValuesEqual(typed[index], other[index]) {
				return false
			}
		}
		return true
	case string:
		other, ok := right.(string)
		return ok && typed == other
	case float64:
		other, ok := right.(float64)
		return ok && typed == other
	case bool:
		other, ok := right.(bool)
		return ok && typed == other
	case nil:
		return right == nil
	}
	return reflect.DeepEqual(left, right)
}

// originalArguments is the provider-emitted argument value, when known.
func (content *ToolCall) originalArguments() (any, bool) {
	if len(content.rawArguments) == 0 {
		return nil, false
	}
	if content.rawValue != nil {
		return content.rawValue, true
	}
	original, err := decodeJSONValue(content.rawArguments)
	return original, err == nil
}

// ToolCallArgumentsValue returns the provider-emitted JSON value. Valid tool
// calls return the public argument map; malformed non-object values remain
// observable so schema validation and transforms see the same runtime value as
// upstream.
func ToolCallArgumentsValue(content *ToolCall) any {
	if content == nil {
		return nil
	}
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if original, ok := content.originalArguments(); ok {
		if object, ok := original.(map[string]any); ok {
			if jsonValuesEqual(object, arguments) {
				return arguments
			}
		} else if len(arguments) == 0 {
			return original
		}
	}
	return arguments
}

// MarshalToolCallArguments preserves the provider's decoded JSON shape and
// object member order while the public argument map remains convenient for
// ordinary object-shaped tool calls.
func MarshalToolCallArguments(content *ToolCall) ([]byte, error) {
	if content == nil {
		return nil, errors.New("ai: nil tool call")
	}
	arguments, stringified, err := toolCallArguments(content)
	if stringified {
		// The provider-emitted form belongs to the tool call.
		arguments = bytes.Clone(arguments)
	}
	return arguments, err
}

// toolCallArguments encodes a tool call's arguments; stringified reports
// output already in JSON.stringify's shape, which compacting leaves as it is.
// The provider-emitted form is returned without a copy.
func toolCallArguments(content *ToolCall) (encoded []byte, stringified bool, err error) {
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if original, ok := content.originalArguments(); ok {
		if object, ok := original.(map[string]any); ok {
			if jsonValuesEqual(object, arguments) {
				return content.rawArguments, true, nil
			}
		} else if len(arguments) == 0 {
			return content.rawArguments, true, nil
		}
	}
	for _, partial := range []*string{content.PartialJSON, content.PartialArgs} {
		if partial == nil {
			continue
		}
		if encoded, err := partialjson.StringifyStreamingJSON(*partial); err == nil {
			return encoded, true, nil
		}
	}
	encoded, err = marshalJSON(stringifyJSONObject(arguments))
	return encoded, false, err
}

func (content UserContent) MarshalJSON() ([]byte, error) { return content.appendWire(nil) }

func (content UserContent) appendWire(dst []byte) ([]byte, error) {
	if content.Text != nil {
		if content.Blocks != nil {
			return nil, errors.New("ai: user content has both text and blocks")
		}
		return jsonwire.AppendString(dst, *content.Text), nil
	}
	return appendWireBlocks(dst, content.Blocks)
}

// wireBuffers lends scratch buffers to the message encoders, which return an
// exact copy: grown from nothing, an encoding allocated about twice its size.
var wireBuffers = sync.Pool{New: func() any { return new([]byte) }}

func marshalWire[T wireAppender](value T) ([]byte, error) {
	buffer := wireBuffers.Get().(*[]byte)
	encoded, err := value.appendWire((*buffer)[:0])
	if err != nil {
		wireBuffers.Put(buffer)
		return nil, err
	}
	result := bytes.Clone(encoded)
	if cap(encoded) <= 1<<20 {
		*buffer = encoded
		wireBuffers.Put(buffer)
	}
	return result, nil
}

// wireAppender is implemented by the message and content types, which append
// their compact wire JSON: encoding/json made every nesting level validate
// its children's encoding again.
type wireAppender interface {
	appendWire(dst []byte) ([]byte, error)
}

// appendWireBlocks appends a content block list; nil encodes as [] like an
// empty list.
func appendWireBlocks[T any](dst []byte, blocks []T) ([]byte, error) {
	dst = append(dst, '[')
	for index, block := range blocks {
		if index > 0 {
			dst = append(dst, ',')
		}
		var err error
		if appender, ok := any(block).(wireAppender); ok && !reflect.ValueOf(appender).IsNil() {
			dst, err = appender.appendWire(dst)
		} else {
			var encoded []byte
			encoded, err = marshalJSON(block)
			dst = append(dst, encoded...)
		}
		if err != nil {
			return nil, err
		}
	}
	return append(dst, ']'), nil
}

func appendOptionalString(dst []byte, member string, value *string) []byte {
	if value == nil {
		return dst
	}
	return jsonwire.AppendString(append(dst, member...), *value)
}

func appendOptionalInt[T int | int64](dst []byte, member string, value *T) []byte {
	if value == nil {
		return dst
	}
	return strconv.AppendInt(append(dst, member...), int64(*value), 10)
}

func appendOptionalBool(dst []byte, member string, value *bool) []byte {
	if value == nil {
		return dst
	}
	return strconv.AppendBool(append(dst, member...), *value)
}
func (content *UserContent) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		text, err := jsonwire.UnmarshalString(data)
		if err != nil {
			return err
		}
		content.Text = &text
		content.Blocks = nil
		return nil
	}
	var blocks UserContentBlocks
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	content.Text = nil
	content.Blocks = blocks
	return nil
}

func unmarshalWireString(data json.RawMessage) (string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return "", nil
	}
	return jsonwire.UnmarshalString(data)
}

func marshalOptionalWireString(value *string) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := jsonwire.MarshalString(*value)
	return json.RawMessage(encoded), err
}

func unmarshalOptionalWireStringSlice(data json.RawMessage) (*[]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	values := make([]string, len(raw))
	for index, item := range raw {
		value, err := unmarshalWireString(item)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return &values, nil
}

func unmarshalOptionalWireString(data json.RawMessage) (*string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	value, err := jsonwire.UnmarshalString(data)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (blocks UserContentBlocks) MarshalJSON() ([]byte, error) { return appendWireBlocks(nil, blocks) }

func (blocks AssistantContent) MarshalJSON() ([]byte, error) { return appendWireBlocks(nil, blocks) }

func (blocks ToolResultContent) MarshalJSON() ([]byte, error) { return appendWireBlocks(nil, blocks) }

func (blocks ImagesContent) MarshalJSON() ([]byte, error) { return appendWireBlocks(nil, blocks) }

var textImageBlockFactories = map[string]func() any{
	"text":  func() any { return &TextContent{} },
	"image": func() any { return &ImageContent{} },
}

var assistantBlockFactories = map[string]func() any{
	"text":     func() any { return &TextContent{} },
	"thinking": func() any { return &ThinkingContent{} },
	"toolCall": func() any { return &ToolCall{} },
}

func (blocks *UserContentBlocks) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[UserContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *AssistantContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[AssistantContentBlock](data, assistantBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *ToolResultContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[ToolResultContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *ImagesContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[ImagesContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (messages MessageList) MarshalJSON() ([]byte, error) {
	if messages == nil {
		return []byte("[]"), nil
	}
	type messageList MessageList
	return marshalJSON(messageList(messages))
}

func (messages *MessageList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decoded := make(MessageList, 0, len(raw))
	for index, item := range raw {
		message, err := UnmarshalMessage(item)
		if err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		decoded = append(decoded, message)
	}
	*messages = decoded
	return nil
}

func unmarshalBlocks(data []byte, factories map[string]func() any) ([]any, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	decoded := make([]any, 0, len(raw))
	for index, item := range raw {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, fmt.Errorf("content %d type: %w", index, err)
		}
		factory := factories[header.Type]
		if factory == nil {
			normalized, err := NormalizeJSONStringifyJSON(item)
			if err != nil {
				return nil, fmt.Errorf("content %d unknown: %w", index, err)
			}
			decoded = append(decoded, &UnknownContentBlock{Raw: normalized})
			continue
		}
		value := factory()
		if err := json.Unmarshal(item, value); err != nil {
			return nil, fmt.Errorf("content %d %s: %w", index, header.Type, err)
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

func unmarshalTypedBlocks[T any](data []byte, factories map[string]func() any) ([]T, error) {
	decoded, err := unmarshalBlocks(data, factories)
	if err != nil {
		return nil, err
	}
	result := make([]T, 0, len(decoded))
	for _, block := range decoded {
		if value, ok := block.(T); ok {
			result = append(result, value)
		}
	}
	return result, nil
}

func decodeJSONValue(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, errors.New("missing JSON value")
	}
	if value, ok := jsonwire.Decode(data); ok {
		return value, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

// NormalizeJSONStringifyJSON parses JSON with JavaScript Number semantics and
// re-emits the same value using JSON.stringify's ordering and scalar spelling.
func NormalizeJSONStringifyJSON(data []byte) ([]byte, error) {
	if jsonwire.Stringified(data) {
		return bytes.Clone(data), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	source := jsonStringifyDecoder{decoder: decoder, data: data}
	var output bytes.Buffer
	output.Grow(len(data))
	if err := writeJSONStringifyJSONValue(&output, &source); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return output.Bytes(), nil
}

type jsonStringifyDecoder struct {
	decoder *json.Decoder
	data    []byte
}

func (source *jsonStringifyDecoder) token() (json.Token, error) {
	start := source.decoder.InputOffset()
	token, err := source.decoder.Token()
	if err != nil {
		return nil, err
	}
	if _, ok := token.(string); !ok {
		return token, nil
	}
	end := source.decoder.InputOffset()
	value, err := jsonwire.UnmarshalStringToken(source.data[start:end])
	if err != nil {
		return nil, err
	}
	return value, nil
}

func writeJSONStringifyJSONValue(output *bytes.Buffer, source *jsonStringifyDecoder) error {
	token, err := source.token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			type member struct {
				name  string
				value []byte
			}
			members := make([]member, 0)
			indexes := make(map[string]int)
			for source.decoder.More() {
				key, err := source.token()
				if err != nil {
					return err
				}
				_, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				name := key.(string)
				var value bytes.Buffer
				if err := writeJSONStringifyJSONValue(&value, source); err != nil {
					return err
				}
				if index, exists := indexes[name]; exists {
					members[index].value = value.Bytes()
				} else {
					indexes[name] = len(members)
					members = append(members, member{name: name, value: value.Bytes()})
				}
			}
			closing, err := source.token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("object is not closed")
			}
			sort.SliceStable(members, func(left, right int) bool {
				leftIndex, leftIsIndex := jsArrayIndex(members[left].name)
				rightIndex, rightIsIndex := jsArrayIndex(members[right].name)
				if leftIsIndex && rightIsIndex {
					return leftIndex < rightIndex
				}
				return leftIsIndex && !rightIsIndex
			})
			output.WriteByte('{')
			for index, member := range members {
				if index > 0 {
					output.WriteByte(',')
				}
				encodedName, err := jsonwire.MarshalString(member.name)
				if err != nil {
					return err
				}
				output.Write(encodedName)
				output.WriteByte(':')
				output.Write(member.value)
			}
			output.WriteByte('}')
			return nil
		case '[':
			output.WriteByte('[')
			for index := 0; source.decoder.More(); index++ {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := writeJSONStringifyJSONValue(output, source); err != nil {
					return err
				}
			}
			closing, err := source.token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("array is not closed")
			}
			output.WriteByte(']')
			return nil
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}

	if number, ok := token.(json.Number); ok {
		value, err := strconv.ParseFloat(number.String(), 64)
		if err != nil && !math.IsInf(value, 0) {
			return err
		}
		if math.IsInf(value, 0) || math.IsNaN(value) {
			output.WriteString("null")
			return nil
		}
		if value == 0 {
			output.WriteByte('0')
			return nil
		}
		encoded, err := marshalJSON(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	}
	if value, ok := token.(string); ok {
		encoded, err := jsonwire.MarshalString(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	}
	encoded, err := marshalJSON(token)
	if err != nil {
		return err
	}
	output.Write(encoded)
	return nil
}

func jsArrayIndex(name string) (uint64, bool) {
	if name == "0" {
		return 0, true
	}
	if name == "" || name[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseUint(name, 10, 32)
	if err != nil || value == math.MaxUint32 || strconv.FormatUint(value, 10) != name {
		return 0, false
	}
	return value, true
}

func marshalRequiredSlice[T any](values []T) ([]byte, error) {
	if values == nil {
		return []byte("[]"), nil
	}
	type slice []T
	return marshalJSON(slice(values))
}

func marshalJSON(value any) ([]byte, error) {
	return jsonwire.Marshal(value)
}

func stringifyJSONObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = stringifyJSONValue(item)
	}
	return result
}

func stringifyJSONValue(value any) any {
	switch value := value.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
		if value == 0 {
			return float64(0)
		}
		return value
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil
		}
		if value == 0 {
			return float32(0)
		}
		return value
	case map[string]any:
		return stringifyJSONObject(value)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = stringifyJSONValue(item)
		}
		return result
	default:
		return value
	}
}

// SetAssistantMessageProviderThinkingLevelBeforeUsage preserves constructor field order for managed Anthropic responses.
func SetAssistantMessageProviderThinkingLevelBeforeUsage(message *AssistantMessage, before bool) {
	message.providerThinkingLevelBeforeUsage = before
}

// SetAssistantMessageRawStopBeforeDiagnostics preserves diagnostics appended after stream termination.
func SetAssistantMessageRawStopBeforeDiagnostics(message *AssistantMessage, before bool) {
	message.rawStopBeforeDiagnostics = before
}
