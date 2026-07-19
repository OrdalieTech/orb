package theme

import (
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func Highlight(code, language string, theme *Theme) []string {
	language = strings.ToLower(strings.TrimSpace(language))
	lexer := lexers.Get(language)
	if lexer == nil || language == "" {
		return fallbackCode(code, theme)
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return fallbackCode(code, theme)
	}
	if ecmaScriptLanguage(language) {
		return highlightECMAScript(iterator, theme)
	}
	var rendered strings.Builder
	for token := iterator(); token != chroma.EOF; token = iterator() {
		parts := strings.Split(token.Value, "\n")
		for index, part := range parts {
			if part != "" {
				rendered.WriteString(highlightTokenForLanguage(language, token.Type, part, theme))
			}
			if index < len(parts)-1 {
				rendered.WriteByte('\n')
			}
		}
	}
	return strings.Split(strings.TrimSuffix(rendered.String(), "\n"), "\n")
}

func highlightECMAScript(iterator chroma.Iterator, theme *Theme) []string {
	tokens := make([]chroma.Token, 0)
	for token := iterator(); token != chroma.EOF; token = iterator() {
		tokens = append(tokens, token)
	}
	var rendered strings.Builder
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.Type == chroma.Text && !strings.Contains(token.Value, "\n") && strings.TrimSpace(token.Value) == "" && index+1 < len(tokens) {
			if close, tail, ok := ecmaParameterList(tokens, index+1); ok && ecmaPreviousOnLine(tokens, index+1) >= 0 && tokens[ecmaPreviousOnLine(tokens, index+1)].Value == "function" {
				rendered.WriteString(theme.Foreground("syntaxFunction", token.Value+tokens[index+1].Value))
				rendered.WriteString(theme.Foreground("syntaxVariable", ecmaTokenValues(tokens[index+2:close])))
				rendered.WriteString(theme.Foreground("syntaxFunction", ecmaTokenValues(tokens[close:tail])))
				index = tail - 1
				continue
			}
		}
		if close, tail, ok := ecmaParameterList(tokens, index); ok {
			rendered.WriteString(theme.Foreground("syntaxFunction", token.Value))
			rendered.WriteString(theme.Foreground("syntaxVariable", ecmaTokenValues(tokens[index+1:close])))
			rendered.WriteString(theme.Foreground("syntaxFunction", ecmaTokenValues(tokens[close:tail])))
			index = tail - 1
			continue
		}
		if token.Type == chroma.NameOther {
			if index+1 < len(tokens) {
				if _, _, ok := ecmaParameterList(tokens, index+1); ok {
					rendered.WriteString(theme.Foreground("syntaxFunction", token.Value))
					continue
				}
				if ecmaObjectKey(tokens, index) {
					rendered.WriteString(theme.Foreground("syntaxVariable", token.Value))
					continue
				}
			}
			rendered.WriteString(token.Value)
			continue
		}
		if token.Type == chroma.KeywordType && !ecmaPrimitiveType(token.Value) {
			rendered.WriteString(token.Value)
			continue
		}
		ecmaWriteToken(&rendered, token, theme)
	}
	return strings.Split(strings.TrimSuffix(rendered.String(), "\n"), "\n")
}

func ecmaParameterList(tokens []chroma.Token, open int) (close int, tail int, ok bool) {
	if open >= len(tokens) || tokens[open].Type != chroma.Punctuation || tokens[open].Value != "(" {
		return 0, 0, false
	}
	depth := 1
	for close = open + 1; close < len(tokens); close++ {
		if tokens[close].Type != chroma.Punctuation {
			continue
		}
		switch tokens[close].Value {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				goto found
			}
		}
	}
	return 0, 0, false

found:
	previous := ecmaPreviousOnLine(tokens, open)
	functionParameters := previous >= 0 && (tokens[previous].Value == "function" ||
		(tokens[previous].Type == chroma.NameOther && ecmaPreviousOnLine(tokens, previous) >= 0 &&
			(tokens[ecmaPreviousOnLine(tokens, previous)].Value == "function" || tokens[ecmaPreviousOnLine(tokens, previous)].Value == "async")))
	next := close + 1
	for next < len(tokens) && tokens[next].Type == chroma.Text && !strings.Contains(tokens[next].Value, "\n") && strings.TrimSpace(tokens[next].Value) == "" {
		next++
	}
	arrowParameters := previous >= 0 && tokens[previous].Type.InCategory(chroma.Operator) && tokens[previous].Value == "=" &&
		next < len(tokens) && tokens[next].Type.InCategory(chroma.Operator) && tokens[next].Value == "=>"
	if !functionParameters && !arrowParameters {
		return 0, 0, false
	}
	tail = close + 1
	if arrowParameters {
		tail = next + 1
	} else if previous >= 0 && tokens[previous].Value == "function" && next < len(tokens) && tokens[next].Type == chroma.Punctuation && strings.HasPrefix(tokens[next].Value, "{") {
		tail = next
	}
	return close, tail, true
}

func ecmaScriptLanguage(language string) bool {
	switch language {
	case "typescript", "ts", "tsx", "javascript", "js", "jsx", "mjs", "cjs":
		return true
	default:
		return false
	}
}

func ecmaPrimitiveType(value string) bool {
	switch value {
	case "any", "bigint", "boolean", "never", "number", "object", "string", "symbol", "unknown", "void":
		return true
	default:
		return false
	}
}

func ecmaPreviousOnLine(tokens []chroma.Token, index int) int {
	for index--; index >= 0; index-- {
		if strings.Contains(tokens[index].Value, "\n") {
			return -1
		}
		if strings.TrimSpace(tokens[index].Value) != "" {
			return index
		}
	}
	return -1
}

