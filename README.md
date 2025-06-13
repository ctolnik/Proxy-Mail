# Proxy-Mail

A Go-based email proxy service that bridges insecure legacy email clients with modern secure email providers. This service allows you to:

- Receive mail via POP3/IMAP from encrypted upstream servers and serve it locally without encryption
- Send mail via SMTP to encrypted upstream servers from local unencrypted clients
- Handle multiple mailboxes from different or same email providers
- Automatically handle authentication with upstream servers

## Features

- **Protocol Support**: POP3, IMAP4, and SMTP proxying
- **Security**: Handles TLS/SSL connections to upstream servers while providing unencrypted local access
- **Multi-mailbox**: Support for multiple email accounts on same or different providers
- **YAML Configuration**: Easy configuration via YAML files
- **Transparent Authentication**: Automatically handles authentication with upstream servers

## Quick Start

1. **Build the service**:
   ```bash
   go build -o proxy-mail
   ```

2. **Configure your mailboxes** (see Configuration section below)

3. **Run the service**:
   ```bash
   ./proxy-mail -config config.yaml
   ```

4. **Configure your email client** to connect to:
   - POP3: `localhost:110` (unencrypted)
   - IMAP: `localhost:143` (unencrypted)
   - SMTP: `localhost:25` (unencrypted)

## Configuration

### Basic Configuration Structure

```yaml
servers:
  - name: "account-name"
    pop3:
      host: "pop.provider.com"
      port: 995
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"
    imap:
      host: "imap.provider.com"
      port: 993
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"
    smtp:
      host: "smtp.provider.com"
      port: 587
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"

local:
  pop3:
    host: "0.0.0.0"
    port: 110
    use_tls: false
  imap:
    host: "0.0.0.0"
    port: 143
    use_tls: false
  smtp:
    host: "0.0.0.0"
    port: 25
    use_tls: false
```

### Multiple Mailboxes on Same Server

For multiple mailboxes on the same email provider, create separate server entries:

```yaml
servers:
  # First Gmail account
  - name: "personal-gmail"
    pop3:
      host: "pop.gmail.com"
      port: 995
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"
    imap:
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"
    smtp:
      host: "smtp.gmail.com"
      port: 587
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"

  # Second Gmail account
  - name: "work-gmail"
    pop3:
      host: "pop.gmail.com"
      port: 995
      use_tls: true
      username: "work@gmail.com"
      password: "app-password-2"
    imap:
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "work@gmail.com"
      password: "app-password-2"
    smtp:
      host: "smtp.gmail.com"
      port: 587
      use_tls: true
      username: "work@gmail.com"
      password: "app-password-2"

  # Different provider
  - name: "business-outlook"
    pop3:
      host: "outlook.office365.com"
      port: 995
      use_tls: true
      username: "business@company.com"
      password: "outlook-password"
    # ... similar for imap and smtp
```

### Running Multiple Proxy Instances

For handling multiple mailboxes with separate local ports, create different configuration files:

**config-personal.yaml**:
```yaml
servers:
  - name: "personal-gmail"
    # ... personal account config

local:
  pop3:
    host: "0.0.0.0"
    port: 1110  # Different port
  imap:
    host: "0.0.0.0"
    port: 1143  # Different port
  smtp:
    host: "0.0.0.0"
    port: 1025  # Different port
```

**config-work.yaml**:
```yaml
servers:
  - name: "work-gmail"
    # ... work account config

local:
  pop3:
    host: "0.0.0.0"
    port: 2110  # Different port
  imap:
    host: "0.0.0.0"
    port: 2143  # Different port
  smtp:
    host: "0.0.0.0"
    port: 2025  # Different port
```

Then run multiple instances:
```bash
./proxy-mail -config config-personal.yaml &
./proxy-mail -config config-work.yaml &
```

## Email Client Configuration

### Thunderbird Example

1. **Add New Account**:
   - Email: `your-email@provider.com`
   - Password: `any-password` (will be ignored)

