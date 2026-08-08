module github.com/monstercameron/schemaflux/examples/smarttodo

go 1.25.0

require (
	github.com/charmbracelet/bubbles v0.18.0
	github.com/charmbracelet/bubbletea v0.25.0
	github.com/charmbracelet/lipgloss v0.10.0
	github.com/mattn/go-sqlite3 v1.14.49
	github.com/monstercameron/schemaflux v0.0.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/containerd/console v1.0.4-0.20230313162750-1ae8d489ac81 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.15 // indirect
	github.com/muesli/ansi v0.0.0-20211018074035-2e021307bc4b // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.15.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sahilm/fuzzy v0.1.1-0.20230530133925-c48e322e2a8f // indirect
	github.com/sashabaranov/go-openai v1.20.4 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/term v0.43.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/monstercameron/schemaflux => ../../

replace github.com/charmbracelet/bubbletea => ./wasmdeps/bubbletea

replace github.com/atotto/clipboard => ./wasmdeps/clipboard

replace github.com/containerd/console => ./wasmdeps/console
