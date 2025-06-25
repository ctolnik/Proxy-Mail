package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create proxy service
	proxyService := NewProxyService(cfg)

	// Start all proxy servers
	if err := proxyService.Start(); err != nil {
		log.Fatalf("Failed to start proxy service: %v", err)
	}

	log.Println("Email proxy service started successfully")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down email proxy service...")
	proxyService.Stop()
	log.Println("Email proxy service stopped")
}

type ProxyService struct {
	config  *Config
	servers []Server
	wg      sync.WaitGroup
}

type Server interface {
	Start() error
	Stop() error
}

func NewProxyService(config *Config) *ProxyService {
	return &ProxyService{
		config: config,
	}
}

func (ps *ProxyService) Start() error {
	// Start POP3 proxy if configured
	if ps.config.Local.POP3.Port > 0 {
		pop3Server := NewPOP3Server(ps.config)
		ps.servers = append(ps.servers, pop3Server)
		ps.wg.Add(1)
		go func() {
			defer ps.wg.Done()
			if err := pop3Server.Start(); err != nil {
				log.Printf("POP3 server error: %v", err)
			}
		}()
		log.Printf("Started POP3 proxy server on port %d", ps.config.Local.POP3.Port)
	} else {
		log.Printf("POP3 proxy server disabled (port not configured)")
	}

	// Start SMTP proxy if configured
	if ps.config.Local.SMTP != nil && ps.config.Local.SMTP.Port > 0 {
		smtpServer := NewSMTPServer(ps.config)
		ps.servers = append(ps.servers, smtpServer)
		ps.wg.Add(1)
		go func() {
			defer ps.wg.Done()
			if err := smtpServer.Start(); err != nil {
				log.Printf("SMTP server error: %v", err)
			}
		}()
		log.Printf("Started SMTP proxy server on port %d", ps.config.Local.SMTP.Port)
	} else {
		log.Printf("SMTP proxy server disabled (not configured or port not set)")
	}

	// Service capabilities summary
	log.Printf("Proxy-Mail service supports:")
	log.Printf("  - Local POP3 server for legacy clients (incoming mail)")
	log.Printf("  - Local SMTP server for legacy clients (outgoing mail)")
	log.Printf("  - Upstream POP3, IMAP, and SMTP connections")
	log.Printf("  - Automatic protocol translation (POP3 client <-> IMAP server)")
	log.Printf("  - Transparent authentication for both incoming and outgoing mail")

	return nil
}

func (ps *ProxyService) Stop() {
	for _, server := range ps.servers {
		server.Stop()
	}
	ps.wg.Wait()
}

