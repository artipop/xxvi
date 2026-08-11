package acp

import (
	"context"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Elicitation is the other half of an agent asking: not "may I", but "which
// one". The claude CLI's AskUserQuestion arrives here — as a form of one
// property carrying a `oneOf` of the options, and a second property beside it,
// marked in its _meta, for an answer typed instead of chosen.
//
// Only form mode is claimed (see clientCapabilities). URL mode sends a person
// to a browser to finish something there, and an application that is itself the
// place the work happens has nowhere to put that.

// UnstableCreateElicitation puts the agent's form to the card and waits.
func (c *sessionClient) UnstableCreateElicitation(ctx context.Context, params acpsdk.UnstableCreateElicitationRequest) (acpsdk.UnstableCreateElicitationResponse, error) {
	if params.Form == nil {
		return declineElicitation(), nil
	}
	q := questionFromForm(*params.Form)
	answer := c.m.ask(ctx, c.s, q)

	if answer.Declined || answer.empty() {
		return declineElicitation(), nil
	}
	content := map[string]any{}
	if text := strings.TrimSpace(answer.Text); text != "" && q.freeField != "" {
		content[q.freeField] = text
	} else if answer.OptionID != "" && q.field != "" {
		content[q.field] = answer.OptionID
	} else if text != "" && q.field != "" {
		// A form with no options at all: the one field is the answer.
		content[q.field] = text
	}
	if len(content) == 0 {
		return declineElicitation(), nil
	}
	return acpsdk.UnstableCreateElicitationResponse{
		Accept: &acpsdk.UnstableCreateElicitationAccept{Action: "accept", Content: content},
	}, nil
}

func declineElicitation() acpsdk.UnstableCreateElicitationResponse {
	return acpsdk.UnstableCreateElicitationResponse{
		Decline: &acpsdk.UnstableCreateElicitationDecline{Action: "decline"},
	}
}

// questionFromForm reads the one shape that matters and ignores the rest.
//
// A schema is JSON Schema, so it can describe a form of any size, but what
// agents actually send is one question: a property with a `oneOf` of consts to
// choose from, optionally with a free-text property naming it as the place for
// a custom answer. Anything beyond the first such property is left alone —
// answering half a form badly is worse than the agent hearing "no".
func questionFromForm(form acpsdk.UnstableCreateElicitationForm) Question {
	q := Question{Kind: QuestionForm, Text: form.Message}

	names := schemaPropertyNames(form.RequestedSchema)
	for _, name := range names {
		prop, _ := form.RequestedSchema.Properties[name].(map[string]any)
		if prop == nil || customAnswerFor(prop) != "" {
			continue
		}
		if options := schemaOptions(prop); len(options) > 0 {
			q.field = name
			q.Options = options
			if title, ok := prop["title"].(string); ok && q.Text == "" {
				q.Text = title
			}
			break
		}
	}
	// No options anywhere: the form is asking for words, and the first property
	// is where they go.
	if q.field == "" && len(names) > 0 {
		q.field = names[0]
		q.FreeText = true
		if prop, ok := form.RequestedSchema.Properties[names[0]].(map[string]any); ok {
			if title, ok := prop["title"].(string); ok && q.Text == "" {
				q.Text = title
			}
		}
		return q
	}
	// The custom-answer field, if the agent offered one for this question.
	for _, name := range names {
		prop, _ := form.RequestedSchema.Properties[name].(map[string]any)
		if prop != nil && customAnswerFor(prop) == q.field {
			q.freeField = name
			q.FreeText = true
			break
		}
	}
	return q
}

// schemaPropertyNames returns the properties in a stable order: a map has none,
// and which question a person is asked must not depend on map iteration.
func schemaPropertyNames(schema acpsdk.UnstableElicitationSchema) []string {
	names := make([]string, 0, len(schema.Properties))
	// Required first and in the order the agent listed them — that is the
	// closest thing a JSON Schema has to "the important one first".
	seen := map[string]bool{}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; ok && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	rest := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// schemaOptions reads a `oneOf` of consts into the buttons a card shows.
func schemaOptions(prop map[string]any) []QuestionOption {
	raw, ok := prop["oneOf"].([]any)
	if !ok {
		return nil
	}
	out := make([]QuestionOption, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		value, ok := item["const"].(string)
		if !ok || value == "" {
			continue
		}
		opt := QuestionOption{ID: value, Label: value}
		if title, ok := item["title"].(string); ok && title != "" {
			opt.Label = title
		}
		if desc, ok := item["description"].(string); ok {
			opt.Description = desc
		}
		out = append(out, opt)
	}
	return out
}

// customAnswerFor reports which question this property is the typed answer to,
// which the claude adapter marks in the property's own _meta.
func customAnswerFor(prop map[string]any) string {
	meta, ok := prop["_meta"].(map[string]any)
	if !ok {
		return ""
	}
	custom, ok := meta["_askUserQuestionCustomAnswer"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := custom["questionId"].(string)
	return id
}
