package jsbridge

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	aiapi "github.com/OrdalieTech/pi-go/ai/api"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
	"github.com/OrdalieTech/pi-go/internal/truncate"
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
// Node defines Symbol.asyncIterator (ES2018); esbuild's __forAwait helper
// falls back to Symbol.for("Symbol.asyncIterator") when it is missing.
if (!Symbol.asyncIterator) { Symbol.asyncIterator = Symbol.for("Symbol.asyncIterator"); }
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
			if description := optionsObject.Get("description"); present(description) && description.ToBoolean() {
				must(runtime, schema.Set("description", description))
			}
			if defaultValue := optionsObject.Get("default"); present(defaultValue) && defaultValue.ToBoolean() {
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
	if err := installExampleHelpers(runtime, vm, codingModule, tuiModule); err != nil {
		return err
	}
	if err := installExampleComponents(runtime, tuiModule, codingModule); err != nil {
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

// installExampleHelpers adds the pi-tui/pi-coding-agent module members the
// upstream single-file examples import, each backed by the Go port of the
// matching upstream function or constant.
func installExampleHelpers(runtime *sobek.Runtime, vm *runtimeVM, codingModule, tuiModule *sobek.Object) error {
	if err := tuiModule.Set("CURSOR_MARKER", tui.CursorMarker); err != nil {
		return err
	}
	if err := tuiModule.Set("wrapTextWithAnsi", func(call sobek.FunctionCall) sobek.Value {
		lines := tui.WrapTextWithANSI(call.Argument(0).String(), int(call.Argument(1).ToInteger()))
		values := make([]any, len(lines))
		for index, line := range lines {
			values[index] = line
		}
		return runtime.NewArray(values...)
	}); err != nil {
		return err
	}
	if err := tuiModule.Set("applyBackgroundToLine", func(call sobek.FunctionCall) sobek.Value {
		background, ok := sobek.AssertFunction(call.Argument(2))
		if !ok {
			panic(runtime.NewTypeError("applyBackgroundToLine requires a background function"))
		}
		return runtime.ToValue(tui.ApplyBackgroundToLine(
			call.Argument(0).String(),
			int(call.Argument(1).ToInteger()),
			func(text string) string {
				value, err := background(sobek.Undefined(), runtime.ToValue(text))
				if err != nil {
					panic(err)
				}
				return value.String()
			},
		))
	}); err != nil {
		return err
	}

	if err := codingModule.Set("CONFIG_DIR_NAME", config.ConfigDirName); err != nil {
		return err
	}
	if err := codingModule.Set("DEFAULT_MAX_LINES", truncate.DefaultMaxLines); err != nil {
		return err
	}
	if err := codingModule.Set("DEFAULT_MAX_BYTES", truncate.DefaultMaxBytes); err != nil {
		return err
	}
	if err := codingModule.Set("formatSize", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(truncate.FormatSize(int(call.Argument(0).ToInteger())))
	}); err != nil {
		return err
	}
	if err := codingModule.Set("truncateHead", func(call sobek.FunctionCall) sobek.Value {
		var options []truncate.Options
		if value := call.Argument(1); present(value) {
			var decoded truncate.Options
			must(runtime, decodeJSON(runtime, value, &decoded))
			options = append(options, decoded)
		}
		return toJS(runtime, truncate.TruncateHead(call.Argument(0).String(), options...))
	}); err != nil {
		return err
	}
	if err := codingModule.Set("withFileMutationQueue", func(call sobek.FunctionCall) sobek.Value {
		path := call.Argument(0).String()
		fn, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(runtime.NewTypeError("withFileMutationQueue requires a function"))
		}
		return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
			return tools.WithFileMutationQueue(path, func() (any, error) {
				return vm.callback(ctx, func(runtime *sobek.Runtime) (any, error) {
					result, err := fn(sobek.Undefined())
					if err != nil {
						return nil, err
					}
					value, err := vm.awaitValue(ctx, runtime, result)
					if err != nil {
						return nil, err
					}
					if !present(value) {
						return promiseUndefined, nil
					}
					return value.Export(), nil
				})
			})
		})
	}); err != nil {
		return err
	}
	if err := codingModule.Set("convertToLlm", func(call sobek.FunctionCall) sobek.Value {
		items, ok := call.Argument(0).Export().([]any)
		if !ok {
			panic(runtime.NewTypeError("convertToLlm requires a message array"))
		}
		converted, err := codingagent.ConvertToLLM(vm.context(), agent.AgentMessages(items))
		must(runtime, err)
		return toJS(runtime, converted)
	}); err != nil {
		return err
	}
	return codingModule.Set("serializeConversation", func(call sobek.FunctionCall) sobek.Value {
		items, ok := call.Argument(0).Export().([]any)
		if !ok {
			panic(runtime.NewTypeError("serializeConversation requires a message array"))
		}
		messages := make(agent.AgentMessages, 0, len(items))
		for _, item := range items {
			encoded, err := json.Marshal(item)
			must(runtime, err)
			message, err := ai.UnmarshalMessage(encoded)
			if err != nil {
				// Upstream serializes only user/assistant/toolResult roles.
				continue
			}
			messages = append(messages, message)
		}
		return runtime.ToValue(harness.SerializeConversation(messages))
	})
}

