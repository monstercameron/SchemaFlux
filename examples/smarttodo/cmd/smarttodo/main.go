package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/database"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/localization"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/tui"
)

func main() {
	os.Setenv("SCHEMAFLUX_DEBUG", "false")
	loadNearestEnv()

	needsAPIKey := false
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("SCHEMAFLUX_API_KEY")
	}
	if apiKey == "" {
		needsAPIKey = true
		schemaflux.Init("")
	} else if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Println("Please ensure your .env file contains:")
		fmt.Println("  OPENAI_API_KEY=your-api-key")
		fmt.Println("Or set the environment variable:")
		fmt.Println("  export SCHEMAFLUX_API_KEY='your-api-key'")
		os.Exit(1)
	}

	localization.InitLocalization()
	go localization.PreloadCommonStrings()

	logFile, err := os.OpenFile(filepath.Join(os.TempDir(), "smarttodo.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		defer logFile.Close()
	}

	dbPath := os.Getenv("SMARTTODO_DB")
	if dbPath == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			schemaflux.GetLogger().Error("Failed to get home directory", "error", homeErr)
			os.Exit(1)
		}
		dbPath = filepath.Join(home, ".smarttodo.db")
	}

	db, err := database.NewDatabase(dbPath)
	if err != nil {
		schemaflux.GetLogger().Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	model := tui.InitialModel(db)
	model.SetNeedsAPIKey(needsAPIKey)

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	go func() {
		sig := <-sigChan
		schemaflux.GetLogger().Info("Received signal, initiating graceful shutdown", "signal", sig)
		p.Send(models.StartClosingMsg{})
		go func() {
			time.Sleep(3 * time.Second)
			p.Send(tea.Quit())
		}()
	}()

	if _, err := p.Run(); err != nil {
		schemaflux.GetLogger().Error("Error running program", "error", err)
		if db != nil {
			db.Close()
		}
		os.Exit(1)
	}

	schemaflux.GetLogger().Info("SchemaFlux CommandDeck closed successfully")
}

func loadNearestEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}

	for {
		envPath := filepath.Join(dir, ".env")
		if data, readErr := os.ReadFile(envPath); readErr == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				key := strings.TrimSpace(parts[0])
				value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
				if os.Getenv(key) == "" {
					_ = os.Setenv(key, value)
				}
			}
			return
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
