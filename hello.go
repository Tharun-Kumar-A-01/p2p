package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const ChatProtocol = "/p2p-chat/1.0.0"
const gap = "\n\n"

// Custom message types for Bubble Tea
type (
	errMsg           error
	messageReceivedMsg string
	connectionStatusMsg string
	peerConnectedMsg   peer.ID
)

// Connection states
type connectionState int

const (
	stateDisconnected connectionState = iota
	stateConnecting
	stateConnected
	stateWaitingForConnection
)

type p2pChat struct {
	host     host.Host
	ctx      context.Context
	streams  map[peer.ID]network.Stream
	streamMu sync.RWMutex
	program  *tea.Program
}

func newP2PChat(ctx context.Context, program *tea.Program) (*p2pChat, error) {
	h, err := libp2p.New(libp2p.EnableHolePunching())
	if err != nil {
		return nil, err
	}

	chat := &p2pChat{
		host:    h,
		ctx:     ctx,
		streams: make(map[peer.ID]network.Stream),
		program: program,
	}

	h.SetStreamHandler(ChatProtocol, chat.handleIncomingStream)
	return chat, nil
}

func (c *p2pChat) handleIncomingStream(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	
	c.streamMu.Lock()
	c.streams[peerID] = s
	c.streamMu.Unlock()

	c.program.Send(peerConnectedMsg(peerID))
	
	go func() {
		defer func() {
			s.Close()
			c.streamMu.Lock()
			delete(c.streams, peerID)
			c.streamMu.Unlock()
		}()

		r := bufio.NewReader(s)
		for {
			msg, err := r.ReadString('\n')
			if err != nil {
				c.program.Send(connectionStatusMsg(fmt.Sprintf("Peer %s disconnected", peerID.ShortString())))
				return
			}
			c.program.Send(messageReceivedMsg(fmt.Sprintf("Peer %s: %s", peerID.ShortString(), strings.TrimSpace(msg))))
		}
	}()
}

func (c *p2pChat) connectToPeer(addr string) error {
	remoteAddr, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("invalid multiaddr: %w", err)
	}

	peerinfo, err := peer.AddrInfoFromP2pAddr(remoteAddr)
	if err != nil {
		return fmt.Errorf("failed to parse peer info: %w", err)
	}

	if err := c.host.Connect(c.ctx, *peerinfo); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	s, err := c.host.NewStream(c.ctx, peerinfo.ID, ChatProtocol)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	c.streamMu.Lock()
	c.streams[peerinfo.ID] = s
	c.streamMu.Unlock()

	return nil
}

func (c *p2pChat) sendMessage(message string) {
	c.streamMu.RLock()
	defer c.streamMu.RUnlock()

	for peerID, stream := range c.streams {
		if _, err := stream.Write([]byte(message + "\n")); err != nil {
			c.program.Send(connectionStatusMsg(fmt.Sprintf("Failed to send to %s: %s", peerID.ShortString(), err.Error())))
		}
	}
}

func (c *p2pChat) getAddresses() []string {
	var addrs []string
	for _, addr := range c.host.Addrs() {
		addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", addr, c.host.ID()))
	}
	return addrs
}

type appState int

const (
	stateSetup appState = iota
	stateChat
)

type model struct {
	state       appState
	connState   connectionState
	viewport    viewport.Model
	textarea    textarea.Model
	textinput   textinput.Model
	messages    []string
	senderStyle lipgloss.Style
	peerStyle   lipgloss.Style
	systemStyle lipgloss.Style
	p2p         *p2pChat
	err         error
	width       int
	height      int
}

