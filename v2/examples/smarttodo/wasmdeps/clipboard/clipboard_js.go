//go:build js && wasm

package clipboard

func readAll() (string, error) { return "", nil }

func writeAll(string) error { return nil }
