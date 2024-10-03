package main

import (
	"fmt"
	"os"

	"SEHO/internal/config"
	"SEHO/internal/logging"
	"SEHO/internal/music"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/redis/go-redis/v9"
)

type model struct {
	choices  []string
	cursor   int
	selected string
	cfg      config.Config
	rdb      *redis.Client
	spinner  spinner.Model
	scanning bool
	result   string
}

func initialModel() model {
	cfg := config.LoadConfig()
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddress,
	})

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return model{
		choices:  []string{"Scan Directory", "Quit"},
		cfg:      cfg,
		rdb:      rdb,
		spinner:  s,
		scanning: false,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.choices[m.cursor]
			switch m.selected {
			case "Scan Directory":
				m.scanning = true
				m.result = ""
				return m, m.startScanning
			case "Quit":
				return m, tea.Quit
			}
		}
	case scanResultMsg:
		m.scanning = false
		m.result = string(msg)
		return m, nil
	}

	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m model) View() string {
	s := "Music Monitor\n\n"

	for i, choice := range m.choices {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}
		s += fmt.Sprintf("%s %s\n", cursor, choice)
	}

	s += "\n"

	if m.scanning {
		s += fmt.Sprintf("%s Scanning directory...\n", m.spinner.View())
	} else if m.result != "" {
		s += m.result + "\n"
	}

	s += "\nPress q to quit.\n"

	return s
}

type scanResultMsg string

func (m model) startScanning() tea.Msg {
	filesAdded, err := music.ScanDirectory(m.cfg.MusicDirectory, m.rdb)
	if err != nil {
		return scanResultMsg(fmt.Sprintf("Error scanning directory: %v", err))
	}
	if filesAdded == 0 {
		return scanResultMsg("No new files found in the directory")
	}
	return scanResultMsg(fmt.Sprintf("Total files added: %d", filesAdded))
}

func main() {
	cleanup := logging.SetupLogger()
	defer cleanup()

	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
		os.Exit(1)
	}
}