// installExampleComponents evaluates the upstream pi-tui component classes
// (Container/Spacer/Text/Box/Loader/CancellableLoader) and the pi-coding-agent
// DynamicBorder/BorderedLoader in-VM, so Box/Container children can be
// arbitrary JS components. The text math (wrapping, widths, backgrounds)
// comes from the Go tui ports exposed on the pi-tui module.
func installExampleComponents(runtime *sobek.Runtime, tuiModule, codingModule *sobek.Object) error {
	factoryValue, err := runtime.RunString(exampleComponentsSource)
	if err != nil {
		return fmt.Errorf("compile example component classes: %w", err)
	}
	factory, ok := sobek.AssertFunction(factoryValue)
	if !ok {
		return fmt.Errorf("example component classes did not evaluate to a factory")
	}
	exportsValue, err := factory(sobek.Undefined(), tuiModule)
	if err != nil {
		return err
	}
	exports := exportsValue.ToObject(runtime)
	for _, name := range []string{"Container", "Spacer", "Text", "Box", "Loader", "CancellableLoader"} {
		if err := tuiModule.Set(name, exports.Get(name)); err != nil {
			return err
		}
	}
	for _, name := range []string{"DynamicBorder", "BorderedLoader"} {
		if err := codingModule.Set(name, exports.Get(name)); err != nil {
			return err
		}
	}
	return nil
}

