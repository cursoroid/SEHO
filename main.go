package main

import (
	"fmt"
	"os"

	"SEHO/internal/config"
	"SEHO/internal/logging"
	"SEHO/internal/music"
	"SEHO/internal/streaming"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/redis/go-redis/v9"
)

type item struct {
	title, desc string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

type model struct {
	list     list.Model
	cfg      config.Config
	rdb      *redis.Client
	streamer *streaming.Streamer
	spinner  spinner.Model
	scanning bool
	result   string
	playing  bool
	current  string
}

func initialModel() model {
	cfg := config.LoadConfig()
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddress,
	})

	items := []list.Item{
		item{title: "Scan Directory", desc: "Scan for new music files"},
		item{title: "List All Music", desc: "Select the music from the list"},
		item{title: "Stream Music", desc: "Select and stream a music file"},
		item{title: "Stop Streaming", desc: "Stop the currently playing music"},
		item{title: "Quit", desc: "Exit the application"},
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "SEHO Music Server"

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))

	streamer := streaming.NewStreamer(rdb, cfg.MusicDirectory)

	return model{
		list:     l,
		cfg:      cfg,
		rdb:      rdb,
		streamer: streamer,
		spinner:  s,
		scanning: false,
		playing:  false,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.list.SetHeight(msg.Height - 4)
		return m, nil

	case tea.KeyMsg:
		switch keypress := msg.String(); keypress {
		case "ctrl+c":
			m.streamer.StopStreaming()
			return m, tea.Quit
		case "enter":
			i, ok := m.list.SelectedItem().(item)
			if ok {
				switch i.title {
				case "Scan Directory":
					m.scanning = true
					m.result = ""
					return m, m.startScanning
				case "Stream Music":
					return m, m.startStreaming()
				case "Stop Streaming":
					m.streamer.StopStreaming()
					m.playing = false
					m.current = ""
					return m, nil
				case "Quit":
					m.streamer.StopStreaming()
					return m, tea.Quit
				}
			}
		}
	
	case scanResultMsg:
		m.scanning = false
		m.result = string(msg)
		return m, nil
	
	case streamResultMsg:
		m.playing = msg.playing
		m.current = msg.current
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	s := m.list.View() + "\n"

	if m.scanning {
		s += fmt.Sprintf("%s Scanning directory...\n", m.spinner.View())
	} else if m.result != "" {
		s += m.result + "\n"
	}

	if m.playing {
		s += fmt.Sprintf("Now playing: %s\n", m.current)
	}

	return s
}

type scanResultMsg string

type streamResultMsg struct {
	playing bool
	current string
}

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

func (m model) startStreaming() tea.Cmd {
	return func() tea.Msg {
		playing, current, err := m.streamer.StreamMusic()
		if err != nil {
			return streamResultMsg{playing: false, current: fmt.Sprintf("Error: %v", err)}
		}
		fmt.Printf("Debug: Streaming started. Playing: %v, Current: %s\n", playing, current)
		
		return streamResultMsg{playing: playing, current: current}
	}
}

func main() {
	cleanup := logging.SetupLogger()
	defer cleanup()

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
		os.Exit(1)
	}
}