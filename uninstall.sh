#!/bin/bash

# Proxy-Mail Uninstallation Script

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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

print_status "Starting Proxy-Mail uninstallation..."

# Stop and disable service
if systemctl is-active --quiet proxy-mail; then
    print_status "Stopping proxy-mail service..."
    systemctl stop proxy-mail
    print_success "Service stopped"
fi

if systemctl is-enabled --quiet proxy-mail; then
    print_status "Disabling proxy-mail service..."
    systemctl disable proxy-mail
    print_success "Service disabled"
fi

# Remove systemd service file
if [[ -f "/etc/systemd/system/proxy-mail.service" ]]; then
    print_status "Removing systemd service file..."
    rm /etc/systemd/system/proxy-mail.service
    systemctl daemon-reload
    print_success "Service file removed"
fi

# Remove binary
if [[ -f "/usr/local/bin/proxy-mail" ]]; then
    print_status "Removing binary..."
    rm /usr/local/bin/proxy-mail
    print_success "Binary removed"
fi

# Ask about configuration file
if [[ -f "/etc/proxy-mail.yaml" ]]; then
    echo
    read -p "Remove configuration file /etc/proxy-mail.yaml? [y/N]: " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm /etc/proxy-mail.yaml
        print_success "Configuration file removed"
    else
        print_warning "Configuration file preserved"
    fi
fi

# Ask about user data
if [[ -d "/var/lib/proxy-mail" ]]; then
    echo
    read -p "Remove user data directory /var/lib/proxy-mail? [y/N]: " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm -rf /var/lib/proxy-mail
        print_success "User data directory removed"
    else
        print_warning "User data directory preserved"
    fi
fi

# Ask about logs
if [[ -d "/var/log/proxy-mail" ]]; then
    echo
    read -p "Remove log directory /var/log/proxy-mail? [y/N]: " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm -rf /var/log/proxy-mail
        print_success "Log directory removed"
    else
        print_warning "Log directory preserved"
    fi
fi

# Ask about user account
if id "proxy-mail" &>/dev/null; then
    echo
    read -p "Remove proxy-mail user account? [y/N]: " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        userdel proxy-mail 2>/dev/null || true
        print_success "User account removed"
    else
        print_warning "User account preserved"
    fi
fi

print_success "Uninstallation completed!"

