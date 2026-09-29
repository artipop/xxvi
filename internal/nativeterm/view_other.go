//go:build !darwin

package nativeterm

import "unsafe"

// Available is false: native terminals are drawn by Ghostty's macOS library.
func Available() bool { return false }

func Show(unsafe.Pointer, string, string, float64, float64, float64, float64, float64, float64) {}
func Hide(string)                                                                               {}
func Close(string)                                                                              {}
func CloseAll()                                                                                 {}
func Focus(string)                                                                              {}
