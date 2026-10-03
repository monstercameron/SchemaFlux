//go:build js && wasm

package tea

import "os"

func (p *Program) initInput() error { return nil }

func (p *Program) restoreInput() error { return nil }

func openInputTTY() (*os.File, error) { return nil, nil }
