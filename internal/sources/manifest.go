// Package sources runs the sources that are programs: a plugin speaking
// sources/protocol, or an MCP server read through a mapping. Everything a
// source brings goes through inbox.Pipeline, the same path the demo file and the
// review queue take, so past the runner nothing can tell which kind brought it.
//
// A source is a plugin rather than a branch in this code (docs/system.md §8.1):
// a new tracker is a manifest, and for a service somebody already wrote an MCP
// server for, the manifest is the whole adapter.
package sources

import (
	"fmt"
	"os"
	"strings"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// Manifest describes a plugin: how to start it, what it needs and what to ask
// for. It is meant to be pasted whole, the way an MCP server is pasted into an
// agent from its README.
type Manifest struct {
	Name            string `json:"name"`
	Title           string `json:"title,omitempty"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`

	// Command, Args and Env are templates over the source, like everything a
	// person fills in reaches the process: the site their tracker lives at is
	// theirs, not the manifest's, and so is where a program they installed
	// lives on their disk.
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Dir     string            `json:"dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	Auth   *AuthSpec `json:"auth,omitempty"`
	Fields []Field   `json:"fields,omitempty"`

	// Kind is what the command speaks: sources/protocol by default, or MCP. A
	// second kind rather than a second registry, because the schedule, the
	// token, the rules and the status are the same whichever way it talks.
	Kind string   `json:"kind,omitempty"`
	MCP  *MCPSpec `json:"mcp,omitempty"`

	// TokenEnv is the variable an MCP server reads its token from. MCP has no
	// place for a credential and every server names its own variable; our own
	// plugins get the token in the handshake and need none of this.
	TokenEnv string `json:"tokenEnv,omitempty"`

	// Managed marks a source that is not made in the sources form: the review
	// queue is made by connecting a project's hosting, and the form has nothing
	// to ask for it. It is in the list all the same, so a source of it is
	// named and edited like any other.
	Managed bool `json:"managed,omitempty"`
}

// Plugin kinds.
const (
	KindPlugin = "plugin"
	KindMCP    = "mcp"
	// KindBuiltin is read by this application's own code rather than by a
	// process: the demo file, the review queue. It is given to the catalog
	// from code and never from a file — a file naming one would take over a
	// source the application reads itself.
	KindBuiltin = "builtin"
)

// SelfCommand is what a manifest writes instead of a path to this application:
// a packaged app has no checkout to point at, and a person adding a source
// should not be asked where the app they are using lives.
const SelfCommand = "$self"

// Field is one setting a plugin needs. The source form is built from these,
// which is the reason a manifest exists: without it every source would need its
// own dialog, and one somebody else wrote could not have one at all.
//
// The types are a closed set: a manifest that could describe any form would be
// a program, and nothing here could promise the form made sense.
type Field struct {
	Key     string   `json:"key"`
	Title   string   `json:"title,omitempty"`
	Type    string   `json:"type,omitempty"`
	Default string   `json:"default,omitempty"`
	Values  []string `json:"values,omitempty"` // for select

	// Options, when set, are the values to choose from, asked of the service
	// itself: its boards, its columns. The form offers them in place of a text
	// box, so a name is picked rather than typed and mistyped.
	Options *Options `json:"options,omitempty"`

	// ShowWhen keeps the field out of the form until another field has the
	// given value: the address of a server means nothing for a program that
	// runs on this machine, and asking for it anyway invites filling it in.
	ShowWhen *ShowWhen `json:"showWhen,omitempty"`
}

// ShowWhen names the field and the value it must have for another to be shown.
type ShowWhen struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Options is a question put to an MCP server whose answer is a list of values
// for one field. It reads like the feed's own mapping, and for the same
// reason: the rows are JSON with names on them.
type Options struct {
	// Tool is called with Arguments, templated over the source as the feed's
	// are, and At is where the list is in its answer.
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments,omitempty"`
	At        string            `json:"at,omitempty"`

	// Value is what is stored in the field and Label what the person reads;
	// both are templates over a row, and the label is the value when empty.
	Value string `json:"value"`
	Label string `json:"label,omitempty"`

	// DependsOn names the fields that must be filled before the question can
	// be asked — columns are those of a board — and that change the answer.
	DependsOn []string `json:"dependsOn,omitempty"`
}

// Field types.
const (
	FieldString = "string"
	FieldNumber = "number"
	FieldBool   = "bool"
	FieldSelect = "select"
)

// There is no secret field: a token is asked for by auth, kept in the keychain
// and never in the source's config, which is a row anybody can read.
var fieldTypes = []string{FieldString, FieldNumber, FieldBool, FieldSelect}

// AuthSpec is what a plugin needs to be given. Only a token for now: OAuth needs
// a browser round trip this application does not do yet.
type AuthSpec struct {
	Type string `json:"type"` // token

	// Optional says the plugin runs without one and takes it when given: the
	// XCIII bridge on the same machine finds its own secret, and only a
	// server needs a token a person issued.
	Optional bool `json:"optional,omitempty"`
}

// AuthToken is the one auth type there is.
const AuthToken = "token"

// NeedsToken reports whether a source of this plugin is useless without a token.
func (p Manifest) NeedsToken() bool {
	return p.TakesToken() && !p.Auth.Optional
}

// TakesToken reports whether the form asks for a token at all.
func (p Manifest) TakesToken() bool {
	return p.Auth != nil && p.Auth.Type == AuthToken
}

// IsMCP reports whether this manifest is an MCP server read through a mapping.
func (p Manifest) IsMCP() bool { return p.Kind == KindMCP }

// IsBuiltin reports whether the application reads this source itself.
func (p Manifest) IsBuiltin() bool { return p.Kind == KindBuiltin }

// Validate normalizes a manifest and refuses what cannot be started or shown.
func (p Manifest) Validate() (Manifest, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, msg.Err("plugin.noName")
	}
	p.Command = strings.TrimSpace(p.Command)
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	if p.Command == "" && p.Kind != KindBuiltin {
		return p, msg.Err("plugin.noCommand", "plugin", p.Name)
	}
	switch p.Kind {
	case KindBuiltin:
		// Nothing to start and nothing to map.
	case "", KindPlugin:
		p.Kind = KindPlugin
		if p.MCP != nil {
			return p, msg.Err("plugin.mappingWithoutMCP", "plugin", p.Name)
		}
	case KindMCP:
		spec := MCPSpec{}
		if p.MCP != nil {
			spec = *p.MCP
		}
		valid, err := spec.Validate()
		if err != nil {
			return p, msg.Wrap(err, "plugin.invalid", "plugin", p.Name)
		}
		p.MCP = &valid
	default:
		return p, msg.Err("plugin.unknownKind", "plugin", p.Name, "kind", p.Kind,
			"allowed", KindPlugin+", "+KindMCP)
	}
	if p.Auth != nil {
		p.Auth.Type = strings.ToLower(strings.TrimSpace(p.Auth.Type))
		if p.Auth.Type != AuthToken {
			return p, msg.Err("plugin.unknownAuth", "plugin", p.Name, "auth", p.Auth.Type, "allowed", AuthToken)
		}
	}
	for i, f := range p.Fields {
		f.Key = strings.TrimSpace(f.Key)
		if f.Key == "" {
			return p, msg.Err("plugin.fieldNoKey", "plugin", p.Name, "n", fmt.Sprint(i+1))
		}
		if f.Type == "" {
			f.Type = FieldString
		}
		if !contains(fieldTypes, f.Type) {
			return p, msg.Err("plugin.unknownFieldType", "plugin", p.Name, "field", f.Key,
				"type", f.Type, "allowed", strings.Join(fieldTypes, ", "))
		}
		if w := f.ShowWhen; w != nil {
			known := false
			for _, other := range p.Fields {
				known = known || (other.Key == w.Key && other.Key != f.Key)
			}
			if !known {
				return p, msg.Err("plugin.showWhenUnknown", "plugin", p.Name, "field", f.Key, "key", w.Key)
			}
		}
		if f.Options != nil {
			opts, err := f.Options.validate(p, f)
			if err != nil {
				return p, err
			}
			f.Options = &opts
		}
		p.Fields[i] = f
	}
	return p, nil
}

