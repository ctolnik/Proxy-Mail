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

	clientAddr := localConn.RemoteAddr().String()
	log.Printf("[SMTP] Client connected from %s", clientAddr)

	// Get the first available SMTP server config
	serverConfig := s.config.GetServerByProtocol("smtp")
	if serverConfig == nil || serverConfig.SMTP == nil {
		log.Printf("[SMTP] ERROR: No SMTP server configuration found for client %s", clientAddr)
		fmt.Fprintf(localConn, "421 No SMTP server configured\r\n")
		return
	}

	log.Printf("[SMTP] Using server config '%s' for client %s", serverConfig.Name, clientAddr)

	// Connect to upstream SMTP server
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.SMTP.Host, serverConfig.SMTP.Port)
	var upstreamConn net.Conn
	var err error

	log.Printf("[SMTP] Connecting to upstream server %s (TLS: %v) for mailbox %s", upstreamAddr, serverConfig.SMTP.UseTLS, serverConfig.SMTP.Username)

	if serverConfig.SMTP.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.SMTP.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		log.Printf("[SMTP] ERROR: Failed to connect to upstream server %s for mailbox %s: %v", upstreamAddr, serverConfig.SMTP.Username, err)
		fmt.Fprintf(localConn, "421 Cannot connect to mail server\r\n")
		return
	}
	defer upstreamConn.Close()

	log.Printf("[SMTP] Successfully connected to upstream server %s for mailbox %s", upstreamAddr, serverConfig.SMTP.Username)

	// Start proxying data between connections
	done := make(chan bool, 2)
	authenticationSent := false

	// Proxy from upstream to local client
	go func() {
		log.Printf("[SMTP] Started downstream proxy (server -> client) for %s", clientAddr)
		scanner := bufio.NewScanner(upstreamConn)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[SMTP] SERVER -> CLIENT (%s): %s", clientAddr, line)
			fmt.Fprintf(localConn, "%s\r\n", line)
		}
		log.Printf("[SMTP] Downstream proxy closed for %s", clientAddr)
		done <- true
	}()

	// Proxy from local client to upstream
	go func() {
		log.Printf("[SMTP] Started upstream proxy (client -> server) for %s", clientAddr)
		scanner := bufio.NewScanner(localConn)
		for scanner.Scan() {
			line := scanner.Text()
			command := strings.ToUpper(strings.TrimSpace(line))
			
			// Handle authentication commands
			if strings.HasPrefix(command, "AUTH LOGIN") && !authenticationSent {
				// Start AUTH LOGIN sequence
				log.Printf("[SMTP] CLIENT -> SERVER (%s): %s", clientAddr, line)
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
				authenticationSent = true
			} else if authenticationSent && !strings.HasPrefix(command, "AUTH") {
				// During AUTH LOGIN, replace username and password
				if line == base64.StdEncoding.EncodeToString([]byte("username")) {
					// Replace with actual username
					username := base64.StdEncoding.EncodeToString([]byte(serverConfig.SMTP.Username))
					log.Printf("[SMTP] CLIENT -> SERVER (%s): [base64_username] -> [base64_%s]", clientAddr, serverConfig.SMTP.Username)
					fmt.Fprintf(upstreamConn, "%s\r\n", username)
				} else if line == base64.StdEncoding.EncodeToString([]byte("password")) {
					// Replace with actual password
					password := base64.StdEncoding.EncodeToString([]byte(serverConfig.SMTP.Password))
					log.Printf("[SMTP] CLIENT -> SERVER (%s): [base64_password] -> [base64_hidden]", clientAddr)
					fmt.Fprintf(upstreamConn, "%s\r\n", password)
				} else {
					// Pass through any other auth data
					log.Printf("[SMTP] CLIENT -> SERVER (%s): [auth_data] %s", clientAddr, line)
					fmt.Fprintf(upstreamConn, "%s\r\n", line)
				}
			} else {
				// Pass through all other commands
				log.Printf("[SMTP] CLIENT -> SERVER (%s): %s", clientAddr, line)
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		log.Printf("[SMTP] Upstream proxy closed for %s", clientAddr)
		done <- true
	}()

	<-done
	log.Printf("[SMTP] Client %s disconnected from mailbox %s", clientAddr, serverConfig.SMTP.Username)
}

