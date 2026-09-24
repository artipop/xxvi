package app

import (
	"os"
	"strings"
)

// The language is the person's to choose, and the choice is kept with the
// machine's settings rather than in the window: the webview's own storage is
// not something this application can count on keeping.
const langSetting = "ui.lang"

// LangSystem is the choice to follow the operating system's language.
const LangSystem = "system"

// Language is what the UI needs to pick its words: what the person chose, and
// the languages the system prefers, best first, for when they chose the system.
type Language struct {
	// Chosen is a language tag the UI has a dictionary for, or LangSystem.
	Chosen string `json:"chosen"`
	// System is the operating system's preferred languages as BCP 47 tags. It
	// is empty where they cannot be learned from here, and the UI then asks its
	// webview instead.
	System []string `json:"system"`
}

// Language is the person's choice of language and what the system would say.
func (s *API) Language() (Language, error) {
	chosen, err := s.app.Store.Setting(langSetting, LangSystem)
	if err != nil {
		return Language{}, err
	}
	return Language{Chosen: chosen, System: systemLanguages()}, nil
}

// SetLanguage keeps the person's choice: a language tag, or LangSystem. Which
// tags there are words for is the UI's to know, so any tag is kept as given.
func (s *API) SetLanguage(chosen string) error {
	chosen = strings.TrimSpace(chosen)
	if chosen == "" {
		chosen = LangSystem
	}
	return s.app.Store.SetSetting(langSetting, chosen)
}

// briefLanguages names the UI's languages in English, which is what every brief
// is written in. The keys are i18n.ts's LANGS: a language the UI has no words
// for is not one the person reads this app in.
var briefLanguages = map[string]string{"en": "English", "ru": "Russian"}

// AgentLanguage is the language the person reads the app in, named for a brief.
func (a *App) AgentLanguage() string {
	chosen, err := a.Store.Setting(langSetting, LangSystem)
	if err != nil {
		chosen = LangSystem
	}
	return agentLanguage(chosen, systemLanguages())
}

// agentLanguage resolves «system» the way the UI does (i18n.ts pick), so the
// agent answers in the language the screen is showing and not in a second
// guess at it.
func agentLanguage(chosen string, system []string) string {
	if name, ok := briefLanguages[chosen]; ok {
		return name
	}
	for _, tag := range system {
		base, _, _ := strings.Cut(strings.ToLower(strings.ReplaceAll(tag, "_", "-")), "-")
		if name, ok := briefLanguages[base]; ok {
			return name
		}
	}
	return briefLanguages["en"]
}

// envLanguages reads the POSIX locale variables in the order they win. On macOS
// they describe the terminal, not the person's choice in System Settings, so
// there they are only the fallback.
func envLanguages() []string {
	var out []string
	add := func(v string) {
		// «ru_RU.UTF-8@euro» → «ru-RU»; C and POSIX name no language at all.
		v, _, _ = strings.Cut(v, ".")
		v, _, _ = strings.Cut(v, "@")
		if v == "" || v == "C" || v == "POSIX" {
			return
		}
		out = append(out, strings.ReplaceAll(v, "_", "-"))
	}
	// LANGUAGE is a list and outranks the rest, but only for a real locale.
	if lc := firstEnv("LC_ALL", "LC_MESSAGES", "LANG"); lc != "" && lc != "C" && lc != "POSIX" {
		for _, l := range strings.Split(os.Getenv("LANGUAGE"), ":") {
			add(l)
		}
	}
	add(firstEnv("LC_ALL", "LC_MESSAGES", "LANG"))
	return out
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
