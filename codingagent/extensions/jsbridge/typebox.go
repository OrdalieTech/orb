package jsbridge

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/ai"
	aiapi "github.com/OrdalieTech/pi-go/ai/api"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
	"github.com/OrdalieTech/pi-go/tui"
	"github.com/grafana/sobek"
)

//go:embed third_party/typebox/typebox-1.1.38.js
var typeboxSource string

var (
	typeboxCompileOnce sync.Once
	typeboxProgram     *sobek.Program
	typeboxCompileErr  error
	builtinCatalogOnce sync.Once
	builtinCatalog     *aimodels.Catalog
	builtinCatalogErr  error
)

func compiledTypebox() (*sobek.Program, error) {
	typeboxCompileOnce.Do(func() {
		typeboxProgram, typeboxCompileErr = sobek.Compile("typebox-1.1.38.js", typeboxSource, false)
	})
	return typeboxProgram, typeboxCompileErr
}

func builtins() (*aimodels.Catalog, error) {
	builtinCatalogOnce.Do(func() { builtinCatalog, builtinCatalogErr = aimodels.Builtin() })
	return builtinCatalog, builtinCatalogErr
}

func installRequire(runtime *sobek.Runtime, vm *runtimeVM) error {
	if err := runtime.Set("__piGoEncodeUTF8", func(call sobek.FunctionCall) sobek.Value {
		buffer := runtime.NewArrayBuffer([]byte(call.Argument(0).String()))
		return runtime.ToValue(buffer)
	}); err != nil {
		return err
	}
	if err := runtime.Set("__piGoParseURL", func(call sobek.FunctionCall) sobek.Value {
		raw := call.Argument(0).String()
		parsed, err := url.Parse(raw)
		if err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		if baseValue := call.Argument(1); present(baseValue) && baseValue.String() != "" {
			base, baseErr := url.Parse(baseValue.String())
			if baseErr != nil {
				panic(runtime.NewTypeError(baseErr.Error()))
			}
			parsed = base.ResolveReference(parsed)
		} else if parsed.Scheme == "" {
			panic(runtime.NewTypeError("Invalid URL"))
		}
		if parsed.Host != "" && parsed.Path == "" {
			parsed.Path = "/"
		}
		pathname := parsed.EscapedPath()
		if pathname == "" && parsed.Host != "" {
			pathname = "/"
		}
		hash, search, protocol, origin := "", "", "", "null"
		if parsed.Fragment != "" {
			hash = "#" + parsed.EscapedFragment()
		}
		if parsed.RawQuery != "" {
			search = "?" + parsed.RawQuery
		}
		if parsed.Scheme != "" {
			protocol = parsed.Scheme + ":"
		}
		if parsed.Scheme != "" && parsed.Host != "" {
			origin = parsed.Scheme + "://" + parsed.Host
		}
		return toJS(runtime, map[string]any{
			"href": parsed.String(), "hash": hash, "pathname": pathname,
			"protocol": protocol, "host": parsed.Host, "hostname": parsed.Hostname(),
			"port": parsed.Port(), "search": search, "origin": origin,
		})
	}); err != nil {
		return err
	}
	if _, err := runtime.RunString(`
globalThis.TextEncoder ??= function TextEncoder() {};
globalThis.TextEncoder.prototype.encode = function(value = "") {
  return new Uint8Array(__piGoEncodeUTF8(String(value)));
};
globalThis.URL ??= function URL(value, base) {
  const baseValue = base && typeof base === "object" && "href" in base ? base.href : base;
  Object.assign(this, __piGoParseURL(String(value), baseValue === undefined ? undefined : String(baseValue)));
};
globalThis.URL.prototype.toString = function() { return this.href; };
`); err != nil {
		return fmt.Errorf("install typebox globals: %w", err)
	}
	program, err := compiledTypebox()
	if err != nil {
		return fmt.Errorf("compile embedded typebox 1.1.38: %w", err)
	}
	if _, err := runtime.RunProgram(program); err != nil {
		return fmt.Errorf("load embedded typebox 1.1.38: %w", err)
	}
	typebox := runtime.Get("__piGoTypebox")
	typeboxCompile := runtime.Get("__piGoTypeboxCompile")
	typeboxValue := runtime.Get("__piGoTypeboxValue")
	for name, module := range map[string]sobek.Value{
		"root": typebox, "compile": typeboxCompile, "value": typeboxValue,
	} {
		if module == nil || sobek.IsUndefined(module) || sobek.IsNull(module) {
			return fmt.Errorf("embedded typebox 1.1.38 did not export its %s module", name)
		}
	}

	codingModule := runtime.NewObject()
	if err := codingModule.Set("defineTool", func(call sobek.FunctionCall) sobek.Value {
		return call.Argument(0)
	}); err != nil {
		return err
	}
	customEditorClass, err := installCustomEditorBase(runtime, vm)
	if err != nil {
		return err
	}
	if err := codingModule.Set("CustomEditor", customEditorClass); err != nil {
		return err
	}
	aiModule := runtime.NewObject()
	if err := aiModule.Set("Type", typebox.ToObject(runtime).Get("Type")); err != nil {
		return err
	}
	if err := aiModule.Set("StringEnum", func(call sobek.FunctionCall) sobek.Value {
		typeObject := typebox.ToObject(runtime).Get("Type").ToObject(runtime)
		unsafe, ok := sobek.AssertFunction(typeObject.Get("Unsafe"))
		if !ok {
			panic(runtime.NewTypeError("typebox Type.Unsafe is unavailable"))
		}
		schema := runtime.NewObject()
		if err := schema.Set("type", "string"); err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		if err := schema.Set("enum", call.Argument(0)); err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		if options := call.Argument(1); present(options) {
			optionsObject := options.ToObject(runtime)
			if description := optionsObject.Get("description"); description.ToBoolean() {
				must(runtime, schema.Set("description", description))
			}
			if defaultValue := optionsObject.Get("default"); defaultValue.ToBoolean() {
				must(runtime, schema.Set("default", defaultValue))
			}
		}
		value, err := unsafe(typeObject, schema)
		if err != nil {
			panic(err)
		}
		return value
	}); err != nil {
		return err
	}
	compatModule := runtime.NewObject()
	if err := compatModule.Set("getModel", func(call sobek.FunctionCall) sobek.Value {
		catalog, err := builtins()
		if err != nil {
			panic(runtime.NewGoError(err))
		}
		model, ok := catalog.Find(call.Argument(0).String(), call.Argument(1).String())
		if !ok {
			return sobek.Undefined()
		}
		return toJS(runtime, model)
	}); err != nil {
		return err
	}
	complete := func(call sobek.FunctionCall) sobek.Value {
		var model ai.Model
		if err := decodeJSON(runtime, call.Argument(0), &model); err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		var requestContext ai.Context
		if err := decodeJSON(runtime, call.Argument(1), &requestContext); err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		var options *ai.SimpleStreamOptions
		if optionValue := call.Argument(2); present(optionValue) {
			options = &ai.SimpleStreamOptions{}
			if err := decodeJSON(runtime, optionValue, options); err != nil {
				panic(runtime.NewTypeError(err.Error()))
			}
			if effort := optionValue.ToObject(runtime).Get("reasoningEffort"); present(effort) {
				level := ai.ThinkingLevel(effort.String())
				options.Reasoning = &level
			}
		}
		return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
			stream, err := aiapi.StreamSimple(ctx, &model, requestContext, options)
			if err != nil {
				return nil, err
			}
			return ai.Collect(stream)
		})
	}
	if err := compatModule.Set("complete", complete); err != nil {
		return err
	}
	if err := compatModule.Set("completeSimple", complete); err != nil {
		return err
	}

	// pi-tui helper functions used by upstream single-file examples; component
	// classes (Text, Container, Markdown) are the WP-542 custom-UI surface.
	tuiModule := runtime.NewObject()
	if err := tuiModule.Set("visibleWidth", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(tui.VisibleWidth(call.Argument(0).String()))
	}); err != nil {
		return err
	}
	if err := tuiModule.Set("truncateToWidth", func(call sobek.FunctionCall) sobek.Value {
		ellipsis := "..."
		if present(call.Argument(2)) {
			ellipsis = call.Argument(2).String()
		}
		pad := false
		if present(call.Argument(3)) {
			pad = call.Argument(3).ToBoolean()
		}
		return runtime.ToValue(tui.TruncateToWidth(call.Argument(0).String(), int(call.Argument(1).ToInteger()), ellipsis, pad))
	}); err != nil {
		return err
	}
	if err := tuiModule.Set("matchesKey", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(tui.MatchesKey(call.Argument(0).String(), tui.KeyID(call.Argument(1).String())))
	}); err != nil {
		return err
	}
	if err := tuiModule.Set("fuzzyFilter", func(call sobek.FunctionCall) sobek.Value {
		items := call.Argument(0).ToObject(runtime)
		query := call.Argument(1).String()
		getText, ok := sobek.AssertFunction(call.Argument(2))
		if !ok {
			panic(runtime.NewTypeError("fuzzyFilter requires a getText function"))
		}
		length := int(items.Get("length").ToInteger())
		values := make([]sobek.Value, 0, length)
		for index := range length {
			values = append(values, items.Get(strconv.Itoa(index)))
		}
		filtered := tui.FuzzyFilter(values, query, func(value sobek.Value) string {
			text, err := getText(sobek.Undefined(), value)
			if err != nil {
				panic(err)
			}
			return text.String()
		})
		array := make([]any, len(filtered))
		for index, value := range filtered {
			array[index] = value
		}
		return runtime.NewArray(array...)
	}); err != nil {
		return err
	}
	emptyModule := runtime.NewObject()
	return runtime.Set("require", func(call sobek.FunctionCall) sobek.Value {
		specifier := call.Argument(0).String()
		switch {
		case specifier == "pi",
			specifier == "@earendil-works/pi-coding-agent",
			specifier == "@mariozechner/pi-coding-agent":
			return codingModule
		case specifier == "typebox",
			specifier == "@sinclair/typebox":
			return typebox
		case specifier == "typebox/compile", specifier == "@sinclair/typebox/compile":
			return typeboxCompile
		case specifier == "typebox/value", specifier == "@sinclair/typebox/value":
			return typeboxValue
		case strings.HasPrefix(specifier, "typebox/"), strings.HasPrefix(specifier, "@sinclair/typebox/"):
			panic(runtime.NewTypeError("unsupported TypeBox 1.1.38 module %q", specifier))
		case specifier == "@earendil-works/pi-ai",
			specifier == "@mariozechner/pi-ai":
			return aiModule
		case specifier == "@earendil-works/pi-ai/compat",
			specifier == "@mariozechner/pi-ai/compat":
			return compatModule
		case specifier == "@earendil-works/pi-tui",
			specifier == "@mariozechner/pi-tui":
			return tuiModule
		case strings.HasPrefix(specifier, "@earendil-works/pi-"),
			strings.HasPrefix(specifier, "@mariozechner/pi-"):
			return emptyModule
		default:
			panic(runtime.NewTypeError("unsupported external module %q", specifier))
		}
	})
}