func (o Options) validate(p Manifest, f Field) (Options, error) {
	if p.Kind != KindMCP {
		return o, msg.Err("plugin.optionsNeedMCP", "plugin", p.Name, "field", f.Key)
	}
	if f.Type != FieldString {
		return o, msg.Err("plugin.optionsNeedString", "plugin", p.Name, "field", f.Key)
	}
	o.Tool = strings.TrimSpace(o.Tool)
	o.Value = strings.TrimSpace(o.Value)
	if o.Tool == "" || o.Value == "" {
		return o, msg.Err("plugin.optionsIncomplete", "plugin", p.Name, "field", f.Key)
	}
	for _, key := range o.DependsOn {
		if key == f.Key {
			return o, msg.Err("plugin.optionsDependsOnItself", "plugin", p.Name, "field", f.Key)
		}
	}
	return o, nil
}

// Argv is the command to start for a source. SelfCommand is resolved here —
// where this application is, is only known at run time — and the rest is
// expanded over the source.
func (p Manifest) Argv(src model.Source) ([]string, error) {
	command := p.Command
	if command == SelfCommand {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("find this application: %w", err)
		}
		command = self
	} else {
		var err error
		if command, err = renderOver(command, src); err != nil {
			return nil, msg.Wrap(err, "plugin.badTemplate", "plugin", p.Name, "what", "command")
		}
		if command = strings.TrimSpace(command); command == "" {
			return nil, msg.Err("plugin.noCommand", "plugin", p.Name)
		}
	}
	argv := []string{command}
	for _, arg := range p.Args {
		value, err := renderOver(arg, src)
		if err != nil {
			return nil, msg.Wrap(err, "plugin.badTemplate", "plugin", p.Name, "what", arg)
		}
		argv = append(argv, value)
	}
	return argv, nil
}

// RenderEnv is the manifest's environment expanded over the source. A variable
// that comes out empty is left out, so a field nobody filled in does not reach
// the process as a value it would believe.
func (p Manifest) RenderEnv(src model.Source) (map[string]string, error) {
	out := make(map[string]string, len(p.Env)+1)
	for name, tmpl := range p.Env {
		value, err := renderOver(tmpl, src)
		if err != nil {
			return nil, msg.Wrap(err, "plugin.badTemplate", "plugin", p.Name, "what", name)
		}
		if value = strings.TrimSpace(value); value != "" {
			out[name] = value
		}
	}
	return out, nil
}

// WithDefaults is the source's config with the manifest's defaults under it,
// so a field left as the form showed it means what the form showed.
func (p Manifest) WithDefaults(src model.Source) model.Source {
	config := make(map[string]string, len(src.Config)+len(p.Fields))
	for _, f := range p.Fields {
		if f.Default != "" {
			config[f.Key] = f.Default
		}
	}
	for k, v := range src.Config {
		if strings.TrimSpace(v) != "" {
			config[k] = v
		}
	}
	src.Config = config
	return src
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
