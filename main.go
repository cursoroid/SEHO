package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/redis/go-redis/v9"
)

type item struct{ title, desc, path string }

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

var menu = []list.Item{
	item{title: "Scan Directory", desc: "Index new music files"},
	item{title: "Browse Library", desc: "Pick a track and play it"},
	item{title: "Stop Playback", desc: "Stop the current track"},
	item{title: "Quit", desc: "Exit"},
}

type statusMsg string

type model struct {
	list     list.Model
	rdb      *redis.Client
	dir      string
	player   *player
	status   string
	browsing bool
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.player.stop()
			return m, tea.Quit
		case "esc":
			if m.browsing {
				return m.showMenu(), nil
			}
		case "enter":
			sel, ok := m.list.SelectedItem().(item)
			if !ok {
				break
			}
			if m.browsing {
				if err := m.player.play(sel.path); err != nil {
					m.status = fmt.Sprintf("Playback failed: %v", err)
				} else {
					m.status = "Playing: " + sel.title
				}
				return m, nil
			}
			return m.menuAction(sel.title)
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.status == "" {
		return m.list.View()
	}
	return m.list.View() + "\n" + m.status
}

func (m model) menuAction(title string) (tea.Model, tea.Cmd) {
	switch title {
	case "Scan Directory":
		m.status = "Scanning " + m.dir + "..."
		return m, scanCmd(m.dir, m.rdb)

	case "Browse Library":
		items, err := listTracks(context.Background(), m.rdb)
		switch {
		case err != nil:
			m.status = fmt.Sprintf("Could not read library: %v", err)
		case len(items) == 0:
			m.status = "Library is empty - scan first"
		default:
			m.browsing = true
			m.status = ""
			m.list.Title = "Library (esc to go back)"
			m.list.SetItems(items)
		}
		return m, nil

	case "Stop Playback":
		m.player.stop()
		m.status = "Stopped"
		return m, nil

	case "Quit":
		m.player.stop()
		return m, tea.Quit
	}
	return m, nil
}

func (m model) showMenu() model {
	m.browsing = false
	m.status = ""
	m.list.Title = "SEHO"
	m.list.SetItems(menu)
	return m
}

func scanCmd(dir string, rdb *redis.Client) tea.Cmd {
	return func() tea.Msg {
		n, err := scanDirectory(context.Background(), dir, rdb)
		if err != nil {
			return statusMsg(fmt.Sprintf("Scan failed: %v", err))
		}
		return statusMsg(fmt.Sprintf("Indexed %d new file(s)", n))
	}
}

// setupLog sends log output to logs/seho.log, or nowhere if that is not writable.
// ponytail: never stderr - this is an alt-screen TUI and stray writes corrupt the render.
func setupLog() func() {
	if err := os.MkdirAll("logs", 0o755); err == nil {
		f, err := os.OpenFile(filepath.Join("logs", "seho.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			log.SetOutput(f)
			return func() { f.Close() }
		}
	}
	log.SetOutput(io.Discard)
	return func() {}
}

func defaultMusicDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Music")
}

func main() {
	closeLog := setupLog()
	defer closeLog()

	dir := cmp.Or(os.Getenv("MUSIC_DIR"), defaultMusicDir())
	rdb := redis.NewClient(&redis.Options{Addr: cmp.Or(os.Getenv("REDIS_ADDR"), "localhost:6379")})
	defer rdb.Close()

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "redis unreachable: %v\n", err)
		os.Exit(1)
	}

	l := list.New(menu, list.NewDefaultDelegate(), 0, 0)
	l.Title = "SEHO"

	m := model{list: l, rdb: rdb, dir: dir, player: &player{}}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
