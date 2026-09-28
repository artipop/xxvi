// Package ptyhold keeps terminals alive while the application is not: a small
// process of its own — the application's executable started as `xxvi
// pty-hold` — owns every pty, and the application talks to it over a unix
// socket. Closing the application lets go of the socket, not of the shells,
// and the next start finds them where they were, with what they printed
// meanwhile.
//
// One holder per data folder, serving one application at a time; every
// session's bytes go over one connection, in frames (protocol.go).
//
// Derived from internal/holder of hrdx (https://github.com/patriceckhart/hrdx,
// commit dd0010875091670536caf6f806b83036be055995), used under the MIT license:
//
//	Copyright (c) 2026 Patric Eckhart
//
//	Permission is hereby granted, free of charge, to any person obtaining a
//	copy of this software and associated documentation files (the "Software"),
//	to deal in the Software without restriction, including without limitation
//	the rights to use, copy, modify, merge, publish, distribute, sublicense,
//	and/or sell copies of the Software, and to permit persons to whom the
//	Software is furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in
//	all copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
//	FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
//	DEALINGS IN THE SOFTWARE.
package ptyhold
