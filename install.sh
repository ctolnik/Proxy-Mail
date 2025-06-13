#!/bin/bash

# Proxy-Mail Installation Script
# This script installs the proxy-mail service as a systemd service

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root
if [[ $EUID -ne 0 ]]; then
    print_error "This script must be run as root (use sudo)"
    exit 1
fi

# Check if binary exists
if [[ ! -f "proxy-mail" ]]; then
    print_error "proxy-mail binary not found. Please run 'go build' first."
    exit 1
fi

print_status "Starting Proxy-Mail installation..."

# Create user and group
print_status "Creating proxy-mail user and group..."
if ! id "proxy-mail" &>/dev/null; then
    useradd --system --home-dir /var/lib/proxy-mail --create-home --shell /bin/false proxy-mail
    print_success "Created proxy-mail user"
else
    print_warning "User proxy-mail already exists"
fi

# Create directories
print_status "Creating directories..."
mkdir -p /var/lib/proxy-mail
mkdir -p /var/log/proxy-mail

# Set ownership
chown -R proxy-mail:proxy-mail /var/lib/proxy-mail
chown -R proxy-mail:proxy-mail /var/log/proxy-mail

# Install binary
print_status "Installing binary to /usr/local/bin/proxy-mail..."
cp proxy-mail /usr/local/bin/proxy-mail
chown root:root /usr/local/bin/proxy-mail
chmod 755 /usr/local/bin/proxy-mail
print_success "Binary installed"

# Install systemd service
print_status "Installing systemd service..."
cp proxy-mail.service /etc/systemd/system/
chown root:root /etc/systemd/system/proxy-mail.service
chmod 644 /etc/systemd/system/proxy-mail.service
print_success "Systemd service installed"

# Install configuration file if it doesn't exist
if [[ ! -f "/etc/proxy-mail.yaml" ]]; then
    print_status "Installing default configuration..."
    if [[ -f "config-example.yaml" ]]; then
        cp config-example.yaml /etc/proxy-mail.yaml
        chown root:proxy-mail /etc/proxy-mail.yaml
        chmod 640 /etc/proxy-mail.yaml
        print_success "Configuration file installed to /etc/proxy-mail.yaml"
        print_warning "Please edit /etc/proxy-mail.yaml with your email server settings before starting the service"
    else
        print_error "config-example.yaml not found. You'll need to create /etc/proxy-mail.yaml manually"
    fi
else
    print_warning "Configuration file /etc/proxy-mail.yaml already exists, not overwriting"
fi

# Reload systemd
print_status "Reloading systemd..."
systemctl daemon-reload
print_success "Systemd reloaded"

# Enable service
print_status "Enabling proxy-mail service..."
systemctl enable proxy-mail.service
print_success "Service enabled"

print_success "Installation completed successfully!"
echo
print_status "Next steps:"
echo "  1. Edit /etc/proxy-mail.yaml with your email server settings"
echo "  2. Start the service: sudo systemctl start proxy-mail"
echo "  3. Check status: sudo systemctl status proxy-mail"
echo "  4. View logs: sudo journalctl -u proxy-mail -f"
echo
print_status "Security note: The configuration file contains passwords and is only readable by root and proxy-mail group"

