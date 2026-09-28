//go:build windows

package ptyhold

// Screen on Windows is the raw tail: libghostty needs cgo and a Zig-built
// library there, and the release is not built that way yet.
type Screen struct{ tail *Tail }

const tailCap = 256 << 10

func NewScreen(cols, rows int) *Screen  { return &Screen{tail: NewTail(tailCap)} }
func (s *Screen) Write(chunk []byte)    { s.tail.Write(chunk) }
func (s *Screen) Resize(cols, rows int) {}
func (s *Screen) Answer(func([]byte))   {}
func (s *Screen) Close()                {}
func (s *Screen) Bytes() []byte         { return s.tail.Bytes() }
