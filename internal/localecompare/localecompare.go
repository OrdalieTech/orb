package localecompare

import (
	"os"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

func New() *collate.Collator {
	return collate.New(defaultLanguage())
}

func defaultLanguage() language.Tag {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			locale = value
			break
		}
	}
	locale = strings.SplitN(locale, ".", 2)[0]
	locale = strings.SplitN(locale, "@", 2)[0]
	if locale == "" || locale == "C" || locale == "POSIX" {
		return language.AmericanEnglish
	}
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return language.AmericanEnglish
	}
	return tag
}
