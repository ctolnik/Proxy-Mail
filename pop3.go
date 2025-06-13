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

type POP3Server struct {
	config   *Config
	listener net.Listener
	wg       sync.WaitGroup
	stopping bool
}

func NewPOP3Server(config *Config) *POP3Server {
	return &POP3Server{
		config: config,
	}
}

func (s *POP3Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Local.POP3.Host, s.config.Local.POP3.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start POP3 server on %s: %w", addr, err)
	}

	s.listener = listener
	log.Printf("POP3 proxy server listening on %s", addr)

	for !s.stopping {
		conn, err := listener.Accept()
		if err != nil {
			if s.stopping {
				break
			}
			log.Printf("POP3 accept error: %v", err)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}

	s.wg.Wait()
	return nil
}

func (s *POP3Server) Stop() error {
	s.stopping = true
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *POP3Server) handleConnection(localConn net.Conn) {
	defer s.wg.Done()
	defer localConn.Close()

	clientAddr := localConn.RemoteAddr().String()
	log.Printf("[POP3] Client connected from %s", clientAddr)

	// Get the first available POP3 server config
	serverConfig := s.config.GetServerByProtocol("pop3")
	if serverConfig == nil || serverConfig.POP3 == nil {
		log.Printf("[POP3] ERROR: No POP3 server configuration found for client %s", clientAddr)
		fmt.Fprintf(localConn, "-ERR No POP3 server configured\r\n")
		return
	}

	log.Printf("[POP3] Using server config '%s' for client %s", serverConfig.Name, clientAddr)

	// Connect to upstream POP3 server
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.POP3.Host, serverConfig.POP3.Port)
	var upstreamConn net.Conn
	var err error

	log.Printf("[POP3] Connecting to upstream server %s (TLS: %v) for mailbox %s", upstreamAddr, serverConfig.POP3.UseTLS, serverConfig.POP3.Username)

	if serverConfig.POP3.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.POP3.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		log.Printf("[POP3] ERROR: Failed to connect to upstream server %s for mailbox %s: %v", upstreamAddr, serverConfig.POP3.Username, err)
		fmt.Fprintf(localConn, "-ERR Cannot connect to mail server\r\n")
		return
	}
	defer upstreamConn.Close()

	log.Printf("[POP3] Successfully connected to upstream server %s for mailbox %s", upstreamAddr, serverConfig.POP3.Username)

	// Start proxying data between connections
	done := make(chan bool, 2)

	// Proxy from upstream to local client
	go func() {
		log.Printf("[POP3] Started downstream proxy (server -> client) for %s", clientAddr)
		scanner := bufio.NewScanner(upstreamConn)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[POP3] SERVER -> CLIENT (%s): %s", clientAddr, line)
			fmt.Fprintf(localConn, "%s\r\n", line)
		}
		log.Printf("[POP3] Downstream proxy closed for %s", clientAddr)
		done <- true
	}()

	// Proxy from local client to upstream
	go func() {
		log.Printf("[POP3] Started upstream proxy (client -> server) for %s", clientAddr)
		scanner := bufio.NewScanner(localConn)
		for scanner.Scan() {
			line := scanner.Text()
			command := strings.ToUpper(strings.TrimSpace(line))
			
			// Handle authentication transparently
			if strings.HasPrefix(command, "USER ") {
				log.Printf("[POP3] CLIENT -> SERVER (%s): USER [client_provided] -> USER %s", clientAddr, serverConfig.POP3.Username)
				fmt.Fprintf(upstreamConn, "USER %s\r\n", serverConfig.POP3.Username)
			} else if strings.HasPrefix(command, "PASS ") {
				log.Printf("[POP3] CLIENT -> SERVER (%s): PASS [client_provided] -> PASS [hidden]", clientAddr)
				fmt.Fprintf(upstreamConn, "PASS %s\r\n", serverConfig.POP3.Password)
			} else {
				log.Printf("[POP3] CLIENT -> SERVER (%s): %s", clientAddr, line)
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		log.Printf("[POP3] Upstream proxy closed for %s", clientAddr)
		done <- true
	}()

	<-done
	log.Printf("[POP3] Client %s disconnected from mailbox %s", clientAddr, serverConfig.POP3.Username)
}
