package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// charsetRegexp matches Content-Type charset parameter
var charsetRegexp = regexp.MustCompile(`(?i)charset\s*=\s*"?([^";,\s]+)"?`)

// detectCharset extracts charset from email headers
func detectCharset(headers []byte) string {
	matches := charsetRegexp.FindSubmatch(headers)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}

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
	LogInfo("📧 SMTP: Client connected from %s", clientAddr)

	// Send initial greeting to client
	fmt.Fprintf(localConn, "220 Proxy-Mail SMTP Ready\r\n")
	LogDebug("SMTP sent greeting to client %s", clientAddr)

	// Handle commands until we get MAIL FROM to determine which mailbox to use
	s.handleSMTPSessionDynamic(localConn, clientAddr)
}

// smtpState tracks the state of an SMTP session
type smtpState struct {
	isAuthenticated bool
	authUsername    string // full email address
	authState       string // "", "username", "password"
	mailboxName     string // for logging context
	upstreamConn    net.Conn
	serverConfig    *ServerConfig
	inDataMode      bool   // track DATA command state
	heloHost        string // store HELO hostname for legacy clients
}

// getMailboxIdentifier returns a string identifier for the current mailbox for logging
func (s *smtpState) getMailboxIdentifier() string {
	if s.mailboxName != "" {
		return s.mailboxName
	}
	if s.authUsername != "" {
		return s.authUsername
	}
	return "unknown"
}

// handleSMTPDataMode handles the DATA command in binary-safe mode
// to preserve original email encoding
func (s *SMTPServer) handleSMTPDataMode(localConn net.Conn, upstreamConn net.Conn, clientAddr string, mailboxName string) error {
	// Read raw bytes using a larger buffer for efficiency
	reader := bufio.NewReaderSize(localConn, 32*1024)
	var messageBuffer bytes.Buffer
	var headerBuffer bytes.Buffer
	inHeaders := true
	var charset string

	// Use a timeout for reading the entire message
	if err := localConn.SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil {
		return fmt.Errorf("failed to set read deadline: %v", err)
	}
	defer localConn.SetReadDeadline(time.Time{})

	for {
		// Read line as raw bytes
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("error reading message data: %v", err)
		}

		// Still collecting headers
		if inHeaders {
			headerBuffer.Write(line)
			// Check for end of headers (empty line)
			if len(line) == 2 && bytes.Equal(line, []byte{'\r', '\n'}) {
				inHeaders = false
				// Detect charset from collected headers
				charset = detectCharset(headerBuffer.Bytes())
				if charset != "" {
					LogInfo("📧 Detected email charset: %s", charset)
				}
			}
		}

		// Store the complete message
		messageBuffer.Write(line)

		// Check for message end (.<CRLF>)
		if len(line) == 3 && line[0] == '.' && line[1] == '\r' && line[2] == '\n' {
			// Found the end marker
			if messageBuffer.Len() > 3 {
				// Forward the complete message to upstream
				if _, err := upstreamConn.Write(messageBuffer.Bytes()); err != nil {
					return fmt.Errorf("error forwarding message to upstream: %v", err)
				}
				LogInfo("📧 Forwarded message (%d bytes) with original encoding%s", 
					messageBuffer.Len(),
					func() string {
						if charset != "" {
							return fmt.Sprintf(" (charset: %s)", charset)
						}
						return ""
					}())
				return nil
			}
		}
	}
}

