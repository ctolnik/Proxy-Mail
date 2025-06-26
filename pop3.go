package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strconv"
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

	// Start without pre-selecting server config
	s.handleIMAPBackend(localConn, clientAddr)
}

func (s *POP3Server) findServerConfigByUsername(username string) *ServerConfig {
	// First try exact match
	for _, server := range s.config.Servers {
		if (server.POP3 != nil && server.POP3.Username == username) ||
		   (server.IMAP != nil && server.IMAP.Username == username) {
			return &server
		}
	}
	return nil
}

func (s *POP3Server) handlePOP3Backend(localConn, upstreamConn net.Conn, upstreamConfig *MailServerConfig, clientAddr string) {

	// Start proxying data between connections for POP3 -> POP3
	done := make(chan bool, 2)

	// Proxy from upstream to local client
	go func() {
		log.Printf("[POP3] Started downstream POP3 proxy (server -> client) for %s", clientAddr)
		scanner := bufio.NewScanner(upstreamConn)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[POP3] POP3-SERVER -> CLIENT (%s): %s", clientAddr, line)
			fmt.Fprintf(localConn, "%s\r\n", line)
		}
		log.Printf("[POP3] Downstream POP3 proxy closed for %s", clientAddr)
		done <- true
	}()

	// Proxy from local client to upstream
	go func() {
		log.Printf("[POP3] Started upstream POP3 proxy (client -> server) for %s", clientAddr)
		scanner := bufio.NewScanner(localConn)
		for scanner.Scan() {
			line := scanner.Text()
			command := strings.ToUpper(strings.TrimSpace(line))

			// Handle authentication transparently
			if strings.HasPrefix(command, "USER ") {
				log.Printf("[POP3] CLIENT -> POP3-SERVER (%s): USER [client_provided] -> USER %s", clientAddr, upstreamConfig.Username)
				fmt.Fprintf(upstreamConn, "USER %s\r\n", upstreamConfig.Username)
			} else if strings.HasPrefix(command, "PASS ") {
				log.Printf("[POP3] CLIENT -> POP3-SERVER (%s): PASS [client_provided] -> PASS [hidden]", clientAddr)
				fmt.Fprintf(upstreamConn, "PASS %s\r\n", upstreamConfig.Password)
			} else {
				log.Printf("[POP3] CLIENT -> POP3-SERVER (%s): %s", clientAddr, line)
				fmt.Fprintf(upstreamConn, "%s\r\n", line)
			}
		}
		log.Printf("[POP3] Upstream POP3 proxy closed for %s", clientAddr)
		done <- true
	}()

	<-done
	log.Printf("[POP3] Client %s disconnected from POP3 mailbox %s", clientAddr, upstreamConfig.Username)
}

