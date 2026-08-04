//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall/js"

	tea "github.com/charmbracelet/bubbletea"
	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/database"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/localization"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/tui"
)

var (
	funcs     []js.Func
	startOnce sync.Once
	inputPipe *io.PipeWriter
	program   *tea.Program
	currentW  = 100
	currentH  = 36
	connected bool
	feedCount int
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("boot", promiseFunc(boot))
	api.Set("connect", promiseFunc(connect))
	api.Set("feed", js.FuncOf(feed))
	api.Set("resize", js.FuncOf(resize))
	js.Global().Set("smarttodoWasm", api)
	select {}
}

func promiseFunc(fn func([]js.Value) (string, error)) js.Func {
	promiseHandler := js.FuncOf(func(this js.Value, args []js.Value) any {
		executor := js.FuncOf(func(this js.Value, promiseArgs []js.Value) any {
			resolve := promiseArgs[0]
			reject := promiseArgs[1]
			go func(callArgs []js.Value) {
				result, err := fn(callArgs)
				if err != nil {
					reject.Invoke(err.Error())
					return
				}
				resolve.Invoke(result)
			}(append([]js.Value(nil), args...))
			return nil
		})
		funcs = append(funcs, executor)
		return js.Global().Get("Promise").New(executor)
	})
	funcs = append(funcs, promiseHandler)
	return promiseHandler
}

func boot(args []js.Value) (string, error) {
	goConsole("boot", "wasm runtime initialized")
	return marshal(map[string]string{
		"message": "Smart Todo wasm runtime ready.",
	})
}

func connect(args []js.Value) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("api key is required")
	}
	apiKey := args[0].String()
	if apiKey == "" {
		return "", fmt.Errorf("api key is required")
	}
	if len(args) >= 3 {
		currentW = max(40, args[1].Int())
		currentH = max(16, args[2].Int())
	}
	goConsole("connect", fmt.Sprintf("starting TUI at %dx%d", currentW, currentH))

	schemaflux.Init(apiKey)
	localization.InitLocalization()
	go localization.PreloadCommonStrings()

	var connectErr error
	startOnce.Do(func() {
		var db *database.Database
		db, connectErr = database.NewDatabase("browser")
		if connectErr != nil {
			return
		}

		model := tui.InitialModel(db)
		model.SetNeedsAPIKey(false)

		reader, writer := io.Pipe()
		inputPipe = writer
		program = tea.NewProgram(
			model,
			tea.WithInput(reader),
			tea.WithOutput(os.Stdout),
			tea.WithoutSignals(),
		)

		go func() {
			goConsole("program", "Bubble Tea program loop starting")
			_, _ = program.Run()
			goConsole("program", "Bubble Tea program loop exited")
		}()
	})
	if connectErr != nil {
		return "", connectErr
	}

	connected = true
	goConsole("connect", "TUI connected")
	if program != nil {
		program.Send(tea.WindowSizeMsg{Width: currentW, Height: currentH})
	}
	return marshal(map[string]string{
		"message": "Smart Todo TUI started.",
	})
}

func feed(this js.Value, args []js.Value) any {
	if !connected || inputPipe == nil || len(args) == 0 {
		return nil
	}
	feedCount++
	if feedCount <= 5 {
		goConsole("input", fmt.Sprintf("received terminal input chunk %d", feedCount))
	}
	_, _ = inputPipe.Write([]byte(args[0].String()))
	return nil
}

func resize(this js.Value, args []js.Value) any {
	if len(args) >= 2 {
		currentW = max(40, args[0].Int())
		currentH = max(16, args[1].Int())
	}
	goConsole("resize", fmt.Sprintf("window size %dx%d", currentW, currentH))
	if connected && program != nil {
		program.Send(tea.WindowSizeMsg{Width: currentW, Height: currentH})
	}
	return nil
}

func goConsole(event, message string) {
	console := js.Global().Get("console")
	if console.IsUndefined() || console.IsNull() {
		return
	}
	console.Call("log", "[smarttodo-go]", event, message)
}

func marshal(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
