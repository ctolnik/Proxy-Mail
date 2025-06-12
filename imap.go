package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
)

type IMAPServer struct {
	config   *Config
	listener net.Listener
	wg       sync.WaitGroup
	stopping bool
}

func NewIMAPServer(config *Config) *IMAPServer {
	return &IMAPServer{
		config: config,
	}
}

func (s *IMAPServer) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Local.IMAP.Host, s.config.Local.IMAP.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start IMAP server on %s: %w", addr, err)
	}

	s.listener = listener
	log.Printf("IMAP proxy server listening on %s", addr)

	for !s.stopping {
		conn, err := listener.Accept()
		if err != nil {
			if s.stopping {
				break
			}
			log.Printf("IMAP accept error: %v", err)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}

	s.wg.Wait()
	return nil
}

func (s *IMAPServer) Stop() error {
	s.stopping = true
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *IMAPServer) handleConnection(localConn net.Conn) {
	defer s.wg.Done()
	defer localConn.Close()

	log.Printf("IMAP client connected from %s", localConn.RemoteAddr())

	// Get the first available IMAP server config
	serverConfig := s.config.GetServerByProtocol("imap")
	if serverConfig == nil || serverConfig.IMAP == nil {
		log.Printf("No IMAP server configuration found")
		fmt.Fprintf(localConn, "* BAD No IMAP server configured\r\n")
		return
	}

	// Connect to upstream IMAP server
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.IMAP.Host, serverConfig.IMAP.Port)
	var upstreamConn net.Conn
	var err error

	if serverConfig.IMAP.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.IMAP.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		log.Printf("Failed to connect to upstream IMAP server %s: %v", upstreamAddr, err)
		fmt.Fprintf(localConn, "* BAD Cannot connect to mail server\r\n")
		return
	}
	defer upstreamConn.Close()

	log.Printf("Connected to upstream IMAP server %s", upstreamAddr)

	// Start proxying data between connections
	done := make(chan bool, 2)
	authenticated := false

	// Proxy from upstream to local client
	go func() {
		scanner := bufio.NewScanner(upstreamConn)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintf(localConn, "%s\r\n", line)
		}
		done <- true
	}()

	// Proxy from local client to upstream
	go func() {
		scanner := bufio.NewScanner(localConn)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)

			if len(parts) >= 3 && strings.ToUpper(parts[1]) == "LOGIN" && !authenticated {
				// Replace login credentials
				tag := parts[0]
				fmt.Fprintf(upstreamConn, "%s LOGIN %s %s\r\n", tag, serverConfig.IMAP.Username, serverConfig.IMAP.Password)
				authenticated = true
			} else {
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		done <- true
	}()

	<-done
	log.Printf("IMAP client %s disconnected", localConn.RemoteAddr())
}