// handleSMTPSessionDynamic handles SMTP session with dynamic mailbox selection
func (s *SMTPServer) handleSMTPSessionDynamic(localConn net.Conn, clientAddr string) {
	clientReader := bufio.NewReader(localConn)
	state := &smtpState{}

	// Initial greeting already sent in handleConnection, don't send it again here
	LogDebug("[%s] Starting SMTP session for client %s", state.getMailboxIdentifier(), clientAddr)

	// Ensure we clean up connections on exit
	defer func() {
		if state.upstreamConn != nil {
			LogDebug("[%s] Closing upstream connection for client %s", state.getMailboxIdentifier(), clientAddr)
			state.upstreamConn.Close()
		}
	}()

	for {
		// Read line from client
		lineBytes, err := clientReader.ReadBytes('\n')
		if err != nil {
			LogDebug("[%s] SMTP client %s disconnected: %v", state.getMailboxIdentifier(), clientAddr, err)
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

		LogDebug("[%s] SMTP CLIENT -> PROXY (%s): %s", state.getMailboxIdentifier(), clientAddr, line)

		// Handle DATA mode separately - but we'll use the new binary-safe method
		if state.inDataMode {
			// Instead of handling the data processing here, we now use handleSMTPDataMode
			// This is just to catch any edge cases where the old state machine still needs this flag
			// The actual data processing is now done in the case "DATA" section below
			state.inDataMode = false
			continue
		}

		// Handle commands with state management
		switch command {
		case "EHLO", "HELO":
			// Send capabilities, making AUTH more prominent
			fmt.Fprintf(localConn, "250-Proxy-Mail SMTP Ready\r\n")
			fmt.Fprintf(localConn, "250-SIZE 35882577\r\n")  // Add common SMTP extensions
			fmt.Fprintf(localConn, "250-8BITMIME\r\n")
			fmt.Fprintf(localConn, "250-PIPELINING\r\n")
			fmt.Fprintf(localConn, "250-AUTH LOGIN PLAIN\r\n")  // Make AUTH more visible
			fmt.Fprintf(localConn, "250 STARTTLS\r\n")
			LogDebug("[%s] SMTP sent enhanced capabilities to client %s", state.getMailboxIdentifier(), clientAddr)

			// For HELO, we might need to handle legacy clients differently
			if command == "HELO" {
				// Store the HELO hostname if needed
				if len(fields) > 1 {
					state.heloHost = fields[1]
				}
				LogDebug("[%s] Client using legacy HELO command, hostname: %s", state.getMailboxIdentifier(), state.heloHost)
			}

		case "AUTH":
			if len(fields) < 2 {
				fmt.Fprintf(localConn, "501 Syntax error\r\n")
				continue
			}

			authType := strings.ToUpper(fields[1])
			if authType == "LOGIN" {
				fmt.Fprintf(localConn, "334 VXNlcm5hbWU6\r\n") // Base64 for "Username:"
				state.authState = "username"
				LogDebug("[%s] SMTP client %s starting AUTH LOGIN", state.getMailboxIdentifier(), clientAddr)
				continue
			}

			fmt.Fprintf(localConn, "504 Authentication mechanism not supported\r\n")
			continue
			
		case "MAIL":
			// Extract sender email from MAIL FROM command
			senderEmail := s.extractEmailFromMailFrom(line)
			if senderEmail == "" {
				fmt.Fprintf(localConn, "501 Invalid MAIL FROM format\r\n")
				LogError("[%s] Invalid MAIL FROM format: %s", state.getMailboxIdentifier(), line)
				continue
			}

			// Check if using legacy authentication (no explicit AUTH)
			if !state.isAuthenticated {
				// Find matching server config - try to match by sender, or use any available SMTP server
				serverConfig := s.findServerConfigBySender(senderEmail)
				if serverConfig == nil {
					fmt.Fprintf(localConn, "550 No upstream SMTP server configured for sending mail\r\n")
					LogError("[%s] No SMTP server configuration available for sending mail from: %s", state.getMailboxIdentifier(), senderEmail)
					continue
				}

				// Auto-authenticate with found credentials
				state.serverConfig = serverConfig
				state.authUsername = senderEmail
				state.mailboxName = senderEmail
				state.isAuthenticated = true
				LogInfo("[%s] Auto-authenticated legacy client %s for sender: %s", state.mailboxName, clientAddr, senderEmail)
			} else {
				// For explicitly authenticated clients, verify sender matches authenticated user
				if senderEmail != state.authUsername {
					LogError("[%s] Sender mismatch: authenticated as %s but trying to send as %s",
						state.mailboxName, state.authUsername, senderEmail)
					fmt.Fprintf(localConn, "550 Sender address must match authenticated user\r\n")
					continue
				}
			}
			
			LogInfo("[%s] SMTP processing MAIL FROM command", state.mailboxName)
			
			// Connect to upstream if not already connected
			if state.upstreamConn == nil {
				LogInfo("[%s] Establishing new upstream connection for MAIL FROM command", state.mailboxName)
				var err error
				state.upstreamConn, err = s.connectToUpstream(state.serverConfig, clientAddr)
				if err != nil {
					LogError("[%s] Failed to connect to upstream server: %v", state.mailboxName, err)
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}

				// Read initial greeting
				upstreamReader := bufio.NewReader(state.upstreamConn)
				greeting, err := upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read upstream greeting: %v", state.mailboxName, err)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				LogDebug("[%s] UPSTREAM greeting: %s", state.mailboxName, strings.TrimSpace(greeting))

				// Send EHLO to upstream
				fmt.Fprintf(state.upstreamConn, "EHLO proxy-mail\r\n")
				LogDebug("[%s] PROXY -> UPSTREAM: EHLO proxy-mail", state.mailboxName)
				LogInfo("[%s] Initiating SMTP handshake with upstream server", state.mailboxName)
				
				// Read multi-line EHLO response
				for {
					response, err := upstreamReader.ReadString('\n')
					if err != nil {
						LogError("[%s] Failed to read EHLO response: %v", state.mailboxName, err)
						state.upstreamConn.Close()
						state.upstreamConn = nil
						fmt.Fprintf(localConn, "451 Local error in processing\r\n")
						continue
					}
					
					respText := strings.TrimSpace(response)
					LogDebug("[%s] UPSTREAM -> PROXY: %s", state.mailboxName, respText)
					
					if len(respText) > 3 && respText[3] == ' ' {
						break  // End of multi-line response
					}
				}

				// Authenticate with upstream
				fmt.Fprintf(state.upstreamConn, "AUTH LOGIN\r\n")
				LogDebug("[%s] PROXY -> UPSTREAM: AUTH LOGIN", state.mailboxName)
				
				response, err := upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read AUTH response: %v", state.mailboxName, err)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				respText := strings.TrimSpace(response)
				LogDebug("[%s] UPSTREAM -> PROXY: %s", state.mailboxName, respText)
				
				if !strings.HasPrefix(respText, "334") {
					LogError("[%s] Upstream AUTH failed: %s", state.mailboxName, respText)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}

				// Send username
				username := base64.StdEncoding.EncodeToString([]byte(state.serverConfig.SMTP.Username))
				fmt.Fprintf(state.upstreamConn, "%s\r\n", username)
				LogDebug("[%s] PROXY -> UPSTREAM: [base64_username]", state.mailboxName)
				
				response, err = upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read username response: %v", state.mailboxName, err)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				respText = strings.TrimSpace(response)
				LogDebug("[%s] UPSTREAM -> PROXY: %s", state.mailboxName, respText)
				
				if !strings.HasPrefix(respText, "334") {
					LogError("[%s] Upstream username failed: %s", state.mailboxName, respText)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}

				// Send password
				password := base64.StdEncoding.EncodeToString([]byte(state.serverConfig.SMTP.Password))
				fmt.Fprintf(state.upstreamConn, "%s\r\n", password)
				LogDebug("[%s] PROXY -> UPSTREAM: [base64_password]", state.mailboxName)
				
				response, err = upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read password response: %v", state.mailboxName, err)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				respText = strings.TrimSpace(response)
				LogDebug("[%s] UPSTREAM -> PROXY: %s", state.mailboxName, respText)
				
				if !strings.HasPrefix(respText, "235") {
					LogError("[%s] Upstream authentication failed: %s", state.mailboxName, respText)
					state.upstreamConn.Close()
					state.upstreamConn = nil
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				LogInfo("[%s] Successfully authenticated with upstream SMTP server", state.mailboxName)
				LogInfo("[%s] Ready to send email from %s", state.mailboxName, state.authUsername)
			}

			// Forward MAIL FROM command to upstream
			fmt.Fprintf(state.upstreamConn, "%s\r\n", line)
			LogDebug("[%s] PROXY -> UPSTREAM: %s", state.mailboxName, line)
			
			// Read response from upstream
			upstreamReader := bufio.NewReader(state.upstreamConn)
			response, err := upstreamReader.ReadString('\n')
			if err != nil {
				LogError("[%s] Failed to read MAIL FROM response: %v", state.mailboxName, err)
				fmt.Fprintf(localConn, "451 Local error in processing\r\n")
				continue
			}
			
			// Forward response to client
			fmt.Fprintf(localConn, "%s", response)
			respText := strings.TrimSpace(response)
			LogDebug("[%s] UPSTREAM -> CLIENT: %s", state.mailboxName, respText)

		case "RCPT":
			if !state.isAuthenticated || state.upstreamConn == nil {
				fmt.Fprintf(localConn, "530 Authentication required\r\n")
				continue
			}

			// Forward RCPT TO command to upstream
			fmt.Fprintf(state.upstreamConn, "%s\r\n", line)
			LogDebug("[%s] PROXY -> UPSTREAM: %s", state.mailboxName, line)
			
			// Read response from upstream
			upstreamReader := bufio.NewReader(state.upstreamConn)
			response, err := upstreamReader.ReadString('\n')
			if err != nil {
				LogError("[%s] Failed to read RCPT TO response: %v", state.mailboxName, err)
				fmt.Fprintf(localConn, "451 Local error in processing\r\n")
				continue
			}
			
			// Forward response to client
			fmt.Fprintf(localConn, "%s", response)
			respText := strings.TrimSpace(response)
			LogDebug("[%s] UPSTREAM -> CLIENT: %s", state.mailboxName, respText)

		case "DATA":
			if !state.isAuthenticated || state.upstreamConn == nil {
				fmt.Fprintf(localConn, "530 Authentication required\r\n")
				continue
			}

			// Forward DATA command to upstream
			fmt.Fprintf(state.upstreamConn, "%s\r\n", line)
			LogDebug("[%s] PROXY -> UPSTREAM: %s", state.mailboxName, line)
			
			// Read response from upstream
			upstreamReader := bufio.NewReader(state.upstreamConn)
			response, err := upstreamReader.ReadString('\n')
			if err != nil {
				LogError("[%s] Failed to read DATA response: %v", state.mailboxName, err)
				fmt.Fprintf(localConn, "451 Local error in processing\r\n")
				continue
			}
			
			// Forward response to client
			fmt.Fprintf(localConn, "%s", response)
			respText := strings.TrimSpace(response)
			LogDebug("[%s] UPSTREAM -> CLIENT: %s", state.mailboxName, respText)
			
			// If server is ready to receive data
			if strings.HasPrefix(respText, "354") {
				LogInfo("[%s] Entering DATA mode, ready to receive message content", state.mailboxName)
				LogInfo("[%s] Email transmission in progress...", state.mailboxName)
				
				// Use binary-safe DATA handling to preserve original encoding
				if err := s.handleSMTPDataMode(localConn, state.upstreamConn, clientAddr, state.mailboxName); err != nil {
					LogError("[%s] Error in DATA mode: %v", state.mailboxName, err)
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				// Read the response from upstream after data transmission
				response, err = upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read upstream response: %v", state.mailboxName, err)
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				respText = strings.TrimSpace(response)
				LogDebug("[%s] UPSTREAM -> PROXY: %s", state.mailboxName, respText)
				fmt.Fprintf(localConn, "%s\r\n", respText)
				
				if strings.HasPrefix(respText, "250") {
					LogInfo("✅ Email sent successfully from %s", state.mailboxName)
					LogInfo("[%s] SMTP transaction completed successfully", state.mailboxName)
				} else {
					LogError("❌ Email failed to send from %s: %s", state.mailboxName, respText)
					LogError("[%s] SMTP transaction failed", state.mailboxName)
				}
			}

		case "QUIT":
			fmt.Fprintf(localConn, "221 Goodbye\r\n")
			LogDebug("[%s] SMTP client %s quit", state.getMailboxIdentifier(), clientAddr)
			return

		default:
			// Handle authentication states
			if state.authState == "username" {
				decoded, err := base64.StdEncoding.DecodeString(line)
				if err != nil {
					fmt.Fprintf(localConn, "501 Invalid base64 encoding\r\n")
					state.authState = ""
					continue
				}

				state.authUsername = string(decoded)
				state.authState = "password"
				fmt.Fprintf(localConn, "334 UGFzc3dvcmQ6\r\n") // Base64 for "Password:"
				LogDebug("[%s] SMTP received username from client %s", state.getMailboxIdentifier(), clientAddr)
				continue
			}

			if state.authState == "password" {
				decoded, err := base64.StdEncoding.DecodeString(line)
				if err != nil {
					fmt.Fprintf(localConn, "501 Invalid base64 encoding\r\n")
					state.authState = ""
					continue
				}

				password := string(decoded)
				
				// Find server config matching the username
				serverConfig := s.findServerConfigByUsername(state.authUsername)
				if serverConfig == nil || !s.validateCredentials(state.authUsername, password, serverConfig) {
					fmt.Fprintf(localConn, "535 Authentication failed\r\n")
					LogError("[%s] Authentication failed for client %s", state.getMailboxIdentifier(), clientAddr)
					state.authState = ""
					continue
				}

				state.isAuthenticated = true
				state.serverConfig = serverConfig
				state.mailboxName = state.authUsername
				fmt.Fprintf(localConn, "235 Authentication successful\r\n")
				LogInfo("[%s] SMTP authentication successful for client %s", state.mailboxName, clientAddr)
				continue
			}

			if !state.isAuthenticated {
				fmt.Fprintf(localConn, "530 Authentication required\r\n")
				LogDebug("[%s] SMTP client %s sent command before authentication: %s", 
					state.getMailboxIdentifier(), clientAddr, command)
				continue
			}
			
			// Forward other commands to upstream if authenticated and connected
			if state.upstreamConn != nil {
				fmt.Fprintf(state.upstreamConn, "%s\r\n", line)
				LogDebug("[%s] PROXY -> UPSTREAM: %s", state.mailboxName, line)
				
				upstreamReader := bufio.NewReader(state.upstreamConn)
				response, err := upstreamReader.ReadString('\n')
				if err != nil {
					LogError("[%s] Failed to read response for command %s: %v", state.mailboxName, command, err)
					fmt.Fprintf(localConn, "451 Local error in processing\r\n")
					continue
				}
				
				fmt.Fprintf(localConn, "%s", response)
				respText := strings.TrimSpace(response)
				LogDebug("[%s] UPSTREAM -> CLIENT: %s", state.mailboxName, respText)
			} else {
				fmt.Fprintf(localConn, "451 Local error in processing\r\n")
				LogError("[%s] No upstream connection available for command: %s", state.mailboxName, command)
			}
		}
	}

	// Cleanup
	if state.upstreamConn != nil {
		state.upstreamConn.Close()
	}
}

// findServerConfigByUsername finds a server config that matches the username (email address)
func (s *SMTPServer) findServerConfigByUsername(email string) *ServerConfig {
	for _, server := range s.config.Servers {
		if server.SMTP != nil && server.SMTP.Username == email {
			LogInfo("Found server config for mailbox: %s", email)
			return &server
		}
	}
	LogError("No server configuration found for email: %s", email)
	return nil
}

// validateCredentials validates the provided username and password against the server config
func (s *SMTPServer) validateCredentials(username, password string, config *ServerConfig) bool {
	return config.SMTP.Username == username && config.SMTP.Password == password
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
			LogDebug("SMTP CLIENT -> UPSTREAM (%s): . (end of message)", clientAddr)
			upstreamConn.Write([]byte(".\r\n"))
			
			// Read the final response from server to see if email was sent
			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				LogDebug("SMTP final response: %s", response)
				
				if strings.HasPrefix(response, "250") {
					LogInfo("✅ EMAIL SENT successfully from %s", upstreamConfig.Username)
				} else {
					LogError("❌ EMAIL FAILED to send from %s: %s", upstreamConfig.Username, response)
				}
				
				// Forward response to client
				fmt.Fprintf(localConn, "%s\r\n", response)
			}
			} else {
				// Forward raw bytes to preserve encoding
				// Forward raw bytes without any modification
				upstreamConn.Write(lineBytes)
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
			LogDebug("SMTP PROXY -> UPSTREAM (%s): %s", clientAddr, line)

			if upstreamScanner.Scan() {
				response := upstreamScanner.Text()
				LogDebug("SMTP UPSTREAM -> PROXY (%s): %s", clientAddr, response)
				fmt.Fprintf(localConn, "%s\r\n", response)
				LogDebug("SMTP PROXY -> CLIENT (%s): %s", clientAddr, response)

				if strings.HasPrefix(response, "354") {
					LogInfo("📧 EMAIL: Starting to receive message content for %s", upstreamConfig.Username)
					
					// Use binary-safe DATA handling to preserve original encoding
					if err := s.handleSMTPDataMode(localConn, upstreamConn, clientAddr, upstreamConfig.Username); err != nil {
						LogError("❌ Error in DATA mode: %v", err)
						fmt.Fprintf(localConn, "451 Local error in processing\r\n")
						continue
					}
					
					// The handleSMTPDataMode function handles reading the response from upstream
					// So we don't need to do it here again
					if upstreamScanner.Scan() {
						response = upstreamScanner.Text()
						LogDebug("SMTP final response: %s", response)
						if strings.HasPrefix(response, "250") {
							LogInfo("✅ EMAIL SENT successfully from %s", upstreamConfig.Username)
						} else {
							LogError("❌ EMAIL FAILED to send from %s: %s", upstreamConfig.Username, response)
						}
						fmt.Fprintf(localConn, "%s\r\n", response)
					}
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
