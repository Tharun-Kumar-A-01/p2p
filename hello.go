// p2p_chat_ui.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Protocol constant
const ChatProtocol = "/p2p-chat/1.0.0"

// Message types for the UI
type Message struct {
	Content   string
	From      string
	Timestamp time.Time
	IsLocal   bool
}

type ConnectionStatus struct {
	PeerID    string
	Connected bool
}

type copiedMsg bool

// Custom tea.Msg types
type incomingMsg Message
type connectionUpdate ConnectionStatus
type hostReady struct {
	host host.Host
	addr string // single IPv6 multiaddr
}

// Styles
var (
	appStyle   = lipgloss.NewStyle().Padding(1, 2)
	titleStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("99")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("236"))
	localMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("86")).
			Bold(true)
	remoteMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))
	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true)
	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
)

type model struct {
	host         host.Host
	ctx          context.Context
	messages     []Message
	viewport     viewport.Model
	textarea     textarea.Model
	ready        bool
	width        int
	height       int
	peerID       string
	listenAddr   string // single IPv6 multiaddr
	connectedTo  map[string]bool
	connectInput string
	state        string // "chat" or "connect"
	copied       bool   // was just copied
}

func initialModel(ctx context.Context) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Focus()
	ta.Prompt = "│ "
	ta.CharLimit = 280
	ta.SetWidth(30)
	ta.SetHeight(3)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false

	vp := viewport.New(30, 20)
	vp.SetContent("")

	return model{
		ctx:         ctx,
		textarea:    ta,
		viewport:    vp,
		messages:    []Message{},
		connectedTo: make(map[string]bool),
		state:       "connect",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.initP2P(),
	)
}

