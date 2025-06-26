package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
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
	log.Printf("[SMTP] Proxy server listening on %s", addr)

	for !s.stopping {
		conn, err := listener.Accept()
		if err != nil {
			if s.stopping {
				break
			}
			log.Printf("[SMTP] Accept error: %v", err)
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
	LogInfo("SMTP client connected from %s", clientAddr)

	// Send initial greeting to client
	fmt.Fprintf(localConn, "220 Proxy-Mail SMTP Ready\r\n")
	LogDebug("SMTP sent greeting to client %s", clientAddr)

	// Handle commands until we get MAIL FROM to determine which mailbox to use
	s.handleSMTPSessionDynamic(localConn, clientAddr)
}

// handleSMTPSessionDynamic handles SMTP session with dynamic mailbox selection
func (s *SMTPServer) handleSMTPSessionDynamic(localConn net.Conn, clientAddr string) {
	clientReader := bufio.NewReader(localConn)
	var selectedMailbox string
	var upstreamConn net.Conn
	var serverConfig *ServerConfig
	var isConnectedToUpstream bool

	for {
		// Read line from client
		lineBytes, err := clientReader.ReadBytes('\n')
		if err != nil {
			LogDebug("SMTP client %s disconnected: %v", clientAddr, err)
			break
		}

		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		command := ""
		if len(fields) > 0 {
			command = strings.ToUpper(fields[0])
		}

		LogDebug("SMTP CLIENT -> PROXY (%s): %s", clientAddr, line)

		// Handle MAIL FROM command to determine which mailbox to use
		if command == "MAIL" && strings.Contains(strings.ToUpper(line), "FROM:") {
			// Extract sender email from MAIL FROM command
			senderEmail := s.extractEmailFromMailFrom(line)
			LogInfo("SMTP selecting mailbox for sender: %s", senderEmail)

			// Find matching server config for this sender
			serverConfig = s.findServerConfigBySender(senderEmail)
			if serverConfig == nil {
				LogError("No SMTP server configuration found for sender: %s", senderEmail)
				fmt.Fprintf(localConn, "550 No mailbox configured for sender\r\n")
				continue
			}

			selectedMailbox = serverConfig.SMTP.Username
			LogInfo("SMTP using mailbox: %s for sender: %s", selectedMailbox, senderEmail)

			// Connect to upstream SMTP server for this mailbox
			upstreamConn, err = s.connectToUpstream(serverConfig, clientAddr)
			if err != nil {
				LogError("Failed to connect to upstream SMTP for %s: %v", selectedMailbox, err)
				fmt.Fprintf(localConn, "421 Cannot connect to mail server\r\n")
				continue
			}
			isConnectedToUpstream = true

			// Start normal SMTP session with the selected upstream
			defer upstreamConn.Close()
			s.handleSMTPSession(localConn, upstreamConn, serverConfig.SMTP, clientAddr)
			return
		}

		// Handle other commands before mailbox selection
		switch command {
		case "EHLO", "HELO":
			fmt.Fprintf(localConn, "250-Proxy-Mail SMTP Ready\r\n")
			fmt.Fprintf(localConn, "250-AUTH LOGIN\r\n")
			fmt.Fprintf(localConn, "250 STARTTLS\r\n")
			LogDebug("SMTP sent EHLO response to client %s", clientAddr)

		case "QUIT":
			fmt.Fprintf(localConn, "221 Goodbye\r\n")
			LogDebug("SMTP client %s quit", clientAddr)
			return

		default:
			fmt.Fprintf(localConn, "503 Need MAIL FROM first\r\n")
			LogDebug("SMTP client %s sent command before MAIL FROM: %s", clientAddr, command)
		}
	}

	if isConnectedToUpstream {
		upstreamConn.Close()
	}
}

func (s *SMTPServer) handleSMTPSessionWithOptions(localConn, upstreamConn net.Conn, upstreamConfig *MailServerConfig, clientAddr string, skipGreeting bool) {
	log.Printf("[SMTP] Starting SMTP session for client %s (skipGreeting: %v)", clientAddr, skipGreeting)

	// Read initial greeting from upstream server (unless already consumed by STARTTLS)
	upstreamScanner := bufio.NewScanner(upstreamConn)
	if !skipGreeting {
		if upstreamScanner.Scan() {
			greeting := upstreamScanner.Text()
			log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, greeting)
			// Forward greeting to client
			fmt.Fprintf(localConn, "%s\r\n", greeting)
			log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, greeting)
		}
	} else {
		// For STARTTLS connections, send a fresh greeting to the client
		greeting := "220 SMTP Proxy Ready"
		fmt.Fprintf(localConn, "%s\r\n", greeting)
		log.Printf("[SMTP] PROXY -> CLIENT (%s): %s (fresh greeting after STARTTLS)", clientAddr, greeting)
	}

	// Continue with the same logic as handleSMTPSession
	s.handleSMTPCommands(localConn, upstreamConn, upstreamScanner, upstreamConfig, clientAddr)
}