2. **Manual Configuration**:
   - **Incoming Server (IMAP)**:
     - Server: `localhost`
     - Port: `143`
     - Security: `None`
     - Authentication: `Normal password`
   
   - **Incoming Server (POP3)**:
     - Server: `localhost`
     - Port: `110`
     - Security: `None`
     - Authentication: `Normal password`
   
   - **Outgoing Server (SMTP)**:
     - Server: `localhost`
     - Port: `25`
     - Security: `None`
     - Authentication: `Normal password`

### Outlook Example

1. **File → Add Account → Manual setup**
2. **POP or IMAP**:
   - **Incoming mail server**: `localhost`
   - **Port**: `143` (IMAP) or `110` (POP3)
   - **Encryption**: `None`
   - **Outgoing mail server**: `localhost`
   - **Port**: `25`
   - **Encryption**: `None`

## Provider-Specific Settings

### Gmail
```yaml
servers:
  - name: "gmail-account"
    pop3:
      host: "pop.gmail.com"
      port: 995
      use_tls: true
    imap:
      host: "imap.gmail.com"
      port: 993
      use_tls: true
    smtp:
      host: "smtp.gmail.com"
      port: 587  # or 465 for SSL
      use_tls: true
```

### Outlook/Hotmail
```yaml
servers:
  - name: "outlook-account"
    pop3:
      host: "outlook.office365.com"
      port: 995
      use_tls: true
    imap:
      host: "outlook.office365.com"
      port: 993
      use_tls: true
    smtp:
      host: "smtp-mail.outlook.com"
      port: 587
      use_tls: true
```

### Yahoo
```yaml
servers:
  - name: "yahoo-account"
    pop3:
      host: "pop.mail.yahoo.com"
      port: 995
      use_tls: true
    imap:
      host: "imap.mail.yahoo.com"
      port: 993
      use_tls: true
    smtp:
      host: "smtp.mail.yahoo.com"
      port: 587
      use_tls: true
```

### Yandex
```yaml
servers:
  - name: "yandex-account"
    pop3:
      host: "pop.yandex.com"
      port: 995
      use_tls: true
    imap:
      host: "imap.yandex.com"
      port: 993
      use_tls: true
    smtp:
      host: "smtp.yandex.com"
      port: 587
      use_tls: true
```

## Security Considerations

⚠️ **Important Security Notes**:

1. **Local Network Only**: This proxy is designed for local network use only. Never expose the unencrypted ports to the internet.

2. **App Passwords**: For Gmail and many other providers, use app-specific passwords instead of your main account password.

3. **Firewall**: Ensure your firewall blocks external access to the proxy ports (25, 110, 143).

4. **Network**: Only use on trusted local networks.

## Troubleshooting

### Common Issues

1. **Connection Refused**:
   - Check if the service is running
   - Verify port numbers in configuration
   - Check firewall settings

2. **Authentication Failed**:
   - Verify username/password in config
   - For Gmail: ensure 2FA is enabled and you're using app passwords
   - Check if the email provider requires "Less secure app access"

3. **TLS Errors**:
   - Verify `use_tls` settings match provider requirements
   - Check port numbers (993/995 for TLS, 143/110 for plain)

### Advanced Logging Options

**Development/Testing** (detailed logs to console):
```bash
./proxy-mail -config config.yaml
```

**Production with log file**:
```bash
./proxy-mail -config config.yaml 2>&1 | tee proxy-mail.log
```

**Systemd service logs** (when installed as service):
```bash
# Real-time logs
sudo journalctl -u proxy-mail -f

# Logs with specific time range
sudo journalctl -u proxy-mail --since "2024-06-13 10:00:00" --until "2024-06-13 12:00:00"

# Only error logs
sudo journalctl -u proxy-mail -p err

# Export logs to file
sudo journalctl -u proxy-mail --since today > proxy-mail-today.log
```

**Log Filtering Examples**:
```bash
# Show only POP3 connections
sudo journalctl -u proxy-mail | grep "\[POP3\]"

# Show authentication events
sudo journalctl -u proxy-mail | grep -E "(USER|LOGIN|AUTH)"

# Show client connections
sudo journalctl -u proxy-mail | grep "Client connected"

# Show errors only
sudo journalctl -u proxy-mail | grep "ERROR"
```

