package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/timer"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const appWidth = 56
const chatMessagesMax = 12

type initConnMsg struct {
	ctx  context.Context
	conn *websocket.Conn
}
type successSentMsg struct{}
type errMsg struct{ err error }

type wsChatMsg struct{ content string }
type wsOngoingRoundInfoMsg struct{ content map[string]any }
type wsFinishedRoundInfoMsg struct{ content map[string]any }
type wsFinishedGameMsg struct{}
type wsPongMsg struct{}
type wsErrMsg struct{ err error }

type model struct {
	ctx          context.Context
	conn         *websocket.Conn
	err          error
	timer        timer.Model
	textInput    textinput.Model
	chatMessages []string
	wordBoxGuide string
	wordBox      string
}

func initialModel() model {
	ti := textinput.New()
	ti.Placeholder = "connecting..."
	ti.Focus()
	ti.Width = appWidth - 2

	return model{
		textInput:    ti,
		wordBoxGuide: "WAITING ROUND START!",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tea.ClearScreen, textinput.Blink, connectToWsServer)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "ctrl+e":
			m.err = nil

		case "enter":
			trimmedInput := strings.TrimSpace(m.textInput.Value())

			if trimmedInput == "/exit" {
				return m, tea.Quit
			}

			if trimmedInput == "/clear" {
				m.chatMessages = []string{}
				m.textInput.SetValue("")
			} else if m.conn != nil && trimmedInput != "" {
				cmds = append(cmds, sendToWsServer(m.ctx, m.conn, trimmedInput))
			}
		}

	case timer.TickMsg:
		var cmd tea.Cmd

		m.timer, cmd = m.timer.Update(msg)
		cmds = append(cmds, cmd)

	case initConnMsg:
		m.textInput.Placeholder = "message/answer here, send with Enter"
		m.ctx = msg.ctx
		m.conn = msg.conn

		cmds = append(
			cmds,
			listenToWsServer(m.ctx, m.conn),
			periodicallyPingWsServer(m.ctx, m.conn),
		)

	case errMsg:
		m.err = msg.err

	case successSentMsg:
		m.textInput.SetValue("")

	case wsPongMsg:
		cmds = append(cmds, listenToWsServer(m.ctx, m.conn))

	case wsErrMsg:
		m.err = msg.err

		cmds = append(cmds, listenToWsServer(m.ctx, m.conn))

	case wsChatMsg:
		m.chatMessages = append(m.chatMessages, msg.content)

		if len(m.chatMessages) > chatMessagesMax {
			m.chatMessages = m.chatMessages[1:]
		}

		cmds = append(cmds, listenToWsServer(m.ctx, m.conn))

	case wsOngoingRoundInfoMsg:
		m.wordBoxGuide = "PLEASE GUESS!"
		m.wordBox = msg.content["word_to_guess"].(string)

		roundFinishTime, err := time.Parse(time.RFC3339, msg.content["round_finish_time"].(string))
		if err != nil {
			m.err = err
			roundFinishTime = time.Now()
		}

		m.timer = timer.NewWithInterval(time.Until(roundFinishTime), 100*time.Millisecond)

		cmds = append(
			cmds,
			listenToWsServer(m.ctx, m.conn),
			m.timer.Init(),
		)

	case wsFinishedRoundInfoMsg:
		m.wordBoxGuide = "TIME'S UP! THE ANSWER:"
		m.wordBox = msg.content["word_answer"].(string)

		toNextRoundTime, err := time.Parse(time.RFC3339, msg.content["to_next_round_time"].(string))
		if err != nil {
			m.err = err
			toNextRoundTime = time.Now()
		}

		m.timer = timer.NewWithInterval(time.Until(toNextRoundTime), 100*time.Millisecond)

		cmds = append(
			cmds,
			listenToWsServer(m.ctx, m.conn),
			m.timer.Init(),
		)

	case wsFinishedGameMsg:
		m.wordBoxGuide = "WAITING ROUND START!"
		m.wordBox = ""

		cmds = append(cmds, listenToWsServer(m.ctx, m.conn))
	}

	var cmd tea.Cmd

	m.textInput, cmd = m.textInput.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

var headerStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("26")).
	Foreground(lipgloss.Color("255")).
	Width(appWidth).
	Align(lipgloss.Center).
	PaddingTop(1)
var mainStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("26")).
	Foreground(lipgloss.Color("255")).
	Width(appWidth).
	Align(lipgloss.Center).
	PaddingBottom(1).
	Bold(true)
var messageTopBottomStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("68"))
var messageStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder(), false, true).
	BorderForeground(lipgloss.Color("68")).
	Width(appWidth - 2).
	Height(chatMessagesMax).
	AlignVertical(lipgloss.Bottom).
	PaddingLeft(1).
	PaddingRight(1).
	Transform(func(s string) string {
		lines := strings.Split(s, "\n")

		return strings.Join(lines[max(0, len(lines)-chatMessagesMax):], "\n")
	})
var errorStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("9")).
	Width(appWidth)
var hotkeyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("8")).
	Bold(true)
var hotkeyTooltipStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("8"))

func (m model) View() string {
	timeout := m.timer.Timeout.Round(100 * time.Millisecond)

	s := "\n"

	if m.timer.Timedout() {
		s += headerStyle.Render(m.wordBoxGuide)
	} else {
		s += headerStyle.Render(fmt.Sprintf(
			"%s - %d.%ds",
			m.wordBoxGuide,
			timeout.Milliseconds()/1000,
			(timeout.Milliseconds()-timeout.Milliseconds()/1000*1000)/100,
		))
	}
	s += "\n"

	s += mainStyle.Render(fmt.Sprintf("'%s'", m.wordBox))
	s += "\n"

	s += messageTopBottomStyle.Render(fmt.Sprintf("╭%s╮", strings.Repeat("─", appWidth-2)))
	s += "\n"
	s += messageStyle.Render(strings.Join(m.chatMessages, "\n"))
	s += "\n"
	s += messageTopBottomStyle.Render(fmt.Sprintf("╰%s╯", strings.Repeat("─", appWidth-2)))
	s += "\n"

	s += m.textInput.View()
	s += "\n"

	if m.err != nil {
		s += errorStyle.Render(m.err.Error())
		s += "\n"
	}

	s += "\n"
	s += hotkeyStyle.Render("Ctrl+C")
	s += hotkeyTooltipStyle.Render(" exit  ")
	s += hotkeyStyle.Render("Ctrl+E")
	s += hotkeyTooltipStyle.Render(" clear errors")
	s += "\n"

	// Send the UI for rendering
	return s
}

func connectToWsServer() tea.Msg {
	ctx := context.Background()

	var link string
	if len(os.Args) < 2 {
		link = "wss://mc.chenk.my.id:3000/ws/anagram/1"
	} else {
		link = os.Args[1]
	}

	conn, _, err := websocket.Dial(ctx, link, nil)
	if err != nil {
		return errMsg{fmt.Errorf("websocket.Dial: %v", err)}
	}

	return initConnMsg{ctx, conn}
}

func listenToWsServer(ctx context.Context, conn *websocket.Conn) tea.Cmd {
	return func() tea.Msg {
		// so the CPU doesn't get too busy
		time.Sleep(time.Millisecond)

		var v map[string]interface{}

		err := wsjson.Read(ctx, conn, &v)
		if err != nil {
			return wsErrMsg{fmt.Errorf("wsjson.Read: %v", err)}
		}

		switch v["type"] {
		case "ChatMessage":
			content := v["content"].(string)
			return wsChatMsg{content}
		case "OngoingRoundInfo":
			content := v["content"].(map[string]any)
			return wsOngoingRoundInfoMsg{content}
		case "FinishedRoundInfo":
			content := v["content"].(map[string]any)
			return wsFinishedRoundInfoMsg{content}
		case "FinishedGame":
			return wsFinishedGameMsg{}
		case "PongMessage":
			return wsPongMsg{}
		default:
			return wsErrMsg{fmt.Errorf("unknown message type: %s", v["type"])}
		}
	}
}

func sendToWsServer(ctx context.Context, conn *websocket.Conn, msg string) tea.Cmd {
	return func() tea.Msg {
		if msg == "/ping" {
			return errMsg{fmt.Errorf(
				"don't ping manually! this is handled automatically by the client",
			)}
		}

		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		if err != nil {
			return errMsg{fmt.Errorf("c.Write: %v", err)}
		}

		return successSentMsg{}
	}
}

func periodicallyPingWsServer(ctx context.Context, conn *websocket.Conn) tea.Cmd {
	return func() tea.Msg {
		for {
			time.Sleep(10*time.Second)

			err := conn.Write(ctx, websocket.MessageText, []byte("/ping"))
			if err != nil {
				return errMsg{fmt.Errorf("c.Write: %v", err)}
			}
		}
	}
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
}
