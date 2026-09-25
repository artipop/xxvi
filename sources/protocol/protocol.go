// Package protocol is the wire contract a source plugin speaks: JSON-RPC 2.0,
// one message per line, over stdio.
//
// It is outside internal/ because it is a contract with people who do not work
// on this repository: a plugin author has to be able to name these types. One
// declaration serves both ends — the app's client and the SDK a plugin is
// written against — so the two cannot drift apart, which is the failure this
// package exists to prevent.
//
// The transport is the one ACP and MCP already use, and for the same practical
// reason: spawning a process group and cleaning it up is already written, and a
// port per plugin would need a listener, an address to hand over and a way to
// find out it died — all of it again, buying nothing.
//
// The shape of the contract is a protocol rather than a Go interface because a
// plugin has to be writable in another language: an interface cannot be
// implemented from TypeScript. See docs/system.md, §8.1.
package protocol

import "encoding/json"

// Version is the protocol version this build speaks. It is a single integer,
// like ACP's: a plugin asking for a version above it is refused with a sentence
// somebody can act on, rather than half-understood.
const Version = 1

// Methods the app calls on a plugin.
const (
	MethodInitialize        = "initialize"
	MethodPoll              = "poll"
	MethodCredentialsUpdate = "credentials/update"
	MethodShutdown          = "shutdown"
)

// Notifications a plugin sends the app.
const (
	NotifyItems       = "items"
	NotifyLog         = "log"
	NotifyNeedsReauth = "needsReauth"
)

// Error kinds, carried in the `data.kind` of a JSON-RPC error. A closed set:
// the app decides what to do next from it, so a kind it does not know is
// treated as a defect of the plugin.
const (
	KindRetryable   = "retryable"    // network, 5xx, a timeout — come back later
	KindNeedsReauth = "needs_reauth" // the credential is dead; a person is needed
	KindBadConfig   = "bad_config"   // a field is wrong; repeating cannot help
)

// Capabilities is what a plugin says it can do. Everything the app does with a
// plugin afterwards is decided from this: what it does not claim, it is never
// asked for.
type Capabilities struct {
	// Poll says the plugin answers `poll`. Push says it sends `items` of its
	// own accord — a process that watches something and never sleeps.
	Poll bool `json:"poll"`
	Push bool `json:"push"`
	// Cursor says `poll` results carry a cursor worth handing back. Without it
	// the plugin reports everything it can see each time, and Version is what
	// keeps that from becoming duplicates.
	Cursor bool `json:"cursor"`
	// Noisy says this is a stream where most items are not wanted — a
	// notification shade. It decides what happens to an item no rule matched:
	// dropped here, filed in the inbox everywhere else.
	Noisy bool `json:"noisy"`
	// Auth is what the plugin needs to be given: "", "token" or "oauth2".
	Auth string `json:"auth,omitempty"`
}

// Credentials are what the app hands a plugin. Only ever the access token of
// its own source: the refresh token and everybody else's stay on this side.
type Credentials struct {
	AccessToken string `json:"accessToken,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"` // RFC 3339
}

// SourceInfo is the entry the plugin is being started for.
type SourceInfo struct {
	Name   string            `json:"name"`
	Config map[string]string `json:"config,omitempty"`
}

// HostInfo tells the plugin who it is talking to, so it can say so in a log or
// a user agent.
type HostInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeParams is the first message of every conversation.
type InitializeParams struct {
	ProtocolVersion int         `json:"protocolVersion"`
	Source          SourceInfo  `json:"source"`
	Credentials     Credentials `json:"credentials,omitempty"`
	Host            HostInfo    `json:"host"`
}

// InitializeResult is the plugin introducing itself.
type InitializeResult struct {
	ProtocolVersion int          `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
}

// PollParams asks for what is new. The cursor is whatever the plugin returned
// last time and is opaque to the app, which only stores it.
type PollParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// PollResult is the answer. RetryAfterSeconds lets a plugin that has been rate
// limited set the pace instead of the schedule.
type PollResult struct {
	Items             []json.RawMessage `json:"items"`
	Cursor            string            `json:"cursor,omitempty"`
	RetryAfterSeconds int               `json:"retryAfterSeconds,omitempty"`
}

// ItemsNotification is a push plugin delivering without being asked. It carries
// exactly what poll would have returned, so both take the same path afterwards.
type ItemsNotification struct {
	Items  []json.RawMessage `json:"items"`
	Cursor string            `json:"cursor,omitempty"`
}

// LogNotification is a line for the source's log.
type LogNotification struct {
	Level   string `json:"level,omitempty"` // info | warn | error
	Message string `json:"message"`
}

// ReauthNotification says the credential is dead and a person is needed.
type ReauthNotification struct {
	Reason string `json:"reason,omitempty"`
}
