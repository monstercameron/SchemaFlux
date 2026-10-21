//go:build js && wasm

package tea

func (p *Program) listenForResize(done chan struct{}) {
	<-done
}
