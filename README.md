# Proxy-Mail

A Go-based email proxy service designed specifically for **legacy email clients** that only support basic POP3/SMTP protocols. This service bridges insecure legacy clients with modern secure email providers.

## Key Features

- **Legacy Client Support**: Provides both POP3 and SMTP access for old email clients that can't handle modern security
- **Protocol Translation**: Automatically translates between POP3 (client) and POP3/IMAP (server)
- **Dual Protocol Support**: Supports both incoming mail (POP3/IMAP) and outgoing mail (SMTP)
- **Upstream Flexibility**: Can connect to upstream servers using POP3, IMAP, or SMTP protocols
- **Security Bridge**: Handles TLS/SSL upstream while providing unencrypted local POP3/SMTP
- **Multi-mailbox**: Support for multiple email accounts from different providers
- **Transparent Authentication**: Automatically handles authentication with upstream servers
- **Enhanced Logging**: Detailed logging of all operations and protocol translations

## Architecture

```
Legacy Client ----[POP3/SMTP Unencrypted]----> Proxy-Mail ----[POP3/IMAP/SMTP + TLS]----> Mail Provider
(Port 110/25)                                                                            (Gmail, Outlook, etc)
```

### Protocol Support

- **Local (Client-facing)**: POP3 (port 110) and SMTP (port 25/587), both unencrypted
- **Upstream (Server-facing)**: POP3, IMAP, and SMTP (with TLS/SSL support)
- **Automatic Fallback**: For incoming mail, prefers POP3 upstream, falls back to IMAP if POP3 unavailable
- **Protocol Translation**: POP3 client commands → IMAP server commands (when needed)
- **Transparent Authentication**: Handles authentication for both incoming (POP3/IMAP) and outgoing (SMTP) mail

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

4. **Configure your legacy email client** to connect to:
   - **Incoming (POP3)**: `localhost:110` (unencrypted)
   - **Outgoing (SMTP)**: `localhost:25` (unencrypted) 
   - Use any username/password (will be ignored)

## Configuration

### Basic Configuration Structure

```yaml
servers:
  - name: "account-name"
    # Configure POP3 upstream (preferred)
    pop3:
      host: "pop.provider.com"
      port: 995
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"
    # Optional: Configure IMAP upstream (fallback)
    imap:
      host: "imap.provider.com"
      port: 993
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"
    # Optional: Configure SMTP upstream (for outgoing mail)
    smtp:
      host: "smtp.provider.com"
      port: 587
      use_tls: true
      username: "your-email@provider.com"
      password: "your-password"

# Local servers for legacy clients
local:
  pop3:
    host: "0.0.0.0"  # Listen on all interfaces  
    port: 110         # Standard POP3 port
    use_tls: false    # No encryption for legacy clients
  smtp:
    host: "0.0.0.0"  # Listen on all interfaces
    port: 25          # Standard SMTP port (or use 587, 2525)
    use_tls: false    # No encryption for legacy clients
```

### Protocol Selection Logic

1. **POP3 Preferred**: If `pop3` is configured, proxy uses POP3 → POP3
2. **IMAP Fallback**: If only `imap` is configured, proxy translates POP3 → IMAP
3. **Both Configured**: POP3 takes precedence, IMAP is ignored
4. **Neither Configured**: Connection fails with error

### Multiple Mailboxes on Same Server

For multiple mailboxes on the same email provider, you have two options:

#### Option 1: Multiple Proxy Instances (Recommended)

Create separate configuration files for each mailbox:

**config-personal.yaml**:
```yaml
servers:
  - name: "personal-gmail"
    pop3:  # Preferred upstream protocol
      host: "pop.gmail.com"
      port: 995
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"
    imap:  # Fallback upstream protocol
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"

local:
  pop3:
    host: "0.0.0.0"
    port: 1110  # Custom port for personal account
```

**config-work.yaml**:
```yaml
servers:
  - name: "work-gmail"
    imap:  # IMAP-only upstream (will use POP3->IMAP translation)
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "work@gmail.com"
      password: "app-password-2"

local:
  pop3:
    host: "0.0.0.0"
    port: 2110  # Custom port for work account
```

Then run multiple instances:
```bash
./proxy-mail -config config-personal.yaml &  # POP3->POP3
./proxy-mail -config config-work.yaml &     # POP3->IMAP
```

#### Option 2: Single Instance (First Account Only)

```yaml
servers:
  - name: "personal-gmail"    # This account will be used
    pop3:
      host: "pop.gmail.com"
      port: 995
      use_tls: true
      username: "personal@gmail.com"
      password: "app-password-1"
  
  - name: "work-gmail"        # This account will be ignored
    imap:
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "work@gmail.com"
      password: "app-password-2"

local:
  pop3:
    port: 110
```