func (s *POP3Server) handleIMAPBackend(localConn net.Conn, clientAddr string) {
	log.Printf("[POP3] Starting POP3-to-IMAP translation for client %s", clientAddr)

	// IMAP session state
	var imapTag int = 1000
	var authenticated bool = false
	var selectedMailbox bool = false
	var messageCount int = 0
	var messages []IMAPMessage

	// Adding user-specific state
	var clientUsername string
	var serverConfig *ServerConfig

	// POP3 session state
	var pop3State string = "AUTHORIZATION" // AUTHORIZATION, TRANSACTION, UPDATE

	// Create a placeholder to store the upstream connection and scanner
	var upstreamConn net.Conn
	var upstreamConfig *MailServerConfig
	var protocol string
	var scanner *bufio.Scanner     // Add scanner at function level

	// Send POP3 greeting to client
	fmt.Fprintf(localConn, "+OK POP3 server ready (IMAP backend)\r\n")
	log.Printf("[POP3] PROXY -> CLIENT (%s): +OK POP3 server ready (IMAP backend)", clientAddr)

	// Handle POP3 commands and translate to IMAP
	clientReader := bufio.NewReader(localConn)
	for {
		// Read line as raw bytes to preserve encoding
		lineBytes, err := clientReader.ReadBytes('\n')
		if err != nil {
			log.Printf("[POP3] Client %s disconnected: %v", clientAddr, err)
			break
		}
		
		// Convert to string for command parsing only
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}

		parts := strings.Fields(strings.ToUpper(line))
		if len(parts) == 0 {
			continue
		}

		command := parts[0]
		log.Printf("[POP3] CLIENT -> PROXY (%s): %s", clientAddr, line)

		switch command {
		case "USER":
			if pop3State != "AUTHORIZATION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}
			if len(parts) != 2 {
				fmt.Fprintf(localConn, "-ERR Missing username\r\n")
				continue
			}

			// Store username (preserving case) and find matching server config
			clientUsername = parts[1]
			// Find exact case-insensitive match
			for _, server := range s.config.Servers {
				if (server.POP3 != nil && strings.EqualFold(server.POP3.Username, clientUsername)) ||
				   (server.IMAP != nil && strings.EqualFold(server.IMAP.Username, clientUsername)) {
					serverConfig = &server
					log.Printf("[POP3] Found exact match for username: %s", clientUsername)
					break
				}
			}
			
			if serverConfig == nil {
				// If no exact match, try to find any available server
				serverConfig = s.config.GetServerByProtocol("imap")
				if serverConfig == nil {
					serverConfig = s.config.GetServerByProtocol("pop3")
				}
				
				if serverConfig == nil {
					fmt.Fprintf(localConn, "-ERR Invalid username\r\n")
					log.Printf("[POP3] No server configuration found for username: %s", clientUsername)
					continue
				}
				log.Printf("[POP3] Using fallback server config for username: %s", clientUsername)
			}

			fmt.Fprintf(localConn, "+OK User accepted\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK User accepted (mapped to %s)", clientAddr, serverConfig.Name)

		case "PASS":
			if pop3State != "AUTHORIZATION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if serverConfig == nil {
				fmt.Fprintf(localConn, "-ERR USER command first\r\n")
				continue
			}

			// Get the correct upstream config
			upstreamConfig = serverConfig.IMAP
			protocol = "IMAP"
			if upstreamConfig == nil {
				upstreamConfig = serverConfig.POP3
				protocol = "POP3"
			}

			if upstreamConn == nil {
				// Connect to upstream server
				upstreamAddr := fmt.Sprintf("%s:%d", upstreamConfig.Host, upstreamConfig.Port)
				var err error
				if upstreamConfig.UseTLS {
					upstreamConn, err = tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: upstreamConfig.Host})
				} else {
					upstreamConn, err = net.Dial("tcp", upstreamAddr)
				}
				if err != nil {
					log.Printf("[POP3] ERROR: Failed to connect to upstream %s server %s for mailbox %s: %v", 
						protocol, upstreamAddr, upstreamConfig.Username, err)
					fmt.Fprintf(localConn, "-ERR Cannot connect to mail server\r\n")
					return
				}
				defer upstreamConn.Close()

				log.Printf("[POP3] Successfully connected to upstream %s server %s for %s using account %s", 
					protocol, upstreamAddr, clientUsername, upstreamConfig.Username)

				// Initialize scanner for upstream responses
				scanner = bufio.NewScanner(upstreamConn)

				// Read initial greeting from IMAP server
				if scanner.Scan() {
					greeting := scanner.Text()
					log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, greeting)
				}
			}
		
			// Authenticate with IMAP using the correct credentials
			if !authenticated {
				imapTag++
				// Use the correct upstream credentials
				loginCmd := fmt.Sprintf("A%d LOGIN %s %s\r\n", imapTag, upstreamConfig.Username, upstreamConfig.Password)
				fmt.Fprintf(upstreamConn, loginCmd)
				log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d LOGIN %s [hidden]", 
					clientAddr, imapTag, upstreamConfig.Username)

				// Read IMAP response
				for scanner.Scan() {
					response := scanner.Text()
					log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)
					if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
						authenticated = true
						break
					} else if strings.HasPrefix(response, fmt.Sprintf("A%d NO", imapTag)) || strings.HasPrefix(response, fmt.Sprintf("A%d BAD", imapTag)) {
						fmt.Fprintf(localConn, "-ERR Authentication failed\r\n")
						log.Printf("[POP3] PROXY -> CLIENT (%s): -ERR Authentication failed", clientAddr)
						return
					}
				}
			}

			if authenticated {
				// Select INBOX
				if !selectedMailbox {
					imapTag++
					selectCmd := fmt.Sprintf("A%d SELECT INBOX\r\n", imapTag)
					fmt.Fprintf(upstreamConn, selectCmd)
					log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d SELECT INBOX", clientAddr, imapTag)

					// Read SELECT response
					for scanner.Scan() {
						response := scanner.Text()
						log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)

						// Parse EXISTS response
						if strings.Contains(response, "EXISTS") {
							fields := strings.Fields(response)
							if len(fields) >= 2 {
								if count, err := strconv.Atoi(fields[1]); err == nil {
									messageCount = count
									LogInfo("📥 INBOX: Found %d emails for %s", messageCount, upstreamConfig.Username)
								}
							}
						}

						if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
							selectedMailbox = true
							break
						} else if strings.HasPrefix(response, fmt.Sprintf("A%d NO", imapTag)) || strings.HasPrefix(response, fmt.Sprintf("A%d BAD", imapTag)) {
							fmt.Fprintf(localConn, "-ERR Cannot select INBOX\r\n")
							log.Printf("[POP3] PROXY -> CLIENT (%s): -ERR Cannot select INBOX", clientAddr)
							return
						}
					}
				}

				pop3State = "TRANSACTION"
				fmt.Fprintf(localConn, "+OK Mailbox locked and ready\r\n")
				log.Printf("[POP3] PROXY -> CLIENT (%s): +OK Mailbox locked and ready", clientAddr)
			}

		case "STAT":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			// Get mailbox status from IMAP
			totalSize := 0
			for _, msg := range messages {
				totalSize += msg.Size
			}

			fmt.Fprintf(localConn, "+OK %d %d\r\n", messageCount, totalSize)
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK %d %d", clientAddr, messageCount, totalSize)

		case "LIST":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if len(parts) == 1 {
				// LIST all messages
				fmt.Fprintf(localConn, "+OK %d messages\r\n", messageCount)
				for i := 1; i <= messageCount; i++ {
					size := 1024 // Default size, would need IMAP FETCH to get real size
					fmt.Fprintf(localConn, "%d %d\r\n", i, size)
				}
				fmt.Fprintf(localConn, ".\r\n")
				log.Printf("[POP3] PROXY -> CLIENT (%s): Listed %d messages", clientAddr, messageCount)
			} else if len(parts) == 2 {
				// LIST specific message
				if msgNum, err := strconv.Atoi(parts[1]); err == nil && msgNum > 0 && msgNum <= messageCount {
					size := 1024 // Default size
					fmt.Fprintf(localConn, "+OK %d %d\r\n", msgNum, size)
					log.Printf("[POP3] PROXY -> CLIENT (%s): +OK %d %d", clientAddr, msgNum, size)
				} else {
					fmt.Fprintf(localConn, "-ERR No such message\r\n")
				}
			}

		case "UIDL":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if len(parts) == 1 {
				// UIDL all messages
				fmt.Fprintf(localConn, "+OK unique-id listing follows\r\n")
				for i := 1; i <= messageCount; i++ {
					// Generate a simple UID based on message number
					// In a real implementation, you'd get this from IMAP UID FETCH
					uid := fmt.Sprintf("%s.%d", upstreamConfig.Username, i)
					fmt.Fprintf(localConn, "%d %s\r\n", i, uid)
				}
				fmt.Fprintf(localConn, ".\r\n")
				log.Printf("[POP3] PROXY -> CLIENT (%s): UIDL listed %d messages", clientAddr, messageCount)
			} else if len(parts) == 2 {
				// UIDL specific message
				if msgNum, err := strconv.Atoi(parts[1]); err == nil && msgNum > 0 && msgNum <= messageCount {
					uid := fmt.Sprintf("%s.%d", upstreamConfig.Username, msgNum)
					fmt.Fprintf(localConn, "+OK %d %s\r\n", msgNum, uid)
					log.Printf("[POP3] PROXY -> CLIENT (%s): +OK %d %s", clientAddr, msgNum, uid)
				} else {
					fmt.Fprintf(localConn, "-ERR No such message\r\n")
				}
			}

		case "RETR":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if len(parts) != 2 {
				fmt.Fprintf(localConn, "-ERR Invalid syntax\r\n")
				continue
			}

			msgNum, err := strconv.Atoi(parts[1])
			if err != nil || msgNum < 1 || msgNum > messageCount {
				fmt.Fprintf(localConn, "-ERR No such message\r\n")
				continue
			}

			// Fetch message from IMAP
			imapTag++
			fetchCmd := fmt.Sprintf("A%d FETCH %d (RFC822)\r\n", imapTag, msgNum)
			fmt.Fprintf(upstreamConn, fetchCmd)
			log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d FETCH %d (RFC822)", clientAddr, imapTag, msgNum)

			fmt.Fprintf(localConn, "+OK Message follows\r\n")

			// Read and forward IMAP FETCH response
			inMessage := false
			for scanner.Scan() {
				response := scanner.Text()
				log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)

				if strings.Contains(response, "RFC822") {
					inMessage = true
					continue
				}

				if inMessage {
					if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
						break
					} else if strings.HasPrefix(response, ")") {
						continue
					} else {
						fmt.Fprintf(localConn, "%s\r\n", response)
					}
				}
			}

			fmt.Fprintf(localConn, ".\r\n")
			LogInfo("📩 EMAIL DOWNLOADED: Message %d delivered to client for %s", msgNum, upstreamConfig.Username)

		case "TOP":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if len(parts) != 3 {
				fmt.Fprintf(localConn, "-ERR Invalid syntax\r\n")
				continue
			}

			msgNum, err := strconv.Atoi(parts[1])
			if err != nil || msgNum < 1 || msgNum > messageCount {
				fmt.Fprintf(localConn, "-ERR No such message\r\n")
				continue
			}

			lines, err := strconv.Atoi(parts[2])
			if err != nil || lines < 0 {
				fmt.Fprintf(localConn, "-ERR Invalid line count\r\n")
				continue
			}

			// Fetch message headers and body from IMAP
			imapTag++
			fetchCmd := fmt.Sprintf("A%d FETCH %d (RFC822)\r\n", imapTag, msgNum)
			fmt.Fprintf(upstreamConn, fetchCmd)
			log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d FETCH %d (RFC822) for TOP %d lines", clientAddr, imapTag, msgNum, lines)

			fmt.Fprintf(localConn, "+OK Top of message follows\r\n")

			// Read and forward IMAP FETCH response with line limiting
			inMessage := false
			headersDone := false
			bodyLines := 0
			for scanner.Scan() {
				response := scanner.Text()
				log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)

				if strings.Contains(response, "RFC822") {
					inMessage = true
					continue
				}

				if inMessage {
					if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
						break
					} else if strings.HasPrefix(response, ")") {
						continue
					} else {
						// Check if we've reached the end of headers
						if !headersDone && response == "" {
							headersDone = true
							fmt.Fprintf(localConn, "\r\n")
							continue
						}

						// Always send headers
						if !headersDone {
							fmt.Fprintf(localConn, "%s\r\n", response)
						} else {
							// Only send specified number of body lines
							if bodyLines < lines {
								fmt.Fprintf(localConn, "%s\r\n", response)
								bodyLines++
							} else {
								// Skip remaining body lines
								continue
							}
						}
					}
				}
			}

			fmt.Fprintf(localConn, ".\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): TOP of message %d delivered (%d body lines)", clientAddr, msgNum, lines)

		case "DELE":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			if len(parts) != 2 {
				fmt.Fprintf(localConn, "-ERR Invalid syntax\r\n")
				continue
			}

			msgNum, err := strconv.Atoi(parts[1])
			if err != nil || msgNum < 1 || msgNum > messageCount {
				fmt.Fprintf(localConn, "-ERR No such message\r\n")
				continue
			}

			// Mark message for deletion in IMAP
			imapTag++
			storeCmd := fmt.Sprintf("A%d STORE %d +FLAGS (\\Deleted)\r\n", imapTag, msgNum)
			fmt.Fprintf(upstreamConn, storeCmd)
			log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d STORE %d +FLAGS (\\Deleted)", clientAddr, imapTag, msgNum)

			// Read IMAP response
			for scanner.Scan() {
				response := scanner.Text()
				log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)
				if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
					break
				}
			}

			fmt.Fprintf(localConn, "+OK Message %d deleted\r\n", msgNum)
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK Message %d deleted", clientAddr, msgNum)

		case "NOOP":
			fmt.Fprintf(localConn, "+OK\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK", clientAddr)

		case "RSET":
			if pop3State != "TRANSACTION" {
				fmt.Fprintf(localConn, "-ERR Command not valid in this state\r\n")
				continue
			}

			// Remove all deletion marks in IMAP
			imapTag++
			storeCmd := fmt.Sprintf("A%d STORE 1:%d -FLAGS (\\Deleted)\r\n", imapTag, messageCount)
			fmt.Fprintf(upstreamConn, storeCmd)
			log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d STORE 1:%d -FLAGS (\\Deleted)", clientAddr, imapTag, messageCount)

			// Read IMAP response
			for scanner.Scan() {
				response := scanner.Text()
				log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)
				if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
					break
				}
			}

			fmt.Fprintf(localConn, "+OK\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK Reset completed", clientAddr)

		case "QUIT":
			if pop3State == "TRANSACTION" {
				// Expunge deleted messages in IMAP
				imapTag++
				expungeCmd := fmt.Sprintf("A%d EXPUNGE\r\n", imapTag)
				fmt.Fprintf(upstreamConn, expungeCmd)
				log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d EXPUNGE", clientAddr, imapTag)

				// Read IMAP response
				for scanner.Scan() {
					response := scanner.Text()
					log.Printf("[POP3] IMAP-SERVER -> PROXY (%s): %s", clientAddr, response)
					if strings.HasPrefix(response, fmt.Sprintf("A%d OK", imapTag)) {
						break
					}
				}
			}

			// Logout from IMAP
			imapTag++
			logoutCmd := fmt.Sprintf("A%d LOGOUT\r\n", imapTag)
			fmt.Fprintf(upstreamConn, logoutCmd)
			log.Printf("[POP3] PROXY -> IMAP-SERVER (%s): A%d LOGOUT", clientAddr, imapTag)

			fmt.Fprintf(localConn, "+OK Goodbye\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): +OK Goodbye", clientAddr)
			return

		default:
			fmt.Fprintf(localConn, "-ERR Unknown command\r\n")
			log.Printf("[POP3] PROXY -> CLIENT (%s): -ERR Unknown command: %s", clientAddr, command)
		}
	}

	log.Printf("[POP3] Client %s disconnected from IMAP mailbox %s", clientAddr, upstreamConfig.Username)
}