func (s *SMTPServer) handleSMTPSession(localConn, upstreamConn net.Conn, upstreamConfig *MailServerConfig, clientAddr string) {
	s.handleSMTPSessionWithOptions(localConn, upstreamConn, upstreamConfig, clientAddr, false)
}

func (s *SMTPServer) handleSMTPCommands(localConn, upstreamConn net.Conn, upstreamScanner *bufio.Scanner, upstreamConfig *MailServerConfig, clientAddr string) {
	log.Printf("[SMTP] Handling SMTP commands for client %s", clientAddr)

	// Handle SMTP commands from client
	clientReader := bufio.NewReader(localConn)
	inDataMode := false
	authState := "" // "expecting_username", "expecting_password", ""

	for {
		// Read line as raw bytes to preserve encoding
		lineBytes, err := clientReader.ReadBytes('\n')
		if err != nil {
			log.Printf("[SMTP] Client %s disconnected: %v", clientAddr, err)
			break
		}
		
		// Convert to string for command parsing only (preserving original bytes)
		line := strings.TrimSpace(string(lineBytes))
		if line == "" && !inDataMode {
			continue
		}

		// Get command safely
		fields := strings.Fields(line)
		var command string
		if len(fields) > 0 {
			command = strings.ToUpper(fields[0])
		} else {
			command = ""
		}
		log.Printf("[SMTP] CLIENT -> PROXY (%s): %s", clientAddr, line)

		// Handle DATA mode (message content)
		if inDataMode {
			if line == "." {
				// End of message
				inDataMode = false
				log.Printf("[SMTP] CLIENT -> UPSTREAM (%s): . (end of message)", clientAddr)
				upstreamConn.Write([]byte(".\r\n"))
			} else {
				// Forward raw bytes to preserve encoding
				// Only do dot escaping if needed (line starts with dot)
				if len(lineBytes) > 0 && lineBytes[0] == '.' {
					// Escape leading dot by adding another dot
					upstreamConn.Write([]byte("."))
				}
				// Remove the \n from lineBytes and ensure CRLF ending
				if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\n' {
					lineBytes = lineBytes[:len(lineBytes)-1] // Remove \n
				}
				// Remove \r if present
				if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
					lineBytes = lineBytes[:len(lineBytes)-1] // Remove \r
				}
				// Forward original bytes (preserving encoding) + CRLF
				upstreamConn.Write(lineBytes)
				upstreamConn.Write([]byte("\r\n"))
			}
			continue
		}

		switch command {
		case "EHLO", "HELO":
			// Forward EHLO/HELO to upstream
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			// Read multi-line response
			for upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)

				// Check if this is the last line of multi-line response
				if len(response) >= 4 && response[3] == ' ' {
					break
				}
			}

		case "MAIL":
			// For MAIL FROM, preserve the original sender address
			// but authenticate using the proxy credentials
			log.Printf("[SMTP] Preserving original sender: %s", line)
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s (original sender preserved)", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)
			}

		case "RCPT":
			// Forward RCPT TO commands as-is
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)
			}

		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) >= 2 && strings.ToUpper(parts[1]) == "LOGIN" {
				// Handle AUTH LOGIN - authenticate with upstream first, then fake client auth
				log.Printf("[SMTP] Handling AUTH LOGIN with stored credentials for %s", upstreamConfig.Username)

				// Authenticate with upstream server using our credentials
				if s.authenticateWithUpstream(upstreamConn, upstreamScanner, upstreamConfig, clientAddr) {
					log.Printf("[SMTP] Upstream authentication successful for %s", upstreamConfig.Username)

					// Now fake the client authentication flow
					authState = "expecting_username"
					fmt.Fprintf(localConn, "334 VXNlcm5hbWU6\r\n") // "Username:" in base64
					log.Printf("[SMTP] PROXY -> CLIENT (%s): 334 VXNlcm5hbWU6 (requesting client username)", clientAddr)
				} else {
					log.Printf("[SMTP] Upstream authentication failed for %s", upstreamConfig.Username)
					fmt.Fprintf(localConn, "535 5.7.8 Authentication failed\r\n")
					log.Printf("[SMTP] PROXY -> CLIENT (%s): 535 5.7.8 Authentication failed", clientAddr)
				}
			} else {
				// Other AUTH methods - pass through
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
				log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

				if upstreamScanner.Scan() {
					response := upstreamScanner.Text()
					log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
					fmt.Fprintf(localConn, "%s\r\n", response)
					log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)
				}
			}

		case "DATA":
			// Forward DATA command
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)

				if strings.HasPrefix(response, "354") {
					inDataMode = true
					log.Printf("[SMTP] Entering DATA mode for client %s", clientAddr)
				}
			}

		case "QUIT":
			// Forward QUIT and close
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)
			}
			return

		default:
			// Handle AUTH LOGIN state machine
			if authState == "expecting_username" {
				// Client sent username (we ignore it)
				log.Printf("[SMTP] Ignoring client username: %s", line)
				authState = "expecting_password"
				fmt.Fprintf(localConn, "334 UGFzc3dvcmQ6\r\n") // "Password:" in base64
				log.Printf("[SMTP] PROXY -> CLIENT (%s): 334 UGFzc3dvcmQ6 (requesting client password)", clientAddr)
				continue
			} else if authState == "expecting_password" {
				// Client sent password (we ignore it and complete auth)
				log.Printf("[SMTP] Ignoring client password, authentication already completed upstream")
				authState = ""
				fmt.Fprintf(localConn, "235 2.7.0 Authentication successful\r\n")
				log.Printf("[SMTP] PROXY -> CLIENT (%s): 235 2.7.0 Authentication successful", clientAddr)
				continue
			}

			// Forward all other commands to upstream
			fmt.Fprintf(upstreamConn, "%s\r\n", line)
			log.Printf("[SMTP] PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				log.Printf("[SMTP] PROXY -> CLIENT (%s): %s", clientAddr, response)
			}
		}
	}

	log.Printf("[SMTP] Client %s disconnected from SMTP mailbox %s", clientAddr, upstreamConfig.Username)
}