func initialModel() model {
	// Setup text input for peer address
	ti := textinput.New()
	ti.Placeholder = "Enter peer multiaddr or press Enter to wait for connections"
	ti.Focus()
	ti.CharLimit = 400
	ti.Width = 400

	// Setup textarea for chat
	ta := textarea.New()
	ta.Placeholder = "Type your message..."
	ta.Prompt = "  ┃ "
	ta.CharLimit = 280
	ta.SetWidth(400)
	ta.SetHeight(2)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(false)

	// Setup viewport for messages
	vp := viewport.New(400, 60)

	return model{
		state:       stateSetup,
		connState:   stateDisconnected,
		textinput:   ti,
		textarea:    ta,
		viewport:    vp,
		messages:    []string{},
		senderStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
		peerStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true),
		systemStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		textarea.Blink,
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		
		if m.state == stateSetup {
			m.textinput.Width = msg.Width - 4
		} else {
			m.viewport.Width = msg.Width
			m.textarea.SetWidth(msg.Width)
			m.viewport.Height = msg.Height - m.textarea.Height() - lipgloss.Height(gap) - 2
			
			if len(m.messages) > 0 {
				m.viewport.SetContent(strings.Join(m.messages, "\n"))
			}
			m.viewport.GotoBottom()
		}

	case tea.KeyMsg:
		if m.state == stateSetup {
			switch msg.Type {
			case tea.KeyCtrlC, tea.KeyEsc:
				return m, tea.Quit
			case tea.KeyEnter:
				return m.handleSetupEnter()
			}
			
			var cmd tea.Cmd
			m.textinput, cmd = m.textinput.Update(msg)
			cmds = append(cmds, cmd)
		} else {
			switch msg.Type {
			case tea.KeyCtrlC, tea.KeyEsc:
				return m, tea.Quit
			case tea.KeyEnter:
				if m.textarea.Value() != "" {
					message := m.textarea.Value()
					m.messages = append(m.messages, m.senderStyle.Render("You: ")+message)
					m.viewport.SetContent(strings.Join(m.messages, "\n"))
					m.textarea.Reset()
					m.viewport.GotoBottom()
					
					if m.p2p != nil {
						m.p2p.sendMessage(message)
					}
				}
			}
			
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			cmds = append(cmds, cmd)
		}

	case messageReceivedMsg:
		m.messages = append(m.messages, m.peerStyle.Render(string(msg)))
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()

	case connectionStatusMsg:
		m.messages = append(m.messages, m.systemStyle.Render("System: "+string(msg)))
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()

	case peerConnectedMsg:
		peerID := peer.ID(msg)
		m.messages = append(m.messages, m.systemStyle.Render(fmt.Sprintf("System: Peer %s connected", peerID.ShortString())))
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()
		m.connState = stateConnected

	case errMsg:
		m.err = msg
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m model) handleSetupEnter() (tea.Model, tea.Cmd) {
	ctx := context.Background()
	program := tea.NewProgram(m) // This will be replaced with the actual program instance

	p2p, err := newP2PChat(ctx, program)
	if err != nil {
		m.err = err
		return m, nil
	}

	m.p2p = p2p
	peerAddr := strings.TrimSpace(m.textinput.Value())

	// Initialize chat messages with connection info
	m.messages = []string{
		m.systemStyle.Render("System: P2P Chat initialized"),
		m.systemStyle.Render(fmt.Sprintf("System: Your Peer ID: %s", p2p.host.ID())),
		m.systemStyle.Render("System: Listening addresses:"),
	}

	for _, addr := range p2p.getAddresses() {
		m.messages = append(m.messages, m.systemStyle.Render(fmt.Sprintf("System:  - %s", addr)))
	}

	if peerAddr != "" {
		m.connState = stateConnecting
		m.messages = append(m.messages, m.systemStyle.Render("System: Connecting to peer..."))
		
		go func() {
			if err := p2p.connectToPeer(peerAddr); err != nil {
				program.Send(connectionStatusMsg(fmt.Sprintf("Connection failed: %s", err.Error())))
			} else {
				program.Send(connectionStatusMsg("Connected to peer successfully"))
			}
		}()
	} else {
		m.connState = stateWaitingForConnection
		m.messages = append(m.messages, m.systemStyle.Render("System: Waiting for incoming connections..."))
	}

	m.state = stateChat
	m.textarea.Focus()
	m.viewport.SetContent(strings.Join(m.messages, "\n"))
	m.viewport.GotoBottom()

	return m, nil
}

func (m model) View() string {
	if m.state == stateSetup {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("P2P Chat - Setup"),
			"",
			"Enter the multiaddr of the peer you want to connect to,",
			"or press Enter to wait for incoming connections.",
			"",
			m.textinput.View(),
			"",
			"Press Ctrl+C to quit",
		)
	}

	statusLine := ""
	switch m.connState {
	case stateConnecting:
		statusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("● Connecting...")
	case stateConnected:
		statusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("● Connected")
	case stateWaitingForConnection:
		statusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("● Waiting for connections...")
	default:
		statusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("● Disconnected")
	}

	header := lipgloss.JoinHorizontal(
		lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Render("P2P Chat"),
		" ",
		statusLine,
	)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		m.viewport.View(),
		gap,
		m.textarea.View(),
	)
}

func main() {
	p := tea.NewProgram(
		initialModel(),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
