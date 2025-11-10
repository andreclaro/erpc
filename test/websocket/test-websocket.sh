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

# Install ws module in isolated directory to avoid workspace issues
if [ ! -d ".test-ws/node_modules/ws" ]; then
    echo "📦 Installing 'ws' module..."
    
    mkdir -p .test-ws
    cd .test-ws
    
    if [ ! -f "package.json" ]; then
        echo '{"name":"erpc-ws-test","private":true}' > package.json
    fi
    
    # Install using npm
    if command -v npm &> /dev/null; then
        npm install ws --silent 2>/dev/null || {
            echo ""
            echo "⚠️  Could not install 'ws' automatically."
            exit 1
        }
    else
        echo "❌ Error: npm is not installed"
        exit 1
    fi
    
    cd ..
    echo ""
fi

# Set NODE_PATH to find ws module
export NODE_PATH=".test-ws/node_modules:$NODE_PATH"

# Check if eRPC is running
echo "🔍 Checking if eRPC is running..."
if ! curl -s http://localhost:4000/healthcheck > /dev/null 2>&1; then
    echo "⚠️  Warning: eRPC doesn't seem to be running on localhost:4000"
    echo ""
    echo "Start eRPC with:"
    echo "  ./bin/erpc-server -config test/websocket/test-ws-config.yaml"
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

# Get the directory where this script is located
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Run the test script from the correct directory
cd "$SCRIPT_DIR"
node test-websocket.js "$@"

