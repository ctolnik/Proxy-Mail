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
	}

	// Start IMAP proxy if configured
	if ps.config.Local.IMAP.Port > 0 {
		imapServer := NewIMAPServer(ps.config)
		ps.servers = append(ps.servers, imapServer)
		ps.wg.Add(1)
		go func() {
			defer ps.wg.Done()
			if err := imapServer.Start(); err != nil {
				log.Printf("IMAP server error: %v", err)
			}
		}()
	}

	// Start SMTP proxy if configured
	if ps.config.Local.SMTP.Port > 0 {
		smtpServer := NewSMTPServer(ps.config)
		ps.servers = append(ps.servers, smtpServer)
		ps.wg.Add(1)
		go func() {
			defer ps.wg.Done()
			if err := smtpServer.Start(); err != nil {
				log.Printf("SMTP server error: %v", err)
			}
		}()
	}

	return nil
}

func (ps *ProxyService) Stop() {
	for _, server := range ps.servers {
		server.Stop()
	}
	ps.wg.Wait()
}

