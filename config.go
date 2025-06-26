package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Name string `yaml:"name"`

	POP3 *MailServerConfig `yaml:"pop3,omitempty"`
	IMAP *MailServerConfig `yaml:"imap,omitempty"`
	SMTP *MailServerConfig `yaml:"smtp,omitempty"`
}

type MailServerConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	UseTLS   bool   `yaml:"use_tls"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type LocalConfig struct {
	POP3 MailServerConfig  `yaml:"pop3"`
	SMTP *MailServerConfig `yaml:"smtp,omitempty"`
	// Note: POP3 and SMTP are supported for local connections (legacy clients)
	// IMAP is only used for upstream connections
}

type Config struct {
	Servers  []ServerConfig `yaml:"servers"`
	Local    LocalConfig    `yaml:"local"`
	LogLevel string         `yaml:"log_level,omitempty"` // "info" or "debug"
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetServerByProtocol returns the first server that supports the given protocol
func (c *Config) GetServerByProtocol(protocol string) *ServerConfig {
	for _, server := range c.Servers {
		switch protocol {
		case "pop3":
			if server.POP3 != nil {
				return &server
			}
		case "imap":
			if server.IMAP != nil {
				return &server
			}
		case "smtp":
			if server.SMTP != nil {
				return &server
			}
		}
	}
	return nil
}