// authenticateWithUpstream handles authentication with the upstream SMTP server
func (s *SMTPServer) authenticateWithUpstream(upstreamConn net.Conn, upstreamScanner *bufio.Scanner, upstreamConfig *MailServerConfig, clientAddr string) bool {
	// Start AUTH LOGIN with upstream
	fmt.Fprintf(upstreamConn, "AUTH LOGIN\r\n")
	log.Printf("[SMTP] PROXY -> UPSTREAM (%s): AUTH LOGIN", clientAddr)

	// Read username prompt
	if !upstreamScanner.Scan() {
		return false
	}
	response := upstreamScanner.Text()
	log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)

	// Send our username
	username := base64.StdEncoding.EncodeToString([]byte(upstreamConfig.Username))
	fmt.Fprintf(upstreamConn, "%s\r\n", username)
	log.Printf("[SMTP] PROXY -> UPSTREAM (%s): [base64_username] %s", clientAddr, upstreamConfig.Username)

	// Read password prompt
	if !upstreamScanner.Scan() {
		return false
	}
	response = upstreamScanner.Text()
	log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)

	// Send our password
	password := base64.StdEncoding.EncodeToString([]byte(upstreamConfig.Password))
	fmt.Fprintf(upstreamConn, "%s\r\n", password)
	log.Printf("[SMTP] PROXY -> UPSTREAM (%s): [base64_password] [hidden]", clientAddr)

	// Read authentication result
	if !upstreamScanner.Scan() {
		return false
	}
	response = upstreamScanner.Text()
	log.Printf("[SMTP] UPSTREAM -> PROXY (%s): %s", clientAddr, response)

	return strings.HasPrefix(response, "235")
}

