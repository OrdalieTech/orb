package host

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

type wireAutocompleteMount struct {
	FactoryHandle  string                    `json:"factoryHandle"`
	ProviderHandle string                    `json:"providerHandle"`
	Current        *wireAutocompleteProvider `json:"current,omitempty"`
}

type wireAutocompleteMountResult struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type wireAutocompleteCall struct {
	ProviderHandle string                `json:"providerHandle"`
	Operation      string                `json:"operation"`
	Lines          []string              `json:"lines,omitempty"`
	CursorLine     int                   `json:"cursorLine,omitempty"`
	CursorCol      int                   `json:"cursorCol,omitempty"`
	Force          bool                  `json:"force,omitempty"`
	Item           *wireAutocompleteItem `json:"item,omitempty"`
	Prefix         string                `json:"prefix,omitempty"`
}

type wireAutocompleteItem struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type wireAutocompleteResult struct {
	Present    bool                   `json:"present,omitempty"`
	Prefix     string                 `json:"prefix,omitempty"`
	Items      []wireAutocompleteItem `json:"items,omitempty"`
	Lines      []string               `json:"lines,omitempty"`
	CursorLine int                    `json:"cursorLine,omitempty"`
	CursorCol  int                    `json:"cursorCol,omitempty"`
	Triggered  bool                   `json:"triggered,omitempty"`
}

type hostAutocompleteProvider struct {
	generation        *generation
	handle            string
	triggerCharacters []string
}

func (manager *Manager) addUIAutocompleteProvider(generation *generation, ui extensions.UI, request wireUIRequest) {
	if request.FactoryHandle == "" {
		return
	}
	ui.AddAutocompleteProvider(func(current extensions.AutocompleteProvider) extensions.AutocompleteProvider {
		handle := "autocomplete-" + request.FactoryHandle
		ctx, cancel := generation.manager.timeoutContext(context.Background())
		defer cancel()
		raw, err := generation.request(ctx, "ui_autocomplete", wireAutocompleteMount{
			FactoryHandle:  request.FactoryHandle,
			ProviderHandle: handle,
			Current:        snapshotAutocompleteProvider(current),
		}, nil)
		if err != nil {
			return current
		}
		var mounted wireAutocompleteMountResult
		if json.Unmarshal(raw, &mounted) != nil {
			return current
		}
		return &hostAutocompleteProvider{
			generation: generation, handle: handle,
			triggerCharacters: append([]string(nil), mounted.TriggerCharacters...),
		}
	})
}

func (provider *hostAutocompleteProvider) TriggerCharacters() []string {
	return append([]string(nil), provider.triggerCharacters...)
}

func (provider *hostAutocompleteProvider) GetSuggestions(
	ctx context.Context,
	request extensions.AutocompleteRequest,
) (*extensions.AutocompleteResult, error) {
	result, err := provider.call(ctx, wireAutocompleteCall{
		ProviderHandle: provider.handle, Operation: "getSuggestions",
		Lines: append([]string(nil), request.Lines...), CursorLine: request.CursorLine,
		CursorCol: request.CursorCol, Force: request.Force,
	})
	if err != nil || !result.Present {
		return nil, err
	}
	items := make([]extensions.AutocompleteItem, len(result.Items))
	for index, item := range result.Items {
		items[index] = extensions.AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}
	}
	return &extensions.AutocompleteResult{Prefix: result.Prefix, Items: items}, nil
}

func (provider *hostAutocompleteProvider) ApplyCompletion(
	request extensions.AutocompleteRequest,
	item extensions.AutocompleteItem,
	prefix string,
) ([]string, int, int) {
	result, err := provider.call(context.Background(), wireAutocompleteCall{
		ProviderHandle: provider.handle, Operation: "applyCompletion",
		Lines: append([]string(nil), request.Lines...), CursorLine: request.CursorLine,
		CursorCol: request.CursorCol, Prefix: prefix,
		Item: &wireAutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description},
	})
	if err != nil {
		return request.Lines, request.CursorLine, request.CursorCol
	}
	return result.Lines, result.CursorLine, result.CursorCol
}

func (provider *hostAutocompleteProvider) ShouldTriggerFileCompletion(request extensions.AutocompleteRequest) bool {
	result, err := provider.call(context.Background(), wireAutocompleteCall{
		ProviderHandle: provider.handle, Operation: "shouldTriggerFileCompletion",
		Lines: append([]string(nil), request.Lines...), CursorLine: request.CursorLine, CursorCol: request.CursorCol,
	})
	return err == nil && result.Triggered
}

func (provider *hostAutocompleteProvider) call(ctx context.Context, call wireAutocompleteCall) (wireAutocompleteResult, error) {
	requestContext, cancel := provider.generation.manager.timeoutContext(ctx)
	defer cancel()
	raw, err := provider.generation.request(requestContext, "ui_autocomplete", call, nil)
	if err != nil {
		return wireAutocompleteResult{}, err
	}
	var result wireAutocompleteResult
	err = json.Unmarshal(raw, &result)
	return result, err
}
