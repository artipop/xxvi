package engine

import (
	"fmt"
	"strings"

	"github.com/artipop/xxvi/internal/model"
)

// What a stage leaves on the card, and how it gets there.
//
// A stage declares its outputs (model.Stage.Writes) and the brief asks the agent
// to end its message with one «Свойство: значение» line per output
// (StageOutputs). This is the reading half of that contract.
//
// Closing words rather than a tool call because that is the only channel there
// is: an ACP session here has no board tools, and the flow already routes on
// what the agent said last (outcomeHint). One currency for both is one thing to
// explain rather than two.
//
// The values land on the card *before* the outcome moves it, so a conditional
// edge reading «Вердикт» reads it as it now stands rather than as it was one
// stage ago.

// ParseWrites reads a stage's declared outputs out of what the agent said.
//
// The scan is backwards, and the first line naming a property wins: the brief
// asks for the values at the end, and an agent that mentioned «Вердикт» while
// thinking out loud must not have that mistaken for its answer. Only declared
// properties are looked for — nothing an agent writes can invent a field on the
// card.
func ParseWrites(text string, writes []model.PropertyWrite) map[string]string {
	if len(writes) == 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	out := map[string]string{}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		name, value, ok := splitLine(lines[i])
		if !ok {
			continue
		}
		for _, w := range writes {
			if !strings.EqualFold(strings.TrimSpace(w.Property), name) {
				continue
			}
			if _, taken := out[w.Property]; !taken && value != "" {
				out[w.Property] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitLine reads one «Свойство: значение» line, tolerating the bullet and the
// bold a model reaches for when it is asked for a list. Everything after the
// first colon is the value — a preview address has colons in it.
func splitLine(line string) (name, value string, ok bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "-*• \t")
	at := strings.Index(line, ":")
	if at < 0 {
		return "", "", false
	}
	name = strings.TrimSpace(strings.Trim(line[:at], "*_` "))
	value = strings.TrimSpace(strings.Trim(line[at+1:], "*_` "))
	if name == "" {
		return "", "", false
	}
	return name, value, true
}

// MissingRequired is the outputs a stage was obliged to produce and did not.
// A stage cannot end until its required values stand: an edge that branches on
// one would otherwise send the card down the fallback for want of a value
// nobody knew was absent.
func MissingRequired(writes []model.PropertyWrite, delivered map[string]string) []string {
	var out []string
	for _, w := range writes {
		if !w.Required {
			continue
		}
		if strings.TrimSpace(delivered[w.Property]) == "" {
			out = append(out, w.Property)
		}
	}
	return out
}

// describeWrites is what the card records about a stage's outputs, in the order
// the stage declared them so two runs read alike.
func describeWrites(writes []model.PropertyWrite, delivered map[string]string) string {
	var parts []string
	for _, w := range writes {
		if v, ok := delivered[w.Property]; ok {
			parts = append(parts, fmt.Sprintf("«%s» = «%s»", w.Property, v))
		}
	}
	return strings.Join(parts, ", ")
}