// upgradeToSTARTTLS handles the STARTTLS upgrade for port 587
func (s *SMTPServer) upgradeToSTARTTLS(conn net.Conn, config *MailServerConfig, clientAddr string) (net.Conn, error) {
	scanner := bufio.NewScanner(conn)
	
	// Read initial greeting
	if !scanner.Scan() {
		return nil, fmt.Errorf("failed to read SMTP greeting")
	}
	greeting := scanner.Text()
	log.Printf("[SMTP] STARTTLS greeting (%s): %s", clientAddr, greeting)
	
	// Send EHLO
	fmt.Fprintf(conn, "EHLO localhost\r\n")
	log.Printf("[SMTP] STARTTLS EHLO (%s): EHLO localhost", clientAddr)
	
	// Read EHLO response (multi-line)
	for scanner.Scan() {
		response := scanner.Text()
		log.Printf("[SMTP] STARTTLS EHLO response (%s): %s", clientAddr, response)
		// Check if this is the last line of multi-line response
		if len(response) >= 4 && response[3] == ' ' {
			break
		}
	}
	
	// Send STARTTLS command
	fmt.Fprintf(conn, "STARTTLS\r\n")
	log.Printf("[SMTP] STARTTLS command (%s): STARTTLS", clientAddr)
	
	// Read STARTTLS response
	if !scanner.Scan() {
		return nil, fmt.Errorf("failed to read STARTTLS response")
	}
	response := scanner.Text()
	log.Printf("[SMTP] STARTTLS response (%s): %s", clientAddr, response)
	
	if !strings.HasPrefix(response, "220") {
		return nil, fmt.Errorf("STARTTLS failed: %s", response)
	}
	
	// Upgrade to TLS
	tlsConn := tls.Client(conn, &tls.Config{ServerName: config.Host})
	err := tlsConn.Handshake()
	if err != nil {
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	// Send fresh EHLO after TLS upgrade (required by many servers)
	fmt.Fprintf(tlsConn, "EHLO localhost\r\n")
	log.Printf("[SMTP] Post-STARTTLS EHLO (%s): EHLO localhost", clientAddr)

	// Read EHLO response after TLS upgrade
	tlsScanner := bufio.NewScanner(tlsConn)
	for tlsScanner.Scan() {
		response := tlsScanner.Text()
		log.Printf("[SMTP] Post-STARTTLS EHLO response (%s): %s", clientAddr, response)
		// Check if this is the last line of multi-line response
		if len(response) >= 4 && response[3] == ' ' {
			break
		}
	}

	log.Printf("[SMTP] STARTTLS upgrade successful (%s)", clientAddr)
	
	// Automatically authenticate with Gmail after STARTTLS
	if err := s.autoAuthenticateAfterSTARTTLS(tlsConn, config, clientAddr); err != nil {
		return nil, fmt.Errorf("auto-authentication failed: %w", err)
	}
	
	return tlsConn, nil
}

// extractEmailFromMailFrom extracts email address from MAIL FROM command
func (s *SMTPServer) extractEmailFromMailFrom(line string) string {
	// Extract email from "MAIL FROM:<email@domain.com>"
	start := strings.Index(strings.ToUpper(line), "FROM:")
	if start == -1 {
		return ""
	}
	
	remainder := line[start+5:] // Skip "FROM:"
	remainder = strings.TrimSpace(remainder)
	
	// Remove angle brackets if present
	if strings.HasPrefix(remainder, "<") && strings.HasSuffix(remainder, ">") {
		return remainder[1 : len(remainder)-1]
	}
	
	// Take first word (email address)
	fields := strings.Fields(remainder)
	if len(fields) > 0 {
		return fields[0]
	}
	
	return remainder
}

// findServerConfigBySender finds server config that matches the sender email
func (s *SMTPServer) findServerConfigBySender(senderEmail string) *ServerConfig {
	// First, try exact match
	for _, server := range s.config.Servers {
		if server.SMTP != nil && server.SMTP.Username == senderEmail {
			return &server
		}
	}
	
	// If no exact match, return the first available SMTP server
	// This allows sending from any address using any configured mailbox
	for _, server := range s.config.Servers {
		if server.SMTP != nil {
			LogInfo("SMTP fallback: Using mailbox %s for sender %s", server.SMTP.Username, senderEmail)
			return &server
		}
	}
	
	return nil
}

// connectToUpstream establishes connection to upstream SMTP server
func (s *SMTPServer) connectToUpstream(serverConfig *ServerConfig, clientAddr string) (net.Conn, error) {
	upstreamAddr := fmt.Sprintf("%s:%d", serverConfig.SMTP.Host, serverConfig.SMTP.Port)
	var upstreamConn net.Conn
	var err error

	LogInfo("SMTP connecting to upstream server %s for mailbox %s", upstreamAddr, serverConfig.SMTP.Username)

	// For port 587, always start with plain connection (STARTTLS)
	// For port 465, use direct TLS connection
	if serverConfig.SMTP.Port == 465 && serverConfig.SMTP.UseTLS {
		upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: serverConfig.SMTP.Host})
	} else {
		upstreamConn, err = net.Dial("tcp", upstreamAddr)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", upstreamAddr, err)
	}

	// Handle STARTTLS upgrade if needed (port 587)
	if serverConfig.SMTP.Port == 587 && serverConfig.SMTP.UseTLS {
		upstreamConn, err = s.upgradeToSTARTTLS(upstreamConn, serverConfig.SMTP, clientAddr)
		if err != nil {
			upstreamConn.Close()
			return nil, fmt.Errorf("STARTTLS upgrade failed: %w", err)
		}
	}

	LogInfo("SMTP successfully connected to upstream server %s for mailbox %s", upstreamAddr, serverConfig.SMTP.Username)
	return upstreamConn, nil
}

