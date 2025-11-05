#!/bin/bash

# WebSocket Test Runner Script
# Installs dependencies and runs the WebSocket test

set -e

echo "🧪 eRPC WebSocket Test Runner"
echo "=============================="
echo ""

# Check if Node.js is installed
if ! command -v node &> /dev/null; then
    echo "❌ Error: Node.js is not installed"
    echo "Please install Node.js from https://nodejs.org/"
    exit 1
fi

echo "✅ Node.js version: $(node --version)"

# Check if npm is installed
if ! command -v npm &> /dev/null; then
    echo "❌ Error: npm is not installed"
    exit 1
fi

# Install ws module if not present
if ! node -e "require('ws')" 2>/dev/null; then
    echo "📦 Installing 'ws' module..."
    npm install ws --no-save
    echo ""
fi

# Check if eRPC is running
echo "🔍 Checking if eRPC is running..."
if ! curl -s http://localhost:4000/healthcheck > /dev/null 2>&1; then
    echo "⚠️  Warning: eRPC doesn't seem to be running on localhost:4000"
    echo ""
    echo "Start eRPC with:"
    echo "  ./bin/erpc-server -config test-ws-config.yaml"
    echo ""
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
else
    echo "✅ eRPC is running"
fi

echo ""
echo "🚀 Starting WebSocket tests..."
echo ""

# Run the test script
node test-websocket.js "$@"