**Note**: Only the first configured account will be used in single-instance mode.

### Legacy Email Client Examples

Configure your legacy email client to connect to the proxy:

#### For Single Instance (Default Port)
- **Incoming Server**: `localhost`
- **Port**: `110`
- **Security**: `None` or `No Encryption`
- **Authentication**: `Normal Password`
- **Username**: `any` (ignored)
- **Password**: `any` (ignored)

#### For Multiple Instances (Custom Ports)
- **Personal Account**: `localhost:1110`
- **Work Account**: `localhost:2110`
- **Business Account**: `localhost:3110`
- (Same security settings as above)

## Legacy Email Client Configuration

### Thunderbird Example

1. **Add New Account**:
   - Email: `any@example.com` (will be ignored)
   - Password: `any-password` (will be ignored)

2. **Manual Configuration**:
   - **Incoming Server (POP3 ONLY)**:
     - Server: `localhost`
     - Port: `110`
     - Security: `None`
     - Authentication: `Normal password`
   
   - **Outgoing Server**: Not supported in current version

### Outlook Express / Windows Mail Example

1. **Add Account → Manual setup**
2. **POP3 Configuration**:
   - **Incoming mail server**: `localhost`
   - **Port**: `110`
   - **Security**: `None`
   - **Username**: `any` (ignored)
   - **Password**: `any` (ignored)

### Generic Legacy Client

- **Protocol**: POP3
- **Server**: `localhost` (or IP address of proxy server)
- **Port**: `110` (or custom port if using multiple instances)
- **Encryption**: None/Disabled
- **Authentication**: Normal/Plain
- **Username/Password**: Any values (completely ignored)

## Provider-Specific Settings

### Gmail (Recommended: POP3 + IMAP Fallback)
```yaml
servers:
  - name: "gmail-account"
    pop3:  # Primary - Direct POP3 connection
      host: "pop.gmail.com"
      port: 995
      use_tls: true
      username: "your-email@gmail.com"
      password: "your-app-password"
    imap:  # Fallback - POP3->IMAP translation
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "your-email@gmail.com"
      password: "your-app-password"
```

### Gmail (IMAP-only with Translation)
```yaml
servers:
  - name: "gmail-imap-only"
    imap:  # Will use POP3->IMAP translation
      host: "imap.gmail.com"
      port: 993
      use_tls: true
      username: "your-email@gmail.com"
      password: "your-app-password"
```

### Outlook/Hotmail
```yaml
servers:
  - name: "outlook-account"
    pop3:  # Primary
      host: "outlook.office365.com"
      port: 995
      use_tls: true
      username: "your-email@outlook.com"
      password: "your-password"
    imap:  # Fallback
      host: "outlook.office365.com"
      port: 993
      use_tls: true
      username: "your-email@outlook.com"
      password: "your-password"
```

### Yahoo
```yaml
servers:
  - name: "yahoo-account"
    pop3:  # Primary
      host: "pop.mail.yahoo.com"
      port: 995
      use_tls: true
      username: "your-email@yahoo.com"
      password: "your-app-password"
    imap:  # Fallback
      host: "imap.mail.yahoo.com"
      port: 993
      use_tls: true
      username: "your-email@yahoo.com"
      password: "your-app-password"
```

### Yandex
```yaml
servers:
  - name: "yandex-account"
    pop3:  # Primary
      host: "pop.yandex.com"
      port: 995
      use_tls: true
      username: "your-email@yandex.com"
      password: "your-password"
    imap:  # Fallback
      host: "imap.yandex.com"
      port: 993
      use_tls: true
      username: "your-email@yandex.com"
      password: "your-password"
```

## Security Considerations

⚠️ **Important Security Notes**:

1. **Local Network Only**: This proxy is designed for local network use only. Never expose port 110 to the internet.

2. **App Passwords**: For Gmail and many other providers, use app-specific passwords instead of your main account password.

3. **Firewall**: Ensure your firewall blocks external access to proxy port 110 (and custom ports if using multiple instances).

4. **Network**: Only use on trusted local networks.

5. **Legacy Clients Only**: This service is specifically designed for legacy email clients that cannot handle modern security protocols.

6. **Read-Only**: Current implementation is optimized for reading email. Sending capabilities are not implemented.

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
   - Ensure upstream server supports the configured protocol (POP3 or IMAP)

4. **Protocol Issues**:
   - If POP3 connection fails, check if IMAP is configured as fallback
   - For Gmail: POP3 may be disabled, use IMAP-only configuration
   - Check logs for "POP3->IMAP translation" messages

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
