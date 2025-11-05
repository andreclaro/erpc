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

# Install ws module if not present
if ! node -e "require('ws')" 2>/dev/null; then
    echo "📦 Installing 'ws' module..."
    
    # Use pnpm if available (this repo uses pnpm workspaces)
    if command -v pnpm &> /dev/null; then
        echo "   Using pnpm..."
        pnpm add -w ws
    # Otherwise use npm with --legacy-peer-deps to avoid workspace issues
    elif command -v npm &> /dev/null; then
        echo "   Using npm..."
        npm install ws --no-save --legacy-peer-deps 2>/dev/null || \
        npm install ws --legacy-peer-deps 2>/dev/null || \
        npm install --global ws 2>/dev/null || {
            echo ""
            echo "⚠️  Could not install 'ws' automatically."
            echo "Please install it manually:"
            echo "  pnpm add -w ws"
            echo "  OR"
            echo "  npm install -g ws"
            exit 1
        }
    else
        echo "❌ Error: Neither pnpm nor npm is installed"
        exit 1
    fi
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

