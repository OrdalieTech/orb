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

var operatorScopeLanguages = map[string]bool{
	"llvm": true, "mathematica": true, "powershell": true, "pwsh": true,
	"reason": true, "reasonml": true, "sql": true, "swift": true,
}

var punctuationScopeLanguages = map[string]bool{"http": true, "https": true, "llvm": true}

func highlightTokenForLanguage(language string, token chroma.TokenType, value string, theme *Theme) string {
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
