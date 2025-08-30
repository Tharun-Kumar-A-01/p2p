// simple_p2p_chat.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const ChatProtocol = "/p2p-chat/1.0.0"

func main() {
	ctx := context.Background()

	// 1️⃣ Create a libp2p host (generates unique peer ID)
	h, err := libp2p.New(libp2p.EnableHolePunching())
	if err != nil {
		panic(err)
	}

	fmt.Println("Your Peer ID:", h.ID())
	fmt.Println("Listening on addresses:")
	for _, addr := range h.Addrs() {
		fmt.Printf(" - %s/p2p/%s\n", addr, h.ID())
	}

	// 2️⃣ Handle incoming streams
	h.SetStreamHandler(ChatProtocol, func(s network.Stream) {
		go handleStream(s)
	})

	// 3️⃣ Ask user for remote peer address
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\nEnter remote peer multiaddr (or leave blank to wait for incoming):")
	remoteAddrStr, _ := reader.ReadString('\n')
	remoteAddrStr = strings.TrimSpace(remoteAddrStr)

	if remoteAddrStr != "" {
		remoteAddr, err := multiaddr.NewMultiaddr(remoteAddrStr)
		if err != nil {
			fmt.Println("Invalid multiaddr:", err)
			return
		}

		peerinfo, err := peer.AddrInfoFromP2pAddr(remoteAddr)
		if err != nil {
			fmt.Println("Failed to parse peer info:", err)
			return
		}

		if err := h.Connect(ctx, *peerinfo); err != nil {
			fmt.Println("Connection failed:", err)
			return
		}

		s, err := h.NewStream(ctx, peerinfo.ID, ChatProtocol)
		if err != nil {
			fmt.Println("Failed to create stream:", err)
			return
		}
		go handleStream(s)
	}

	// 4️⃣ Read stdin and send messages
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		for _, conn := range h.Network().Conns() {
			stream, err := h.NewStream(ctx, conn.RemotePeer(), ChatProtocol)
			if err != nil {
				fmt.Println("Stream error:", err)
				continue
			}
			_, _ = stream.Write([]byte(text + "\n"))
		}
	}
}

// handleStream reads incoming messages
func handleStream(s network.Stream) {
	defer s.Close()
	r := bufio.NewReader(s)
	for {
		msg, err := r.ReadString('\n')
		if err != nil {
			fmt.Println("Connection closed:", s.Conn().RemotePeer())
			return
		}
		fmt.Printf("\nPeer : %s \n >", strings.TrimSpace(msg))
	}
}