func ecmaObjectKey(tokens []chroma.Token, index int) bool {
	if index+1 >= len(tokens) {
		return false
	}
	next := tokens[index+1]
	operatorColon := next.Type.InCategory(chroma.Operator) && next.Value == ":"
	textColon := next.Type == chroma.Text && strings.HasPrefix(next.Value, ":")
	if !operatorColon && !textColon {
		return false
	}
	previous := ecmaPreviousOnLine(tokens, index)
	return previous < 0 || (tokens[previous].Type == chroma.Punctuation && (strings.Contains(tokens[previous].Value, "{") || strings.Contains(tokens[previous].Value, ",")))
}

func ecmaTokenValues(tokens []chroma.Token) string {
	var value strings.Builder
	for _, token := range tokens {
		value.WriteString(token.Value)
	}
	return value.String()
}

func ecmaWriteToken(rendered *strings.Builder, token chroma.Token, theme *Theme) {
	parts := strings.Split(token.Value, "\n")
	for index, part := range parts {
		if part != "" {
			rendered.WriteString(highlightTokenForLanguage("typescript", token.Type, part, theme))
		}
		if index < len(parts)-1 {
			rendered.WriteByte('\n')
		}
	}
}

var operatorScopeLanguages = map[string]bool{
	"llvm": true, "mathematica": true, "powershell": true, "pwsh": true,
	"reason": true, "reasonml": true, "sql": true, "swift": true,
}

var punctuationScopeLanguages = map[string]bool{"http": true, "https": true, "llvm": true}

func highlightTokenForLanguage(language string, token chroma.TokenType, value string, theme *Theme) string {
	if language == "json" && token == chroma.NameTag {
		return theme.Foreground("syntaxVariable", value)
	}
	if token.InCategory(chroma.Operator) || token == chroma.NameOperator {
		if !operatorScopeLanguages[language] {
			return value
		}
	}
	if token.InCategory(chroma.Punctuation) {
		if language == "llvm" && value == "=" {
			return theme.Foreground("syntaxOperator", value)
		}
		if !punctuationScopeLanguages[language] {
			return value
		}
	}
	return highlightToken(token, value, theme)
}

func fallbackCode(code string, theme *Theme) []string {
	lines := strings.Split(code, "\n")
	for index := range lines {
		lines[index] = theme.Foreground("mdCodeBlock", lines[index])
	}
	return lines
}

func highlightToken(token chroma.TokenType, value string, theme *Theme) string {
	color := ""
	switch {
	case token.InCategory(chroma.Comment):
		color = "syntaxComment"
	case token == chroma.NameFunction || token == chroma.NameFunctionMagic || token == chroma.NameLabel:
		color = "syntaxFunction"
	case token == chroma.NameClass || token == chroma.NameBuiltin || token == chroma.NameBuiltinPseudo || token == chroma.KeywordType:
		color = "syntaxType"
	case token.InCategory(chroma.Keyword):
		color = "syntaxKeyword"
	case token.InSubCategory(chroma.NameVariable) || token == chroma.NameAttribute || token == chroma.NameProperty:
		color = "syntaxVariable"
	case token.InSubCategory(chroma.LiteralString):
		color = "syntaxString"
	case token.InSubCategory(chroma.LiteralNumber):
		color = "syntaxNumber"
	case token.InCategory(chroma.Operator) || token == chroma.NameOperator:
		color = "syntaxOperator"
	case token.InCategory(chroma.Punctuation):
		color = "syntaxPunctuation"
	case token == chroma.GenericInserted:
		color = "toolDiffAdded"
	case token == chroma.GenericDeleted:
		color = "toolDiffRemoved"
	case token == chroma.GenericEmph:
		return Italic(value)
	case token == chroma.GenericStrong:
		return Bold(value)
	}
	if color == "" {
		return value
	}
	return theme.Foreground(color, value)
}

var languageByExtension = map[string]string{
	"ts": "typescript", "tsx": "typescript", "js": "javascript", "jsx": "javascript", "mjs": "javascript", "cjs": "javascript",
	"py": "python", "rb": "ruby", "rs": "rust", "go": "go", "java": "java", "kt": "kotlin", "swift": "swift",
	"c": "c", "h": "c", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp", "cs": "csharp", "php": "php",
	"sh": "bash", "bash": "bash", "zsh": "bash", "fish": "fish", "ps1": "powershell", "sql": "sql",
	"html": "html", "htm": "html", "css": "css", "scss": "scss", "sass": "sass", "less": "less",
	"json": "json", "yaml": "yaml", "yml": "yaml", "toml": "toml", "xml": "xml", "md": "markdown", "markdown": "markdown",
	"dockerfile": "dockerfile", "makefile": "makefile", "cmake": "cmake", "lua": "lua", "perl": "perl", "r": "r",
	"scala": "scala", "clj": "clojure", "ex": "elixir", "exs": "elixir", "erl": "erlang", "hs": "haskell", "ml": "ocaml",
	"vim": "vim", "graphql": "graphql", "proto": "protobuf", "tf": "hcl", "hcl": "hcl",
}

func LanguageFromPath(path string) string {
	base := path
	if slash := strings.LastIndexAny(base, `/\`); slash >= 0 {
		base = base[slash+1:]
	}
	if language := languageByExtension[strings.ToLower(base)]; language != "" {
		return language
	}
	if dot := strings.LastIndexByte(base, '.'); dot >= 0 && dot < len(base)-1 {
		return languageByExtension[strings.ToLower(base[dot+1:])]
	}
	return ""
}
