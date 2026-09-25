// Package plugin runs a source plugin: it spawns the process and speaks the
// protocol to it. The protocol itself lives in sources/protocol, outside
// internal/, because a plugin author has to be able to name those types — see
// the note there.
//
// Everything the wire knows about is aliased here rather than re-declared, so
// this package reads as one thing while there is still only one declaration of
// each message.
package plugin

import "github.com/artipop/xxvi/sources/protocol"

// The wire types, under the names this side of the app uses them by.
type (
	Capabilities       = protocol.Capabilities
	Credentials        = protocol.Credentials
	SourceInfo         = protocol.SourceInfo
	HostInfo           = protocol.HostInfo
	InitializeParams   = protocol.InitializeParams
	InitializeResult   = protocol.InitializeResult
	PollParams         = protocol.PollParams
	PollResult         = protocol.PollResult
	ItemsNotification  = protocol.ItemsNotification
	LogNotification    = protocol.LogNotification
	ReauthNotification = protocol.ReauthNotification
)

const (
	Version = protocol.Version

	MethodInitialize        = protocol.MethodInitialize
	MethodPoll              = protocol.MethodPoll
	MethodCredentialsUpdate = protocol.MethodCredentialsUpdate
	MethodShutdown          = protocol.MethodShutdown

	NotifyItems       = protocol.NotifyItems
	NotifyLog         = protocol.NotifyLog
	NotifyNeedsReauth = protocol.NotifyNeedsReauth

	KindRetryable   = protocol.KindRetryable
	KindNeedsReauth = protocol.KindNeedsReauth
	KindBadConfig   = protocol.KindBadConfig
)
