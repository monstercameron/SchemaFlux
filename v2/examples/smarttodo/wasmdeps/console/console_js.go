//go:build js && wasm

package console

import (
	"errors"
	"io"
)

var ErrNotAConsole = errors.New("provided file is not a console")

type File interface {
	io.ReadWriteCloser
	Fd() uintptr
	Name() string
}

type Console interface {
	File
	Resize(WinSize) error
	ResizeFrom(Console) error
	SetRaw() error
	DisableEcho() error
	Reset() error
	Size() (WinSize, error)
}

type WinSize struct {
	Height uint16
	Width  uint16
	x      uint16
	y      uint16
}

type fakeConsole struct{}

func (fakeConsole) Read([]byte) (int, error)    { return 0, io.EOF }
func (fakeConsole) Write(p []byte) (int, error) { return len(p), nil }
func (fakeConsole) Close() error                { return nil }
func (fakeConsole) Fd() uintptr                 { return 0 }
func (fakeConsole) Name() string                { return "wasm-console" }
func (fakeConsole) Resize(WinSize) error        { return nil }
func (fakeConsole) ResizeFrom(Console) error    { return nil }
func (fakeConsole) SetRaw() error               { return nil }
func (fakeConsole) DisableEcho() error          { return nil }
func (fakeConsole) Reset() error                { return nil }
func (fakeConsole) Size() (WinSize, error)      { return WinSize{Width: 120, Height: 40}, nil }
func Current() Console                          { return fakeConsole{} }
func ConsoleFromFile(File) (Console, error)     { return fakeConsole{}, nil }