// autoAuthenticateAfterSTARTTLS automatically authenticates with Gmail after STARTTLS
func (s *SMTPServer) autoAuthenticateAfterSTARTTLS(conn net.Conn, config *MailServerConfig, clientAddr string) error {
	scanner := bufio.NewScanner(conn)
	
	LogInfo("SMTP auto-authenticating with %s after STARTTLS", config.Username)
	
	// Start AUTH LOGIN
	fmt.Fprintf(conn, "AUTH LOGIN\r\n")
	LogDebug("SMTP AUTO AUTH: AUTH LOGIN")
	
	// Read username prompt
	if !scanner.Scan() {
		return fmt.Errorf("failed to read username prompt")
	}
	response := scanner.Text()
	LogDebug("SMTP AUTO AUTH response: %s", response)
	
	if !strings.HasPrefix(response, "334") {
		return fmt.Errorf("unexpected response to AUTH LOGIN: %s", response)
	}
	
	// Send username
	username := base64.StdEncoding.EncodeToString([]byte(config.Username))
	fmt.Fprintf(conn, "%s\r\n", username)
	LogDebug("SMTP AUTO AUTH: sent username %s", config.Username)
	
	// Read password prompt
	if !scanner.Scan() {
		return fmt.Errorf("failed to read password prompt")
	}
	response = scanner.Text()
	LogDebug("SMTP AUTO AUTH response: %s", response)
	
	if !strings.HasPrefix(response, "334") {
		return fmt.Errorf("unexpected response to password prompt: %s", response)
	}
	
	// Send password
	password := base64.StdEncoding.EncodeToString([]byte(config.Password))
	fmt.Fprintf(conn, "%s\r\n", password)
	LogDebug("SMTP AUTO AUTH: sent password [hidden]")
	
	// Read authentication result
	if !scanner.Scan() {
		return fmt.Errorf("failed to read auth result")
	}
	response = scanner.Text()
	LogDebug("SMTP AUTO AUTH result: %s", response)
	
	if !strings.HasPrefix(response, "235") {
		return fmt.Errorf("authentication failed: %s", response)
	}
	
	LogInfo("SMTP auto-authentication successful for %s", config.Username)
	return nil
}