// exampleComponentsSource ports the upstream classes 1:1 (pi-tui
// container/spacer/text/box/loader/cancellable-loader and pi-coding-agent
// dynamic-border/bordered-loader). Syntax stays within sobek's ES2017
// support: no optional chaining, nullish coalescing, spread, or class fields.
const exampleComponentsSource = `(function (tui) {
	"use strict";
	var visibleWidth = tui.visibleWidth;
	var wrapTextWithAnsi = tui.wrapTextWithAnsi;
	var applyBackgroundToLine = tui.applyBackgroundToLine;
	var matchesKey = tui.matchesKey;

	class Container {
		constructor() { this.children = []; }
		addChild(component) { this.children.push(component); }
		removeChild(component) {
			var index = this.children.indexOf(component);
			if (index !== -1) { this.children.splice(index, 1); }
		}
		clear() { this.children = []; }
		invalidate() {
			for (var i = 0; i < this.children.length; i++) {
				if (this.children[i].invalidate) { this.children[i].invalidate(); }
			}
		}
		render(width) {
			var lines = [];
			for (var i = 0; i < this.children.length; i++) {
				var childLines = this.children[i].render(width);
				for (var j = 0; j < childLines.length; j++) { lines.push(childLines[j]); }
			}
			return lines;
		}
	}

	class Spacer {
		constructor(lines) { this.lines = lines === undefined ? 1 : lines; }
		setLines(lines) { this.lines = lines; }
		invalidate() {}
		render(_width) {
			var result = [];
			for (var i = 0; i < this.lines; i++) { result.push(""); }
			return result;
		}
	}

	class Text {
		constructor(text, paddingX, paddingY, customBgFn) {
			this.text = text === undefined ? "" : text;
			this.paddingX = paddingX === undefined ? 1 : paddingX;
			this.paddingY = paddingY === undefined ? 1 : paddingY;
			this.customBgFn = customBgFn;
			this.cachedText = undefined;
			this.cachedWidth = undefined;
			this.cachedLines = undefined;
		}
		setText(text) {
			this.text = text;
			this.cachedText = undefined;
			this.cachedWidth = undefined;
			this.cachedLines = undefined;
		}
		setCustomBgFn(customBgFn) {
			this.customBgFn = customBgFn;
			this.cachedText = undefined;
			this.cachedWidth = undefined;
			this.cachedLines = undefined;
		}
		invalidate() {
			this.cachedText = undefined;
			this.cachedWidth = undefined;
			this.cachedLines = undefined;
		}
		render(width) {
			if (this.cachedLines && this.cachedText === this.text && this.cachedWidth === width) {
				return this.cachedLines;
			}
			if (!this.text || this.text.trim() === "") {
				var empty = [];
				this.cachedText = this.text;
				this.cachedWidth = width;
				this.cachedLines = empty;
				return empty;
			}
			var normalizedText = this.text.replace(/\t/g, "   ");
			var contentWidth = Math.max(1, width - this.paddingX * 2);
			var wrappedLines = wrapTextWithAnsi(normalizedText, contentWidth);
			var leftMargin = " ".repeat(this.paddingX);
			var rightMargin = " ".repeat(this.paddingX);
			var contentLines = [];
			for (var i = 0; i < wrappedLines.length; i++) {
				var lineWithMargins = leftMargin + wrappedLines[i] + rightMargin;
				if (this.customBgFn) {
					contentLines.push(applyBackgroundToLine(lineWithMargins, width, this.customBgFn));
				} else {
					var visibleLen = visibleWidth(lineWithMargins);
					contentLines.push(lineWithMargins + " ".repeat(Math.max(0, width - visibleLen)));
				}
			}
			var emptyLine = " ".repeat(width);
			var emptyLines = [];
			for (var j = 0; j < this.paddingY; j++) {
				emptyLines.push(this.customBgFn ? applyBackgroundToLine(emptyLine, width, this.customBgFn) : emptyLine);
			}
			var result = emptyLines.concat(contentLines, emptyLines);
			this.cachedText = this.text;
			this.cachedWidth = width;
			this.cachedLines = result;
			return result.length > 0 ? result : [""];
		}
	}

	class Box {
		constructor(paddingX, paddingY, bgFn) {
			this.children = [];
			this.paddingX = paddingX === undefined ? 1 : paddingX;
			this.paddingY = paddingY === undefined ? 1 : paddingY;
			this.bgFn = bgFn;
			this.cache = undefined;
		}
		addChild(component) { this.children.push(component); this.cache = undefined; }
		removeChild(component) {
			var index = this.children.indexOf(component);
			if (index !== -1) { this.children.splice(index, 1); this.cache = undefined; }
		}
		clear() { this.children = []; this.cache = undefined; }
		setBgFn(bgFn) { this.bgFn = bgFn; }
		invalidate() {
			this.cache = undefined;
			for (var i = 0; i < this.children.length; i++) {
				if (this.children[i].invalidate) { this.children[i].invalidate(); }
			}
		}
		applyBg(line, width) {
			var visLen = visibleWidth(line);
			var padded = line + " ".repeat(Math.max(0, width - visLen));
			return this.bgFn ? this.bgFn(padded) : padded;
		}
		matchCache(width, childLines, bgSample) {
			var cache = this.cache;
			if (!cache || cache.width !== width || cache.bgSample !== bgSample || cache.childLines.length !== childLines.length) {
				return false;
			}
			for (var i = 0; i < childLines.length; i++) {
				if (cache.childLines[i] !== childLines[i]) { return false; }
			}
			return true;
		}
		render(width) {
			if (this.children.length === 0) { return []; }
			var contentWidth = Math.max(1, width - this.paddingX * 2);
			var leftPad = " ".repeat(this.paddingX);
			var childLines = [];
			for (var i = 0; i < this.children.length; i++) {
				var lines = this.children[i].render(contentWidth);
				for (var j = 0; j < lines.length; j++) { childLines.push(leftPad + lines[j]); }
			}
			if (childLines.length === 0) { return []; }
			var bgSample = this.bgFn ? this.bgFn("test") : undefined;
			if (this.matchCache(width, childLines, bgSample)) { return this.cache.lines; }
			var result = [];
			for (var top = 0; top < this.paddingY; top++) { result.push(this.applyBg("", width)); }
			for (var line = 0; line < childLines.length; line++) { result.push(this.applyBg(childLines[line], width)); }
			for (var bottom = 0; bottom < this.paddingY; bottom++) { result.push(this.applyBg("", width)); }
			this.cache = { childLines: childLines, width: width, bgSample: bgSample, lines: result };
			return result;
		}
	}

	var DEFAULT_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
	var DEFAULT_INTERVAL_MS = 80;

	class Loader extends Text {
		constructor(ui, spinnerColorFn, messageColorFn, message, indicator) {
			super("", 1, 0);
			this.frames = DEFAULT_FRAMES.slice();
			this.intervalMs = DEFAULT_INTERVAL_MS;
			this.currentFrame = 0;
			this.intervalId = null;
			this.ui = ui === undefined ? null : ui;
			this.renderIndicatorVerbatim = false;
			this.spinnerColorFn = spinnerColorFn;
			this.messageColorFn = messageColorFn;
			this.message = message === undefined ? "Loading..." : message;
			this.setIndicator(indicator);
		}
		render(width) { return [""].concat(Text.prototype.render.call(this, width)); }
		start() { this.updateDisplay(); this.restartAnimation(); }
		stop() {
			if (this.intervalId) { clearInterval(this.intervalId); this.intervalId = null; }
		}
		setMessage(message) { this.message = message; this.updateDisplay(); }
		setIndicator(indicator) {
			this.renderIndicatorVerbatim = indicator !== undefined;
			this.frames = indicator && indicator.frames !== undefined ? indicator.frames.slice() : DEFAULT_FRAMES.slice();
			this.intervalMs = indicator && indicator.intervalMs && indicator.intervalMs > 0 ? indicator.intervalMs : DEFAULT_INTERVAL_MS;
			this.currentFrame = 0;
			this.start();
		}
		restartAnimation() {
			this.stop();
			if (this.frames.length <= 1) { return; }
			var self = this;
			this.intervalId = setInterval(function () {
				self.currentFrame = (self.currentFrame + 1) % self.frames.length;
				self.updateDisplay();
			}, this.intervalMs);
		}
		updateDisplay() {
			var frame = this.frames[this.currentFrame] === undefined ? "" : this.frames[this.currentFrame];
			var renderedFrame = this.renderIndicatorVerbatim ? frame : this.spinnerColorFn(frame);
			var indicator = frame.length > 0 ? renderedFrame + " " : "";
			this.setText(indicator + this.messageColorFn(this.message));
			if (this.ui) { this.ui.requestRender(); }
		}
	}

	class CancellableLoader extends Loader {
		constructor(ui, spinnerColorFn, messageColorFn, message, indicator) {
			super(ui, spinnerColorFn, messageColorFn, message, indicator);
			this.abortController = new AbortController();
			this.onAbort = undefined;
		}
		get signal() { return this.abortController.signal; }
		get aborted() { return this.abortController.signal.aborted; }
		handleInput(data) {
			// Upstream matches the tui.select.cancel keybinding (escape, ctrl+c).
			if (matchesKey(data, "escape") || matchesKey(data, "ctrl+c")) {
				this.abortController.abort();
				if (this.onAbort) { this.onAbort(); }
			}
		}
		dispose() { this.stop(); }
	}

	// Upstream's default color closes over the interactive-mode theme global,
	// which is undefined for extension-loaded modules; extensions always pass
	// an explicit color function.
	var globalTheme = undefined;
	class DynamicBorder {
		constructor(color) {
			this.color = color === undefined
				? function (str) { return globalTheme.fg("border", str); }
				: color;
		}
		invalidate() {}
		render(width) { return [this.color("─".repeat(Math.max(1, width)))]; }
	}

	class BorderedLoader extends Container {
		constructor(tuiHost, theme, message, options) {
			super();
			this.cancellable = options && options.cancellable !== undefined ? options.cancellable : true;
			var borderColor = function (s) { return theme.fg("border", s); };
			this.addChild(new DynamicBorder(borderColor));
			if (this.cancellable) {
				this.loader = new CancellableLoader(
					tuiHost,
					function (s) { return theme.fg("accent", s); },
					function (s) { return theme.fg("muted", s); },
					message
				);
			} else {
				this.signalController = new AbortController();
				this.loader = new Loader(
					tuiHost,
					function (s) { return theme.fg("accent", s); },
					function (s) { return theme.fg("muted", s); },
					message
				);
			}
			this.addChild(this.loader);
			if (this.cancellable) {
				this.addChild(new Spacer(1));
				// keyHint("tui.select.cancel", "cancel") over the default keys.
				this.addChild(new Text(theme.fg("dim", "escape/ctrl+c") + theme.fg("muted", " cancel"), 1, 0));
			}
			this.addChild(new Spacer(1));
			this.addChild(new DynamicBorder(borderColor));
		}
		get signal() {
			if (this.cancellable) { return this.loader.signal; }
			return this.signalController ? this.signalController.signal : new AbortController().signal;
		}
		set onAbort(fn) {
			if (this.cancellable) { this.loader.onAbort = fn; }
		}
		handleInput(data) {
			if (this.cancellable) { this.loader.handleInput(data); }
		}
		dispose() {
			if (typeof this.loader.dispose === "function") { this.loader.dispose(); }
			else if (typeof this.loader.stop === "function") { this.loader.stop(); }
		}
	}

	return {
		Container: Container,
		Spacer: Spacer,
		Text: Text,
		Box: Box,
		Loader: Loader,
		CancellableLoader: CancellableLoader,
		DynamicBorder: DynamicBorder,
		BorderedLoader: BorderedLoader,
	};
})`
