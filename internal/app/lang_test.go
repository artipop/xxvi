package app

import (
	"slices"
	"testing"
)

// «system» means what the screen shows: the first system language the UI has
// words for, English when there is none.
func TestAgentLanguage(t *testing.T) {
	for _, c := range []struct {
		chosen string
		system []string
		want   string
	}{
		{chosen: "ru", system: []string{"en-US"}, want: "Russian"},
		{chosen: LangSystem, system: []string{"de-DE", "ru_RU", "en"}, want: "Russian"},
		{chosen: LangSystem, system: []string{"de-DE"}, want: "English"},
		{chosen: LangSystem, want: "English"},
	} {
		if got := agentLanguage(c.chosen, c.system); got != c.want {
			t.Fatalf("%q %q: got %q, want %q", c.chosen, c.system, got, c.want)
		}
	}
}

func TestEnvLanguages(t *testing.T) {
	for _, c := range []struct {
		name                     string
		all, messages, lang, lst string
		want                     []string
	}{
		{name: "lang", lang: "ru_RU.UTF-8", want: []string{"ru-RU"}},
		{name: "lc_all wins", all: "de_DE.UTF-8@euro", lang: "ru_RU.UTF-8", want: []string{"de-DE"}},
		{name: "messages before lang", messages: "ru_RU", lang: "en_US", want: []string{"ru-RU"}},
		{name: "language list first", lang: "en_US.UTF-8", lst: "ru:uk", want: []string{"ru", "uk", "en-US"}},
		{name: "C names nothing", lang: "C", lst: "ru", want: nil},
		{name: "nothing set", want: nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LC_ALL", c.all)
			t.Setenv("LC_MESSAGES", c.messages)
			t.Setenv("LANG", c.lang)
			t.Setenv("LANGUAGE", c.lst)
			if got := envLanguages(); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
