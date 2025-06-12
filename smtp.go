package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"encoding/base64"
)

type SMTPServer struct {
	config   *Config
	listener net.Listener
	wg       sync.WaitGroup
	stopping bool
}

func NewSMTPServer(config *Config) *SMTPServer {
	return &SMTPServer{
		config: config,
	}
}

func (s *SMTPServer) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Local.SMTP.Host, s.config.Local.SMTP.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start SMTP server on %s: %w", addr, err)
	}

	s.listener = listener
	log.Printf("SMTP proxy server listening on %s", addr)

	for !s.stopping {
		conn, err := listener.Accept()
		if err != nil {
			if s.stopping {
				break
			}
			log.Printf("SMTP accept error: %v", err)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}

	s.wg.Wait()
	return nil
}

func (s *SMTPServer) Stop() error {
	s.stopping = true
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *SMTPServer) handleConnection(localConn net.Conn) {
	defer s.wg.Done()
	defer localConn.Close()

	log.Printf("SMTP client connected from %s", localConn.RemoteAddr())

	// Get the first available SMTP server config
	serverConfig := s.config.GetServerByProtocol("smtp")
	if serverConfig == nil || serverConfig.SMTP == nil {
		log.Printf("No SMTP server configuration found")
		fmt.Fprintf(localConn, "421 No SMTP server configured\r\n")
		return
	}

	// Connect to upstream SMTP server
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.SMTP.Host, serverConfig.SMTP.Port)
	var upstreamConn net.Conn
	var err error

	if serverConfig.SMTP.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.SMTP.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		log.Printf("Failed to connect to upstream SMTP server %s: %v", upstreamAddr, err)
		fmt.Fprintf(localConn, "421 Cannot connect to mail server\r\n")
		return
	}
	defer upstreamConn.Close()

	log.Printf("Connected to upstream SMTP server %s", upstreamAddr)

	// Start proxying data between connections
	done := make(chan bool, 2)
	authenticationSent := false

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
			command := strings.ToUpper(strings.TrimSpace(line))
			
			// Handle authentication commands
			if strings.HasPrefix(command, "AUTH LOGIN") && !authenticationSent {
				// Start AUTH LOGIN sequence
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
				authenticationSent = true
			} else if authenticationSent && !strings.HasPrefix(command, "AUTH") {
				// During AUTH LOGIN, replace username and password
				if line == base64.StdEncoding.EncodeToString([]byte("username")) {
					// Replace with actual username
					username := base64.StdEncoding.EncodeToString([]byte(serverConfig.SMTP.Username))
					fmt.Fprintf(upstreamConn, "%s\r\n", username)
				} else if line == base64.StdEncoding.EncodeToString([]byte("password")) {
					// Replace with actual password
					password := base64.StdEncoding.EncodeToString([]byte(serverConfig.SMTP.Password))
					fmt.Fprintf(upstreamConn, "%s\r\n", password)
				} else {
					// Pass through any other auth data
					fmt.Fprintf(upstreamConn, "%s\r\n", line)
				}
			} else {
				// Pass through all other commands
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		done <- true
	}()

	<-done
	log.Printf("SMTP client %s disconnected", localConn.RemoteAddr())
}

