// QP-017: run with `go run .audit/windows_arm64_runtime_repro.go` on Windows
// ARM64. The program uses only the standard library and should print
// "completed". On the affected host, Go 1.27.3 crashes in the runtime before
// reaching that line; the same program completes under windows/amd64.
package main

import "fmt"

func main() {
	for range 100000 {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			var p *struct{ x int }
			_ = p.x
		}()
		<-done
	}
	fmt.Println("completed")
}
