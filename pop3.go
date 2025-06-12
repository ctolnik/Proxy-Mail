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

	log.Printf("POP3 client connected from %s", localConn.RemoteAddr())

	// Get the first available POP3 server config
	serverConfig := s.config.GetServerByProtocol("pop3")
	if serverConfig == nil || serverConfig.POP3 == nil {
		log.Printf("No POP3 server configuration found")
		fmt.Fprintf(localConn, "-ERR No POP3 server configured\r\n")
		return
	}

	// Connect to upstream POP3 server
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.POP3.Host, serverConfig.POP3.Port)
	var upstreamConn net.Conn
	var err error

	if serverConfig.POP3.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.POP3.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		log.Printf("Failed to connect to upstream POP3 server %s: %v", upstreamAddr, err)
		fmt.Fprintf(localConn, "-ERR Cannot connect to mail server\r\n")
		return
	}
	defer upstreamConn.Close()

	log.Printf("Connected to upstream POP3 server %s", upstreamAddr)

	// Start proxying data between connections
	done := make(chan bool, 2)

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
			// Handle authentication transparently
			if strings.HasPrefix(strings.ToUpper(line), "USER ") {
				fmt.Fprintf(upstreamConn, "USER %s\r\n", serverConfig.POP3.Username)
			} else if strings.HasPrefix(strings.ToUpper(line), "PASS ") {
				fmt.Fprintf(upstreamConn, "PASS %s\r\n", serverConfig.POP3.Password)
			} else {
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		done <- true
	}()

	<-done
	log.Printf("POP3 client %s disconnected", localConn.RemoteAddr())
}