## Enhanced Logging

The service now provides detailed logging for all operations:

### Log Format
- `[POP3]`, `[IMAP]`, `[SMTP]` prefixes identify the protocol
- Client IP addresses are tracked for each connection
- Upstream server connections show TLS status and mailbox
- All commands and responses are logged (passwords are hidden)
- Authentication credential replacement is logged

### Example Log Output
```
2024-06-13T17:45:01Z [POP3] Client connected from 192.168.1.100:52341
2024-06-13T17:45:01Z [POP3] Using server config 'personal-gmail' for client 192.168.1.100:52341
2024-06-13T17:45:01Z [POP3] Connecting to upstream server pop.gmail.com:995 (TLS: true) for mailbox personal@gmail.com
2024-06-13T17:45:02Z [POP3] Successfully connected to upstream server pop.gmail.com:995 for mailbox personal@gmail.com
2024-06-13T17:45:02Z [POP3] Started downstream proxy (server -> client) for 192.168.1.100:52341
2024-06-13T17:45:02Z [POP3] Started upstream proxy (client -> server) for 192.168.1.100:52341
2024-06-13T17:45:02Z [POP3] SERVER -> CLIENT (192.168.1.100:52341): +OK POP3 server ready
2024-06-13T17:45:03Z [POP3] CLIENT -> SERVER (192.168.1.100:52341): USER [client_provided] -> USER personal@gmail.com
2024-06-13T17:45:03Z [POP3] CLIENT -> SERVER (192.168.1.100:52341): PASS [client_provided] -> PASS [hidden]
```

## Running as systemd Service

### Automated Installation (Recommended)

1. **Build the service**:
   ```bash
   go build -o proxy-mail
   ```

2. **Run the installation script**:
   ```bash
   chmod +x install.sh
   sudo ./install.sh
   ```

3. **Edit configuration**:
   ```bash
   sudo nano /etc/proxy-mail.yaml
   ```

4. **Start the service**:
   ```bash
   sudo systemctl start proxy-mail
   sudo systemctl status proxy-mail
   ```

### Manual Installation

If you prefer manual installation:

1. **Create user and directories**:
   ```bash
   sudo useradd --system --home-dir /var/lib/proxy-mail --create-home --shell /bin/false proxy-mail
   sudo mkdir -p /var/lib/proxy-mail /var/log/proxy-mail
   sudo chown -R proxy-mail:proxy-mail /var/lib/proxy-mail /var/log/proxy-mail
   ```

2. **Install binary and service**:
   ```bash
   sudo cp proxy-mail /usr/local/bin/
   sudo cp proxy-mail.service /etc/systemd/system/
   sudo cp config-example.yaml /etc/proxy-mail.yaml
   sudo chown root:proxy-mail /etc/proxy-mail.yaml
   sudo chmod 640 /etc/proxy-mail.yaml
   ```

3. **Enable and start service**:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable proxy-mail
   sudo systemctl start proxy-mail
   ```

### Service Management Commands

```bash
# Check service status
sudo systemctl status proxy-mail

# Start service
sudo systemctl start proxy-mail

# Stop service
sudo systemctl stop proxy-mail

# Restart service
sudo systemctl restart proxy-mail

# View logs (real-time)
sudo journalctl -u proxy-mail -f

# View recent logs
sudo journalctl -u proxy-mail --since "1 hour ago"

# View logs with timestamps
sudo journalctl -u proxy-mail -o short-iso
```

### Security Features

The systemd service includes security hardening:
- Runs as non-privileged `proxy-mail` user
- Private `/tmp` and `/dev` filesystems
- Protected `/home` and read-only system directories
- Restricted system calls and kernel access
- Memory execution protection
- Configuration file protected with `640` permissions

### Uninstallation

To remove the service:
```bash
chmod +x uninstall.sh
sudo ./uninstall.sh
```

### macOS (launchd)

Create `~/Library/LaunchAgents/com.proxy-mail.plist`:
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.proxy-mail</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/proxy-mail</string>
        <string>-config</string>
        <string>/usr/local/etc/proxy-mail/config.yaml</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
```

## License

See LICENSE file for details.