func (m model) initP2P() tea.Cmd {
	return func() tea.Msg {
		h, err := libp2p.New(libp2p.EnableHolePunching())
		if err != nil {
			return Message{
				Content:   fmt.Sprintf("Failed to create host: %v", err),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			}
		}

		// Prefer IPv6 multiaddr
		var ipv6Addr string
		addr := h.Addrs()[len(h.Addrs())-1]

		ipv6Addr = fmt.Sprintf("%s/p2p/%s", addr, h.ID())

		if ipv6Addr == "" {
			// fallback to any addr if no IPv6 found
			ipv6Addr = fmt.Sprintf("%s/p2p/%s", h.Addrs()[0], h.ID())
		}

		return hostReady{
			host: h,
			addr: ipv6Addr,
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		taCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, taCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		if !m.ready {
			m.viewport = viewport.New(msg.Width-4, msg.Height-10)
			m.viewport.YPosition = 3
			m.viewport.HighPerformanceRendering = false
			m.textarea.SetWidth(msg.Width - 4)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - 10
			m.textarea.SetWidth(msg.Width - 4)
		}

		m.viewport.SetContent(m.renderMessages())

	case hostReady:
		m.host = msg.host
		m.peerID = msg.host.ID().String()
		m.listenAddr = msg.addr

		m.host.SetStreamHandler(ChatProtocol, func(s network.Stream) {
			go m.handleStream(s)
		})

		m.messages = append(m.messages, Message{
			Content:   fmt.Sprintf("Host initialized. Your Peer ID: %s", m.peerID),
			From:      "System",
			Timestamp: time.Now(),
			IsLocal:   false,
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()

	case incomingMsg:
		m.messages = append(m.messages, Message(msg))
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()

	case connectionUpdate:
		if msg.Connected {
			m.connectedTo[msg.PeerID] = true
			m.messages = append(m.messages, Message{
				Content:   fmt.Sprintf("Connected to peer: %s", msg.PeerID),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			})
		} else {
			delete(m.connectedTo, msg.PeerID)
			m.messages = append(m.messages, Message{
				Content:   fmt.Sprintf("Disconnected from peer: %s", msg.PeerID),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			})
		}
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyCtrlN:
			m.state = "connect"
			m.textarea.Reset()
			m.textarea.Placeholder = "Enter peer multiaddr..."
			m.textarea.Focus()

		case tea.KeyCtrlD:
			if m.state == "connect" {
				m.state = "chat"
				m.textarea.Reset()
				m.textarea.Placeholder = "Type a message..."
				m.textarea.Focus()
			}

		case tea.KeyEnter:
			if m.state == "connect" {
				addr := strings.TrimSpace(m.textarea.Value())
				if addr != "" {
					cmd := m.connectToPeer(addr)
					m.state = "chat"
					m.textarea.Reset()
					m.textarea.Placeholder = "Type a message..."
					return m, cmd
				}
			} else {
				text := strings.TrimSpace(m.textarea.Value())
				if text != "" {
					m.messages = append(m.messages, Message{
						Content:   text,
						From:      "You",
						Timestamp: time.Now(),
						IsLocal:   true,
					})

					if m.host != nil {
						go m.broadcastMessage(text)
					}

					m.textarea.Reset()
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
				}
			}

		// Clipboard copy hotkey
		case tea.KeyRunes:
			if msg.String() == "c" &&
				m.state == "chat" &&
				m.listenAddr != "" {
				clipboard.WriteAll(m.listenAddr)
				m.copied = true
				// Reset message after 2 seconds
				return m, func() tea.Msg {
					time.Sleep(2 * time.Second)
					return copiedMsg(false)
				}
			}
		}

	case copiedMsg:
		m.copied = bool(msg)
	}

	return m, tea.Batch(taCmd, vpCmd)
}

func (m model) connectToPeer(addrStr string) tea.Cmd {
	return func() tea.Msg {
		remoteAddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			return incomingMsg{
				Content:   fmt.Sprintf("Invalid multiaddr: %v", err),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			}
		}
		peerinfo, err := peer.AddrInfoFromP2pAddr(remoteAddr)
		if err != nil {
			return incomingMsg{
				Content:   fmt.Sprintf("Failed to parse peer info: %v", err),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			}
		}
		if err := m.host.Connect(m.ctx, *peerinfo); err != nil {
			return incomingMsg{
				Content:   fmt.Sprintf("Connection failed: %v", err),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			}
		}
		s, err := m.host.NewStream(m.ctx, peerinfo.ID, ChatProtocol)
		if err != nil {
			return incomingMsg{
				Content:   fmt.Sprintf("Failed to create stream: %v", err),
				From:      "System",
				Timestamp: time.Now(),
				IsLocal:   false,
			}
		}
		go m.handleStream(s)
		return connectionUpdate{
			PeerID:    peerinfo.ID.String(),
			Connected: true,
		}
	}
}

func (m model) broadcastMessage(text string) {
	for _, conn := range m.host.Network().Conns() {
		stream, err := m.host.NewStream(m.ctx, conn.RemotePeer(), ChatProtocol)
		if err != nil {
			continue
		}
		defer stream.Close()
		stream.Write([]byte(text + "\n"))
	}
}

func (m model) handleStream(s network.Stream) {
	defer s.Close()
	r := bufio.NewReader(s)
	peerID := s.Conn().RemotePeer().String()

	for {
		msg, err := r.ReadString('\n')
		if err != nil {
			// Connection closed
			p := tea.NewProgram(nil)
			p.Send(connectionUpdate{
				PeerID:    peerID,
				Connected: false,
			})
			return
		}
		// Send message to UI
		p := tea.NewProgram(nil)
		p.Send(incomingMsg{
			Content:   strings.TrimSpace(msg),
			From:      fmt.Sprintf("Peer[%s]", peerID[:8]),
			Timestamp: time.Now(),
			IsLocal:   false,
		})
	}
}

func (m model) renderMessages() string {
	var sb strings.Builder

	for _, msg := range m.messages {
		timestamp := msg.Timestamp.Format("15:04:05")

		var style lipgloss.Style
		if msg.From == "System" {
			style = systemMsgStyle
		} else if msg.IsLocal {
			style = localMsgStyle
		} else {
			style = remoteMsgStyle
		}

		line := fmt.Sprintf("[%s] %s: %s", timestamp, msg.From, msg.Content)
		sb.WriteString(style.Render(line) + "\n")
	}

	return sb.String()
}

func (m model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	// Title
	title := titleStyle.Render(" 🌐 P2P Chat ")

	// Status bar
	var status string
	if m.host != nil {
		connCount := len(m.connectedTo)
		status = fmt.Sprintf(" ID: %s | Peers: %d | Ctrl+N: Connect | Ctrl+C: Quit",
			m.peerID[:8], connCount)
	} else {
		status = " Initializing P2P host..."
	}
	statusBar := statusBarStyle.Copy().Width(m.width - 4).Render(status)

	// Mode indicator
	modeText := ""
	if m.state == "connect" {
		modeText = helpStyle.Render("📡 Enter peer multiaddr and press Enter (Ctrl+D to cancel)")
	}

	// Listen address + copy button/hotkey
	addrText := ""
	if m.listenAddr != "" && m.state == "chat" {
		line := fmt.Sprintf("Your address: %s   [c] Copy", m.listenAddr)
		if m.copied {
			line += " (Copied!)"
		}
		addrText = helpStyle.Render(line)
	}

	return appStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			title,
			m.viewport.View(),
			modeText,
			addrText,
			m.textarea.View(),
			statusBar,
		),
	)
}

func main() {
	ctx := context.Background()
	p := tea.NewProgram(
		initialModel(ctx),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
