# WebSocket Tests

Test suite for eRPC's WebSocket subscription implementation.

## 🧪 Test Files

### Automated Tests
- **`test-websocket.sh`** - Test runner script
  - Checks dependencies
  - Installs ws module if needed
  - Runs the comprehensive test suite

- **`test-websocket.js`** - Comprehensive Node.js test suite
  - Tests `eth_subscribe` (newHeads, logs)
  - Tests `eth_unsubscribe`
  - Tests JSON-RPC methods over WebSocket
  - Validates notifications and responses

### Manual/Interactive Tests
- **`test_ws.html`** - Browser-based testing
  - Open in browser to test subscriptions
  - Interactive UI for debugging

- **`test-ws-quick.js`** - Quick connection test
  - Fast sanity check
  - Waits for first block notification

### Configuration
- **`test-ws-config.yaml`** - Minimal eRPC config for testing
  - Configured for Base Sepolia testnet
  - WebSocket enabled
  - Single upstream

- **`test-ws-package.json`** - Package metadata for test isolation

## 🚀 Quick Start

### Run Automated Tests

```bash
# From the repository root
./test/websocket/test-websocket.sh
```

### Manual Testing with Browser

```bash
# Start eRPC server
./bin/erpc-server -config test/websocket/test-ws-config.yaml

# Open in browser
open test/websocket/test_ws.html
```

### Quick Connection Test

```bash
# Start server
./bin/erpc-server -config test/websocket/test-ws-config.yaml

# In another terminal
cd test/websocket
node test-ws-quick.js
```

## 📋 Test Coverage

### Subscription Methods
- ✅ `eth_subscribe("newHeads")`
- ✅ `eth_subscribe("logs")`
- ✅ `eth_subscribe("logs", filter)`
- ✅ `eth_unsubscribe(subId)`

### JSON-RPC Methods
- ✅ `eth_blockNumber`
- ✅ `eth_chainId`
- ✅ `eth_gasPrice`
- ✅ `net_version`
- ✅ `eth_getBlockByNumber`
- ✅ `eth_getBalance`

### Notifications
- ✅ `newHeads` block notifications
- ✅ `logs` event notifications
- ✅ Continuous streaming
- ✅ Connection stability

## 🔧 Requirements

### For Automated Tests
- Node.js (v14+)
- `ws` npm module (auto-installed by script)

### For Server
- Go 1.21+
- eRPC built (`make build`)

## 📊 Expected Results

When running `test-websocket.sh`, you should see:

```
✅ WebSocket connected!
✅ newHeads: Subscribed with ID 0x...
✅ newHeads: First notification received!
📬 newHeads: Block 0x... (multiple notifications)
✅ logs: Subscribed with ID 0x...
📬 logs: Multiple log notifications
✅ JSON-RPC tests: 6/6 passed
✅ Unsubscribed successfully
✅ All tests passed!
```

## 🐛 Troubleshooting

### Server Not Running
```bash
ps aux | grep erpc-server
```

### Connection Refused
Check that server is listening on port 4000:
```bash
lsof -i :4000
```

### No Notifications
- Verify network has activity (Base Sepolia recommended)
- Check server logs for errors
- Ensure poll interval is appropriate (default 2s)

### ws Module Not Found
The test script should auto-install it, but you can manually:
```bash
cd test/websocket
npm install ws
```

## 📚 Related Documentation

See the [../../docs/websocket/](../../docs/websocket/) directory for:
- Testing guide: [WS_TESTING.md](../../docs/websocket/WS_TESTING.md)
- Quick start: [QUICKSTART_WS.md](../../docs/websocket/QUICKSTART_WS.md)
- Full documentation: [README.md](../../docs/websocket/README.md)

## 🎯 Test Networks

### Recommended for Testing
- **Base Sepolia** (84532) - Fast blocks (~2s), active network
- **Ethereum Sepolia** (11155111) - Standard testnet

### Production Networks
- **Ethereum Mainnet** (1)
- **Base** (8453)
- Any EVM-compatible chain

## 🔍 Debugging

### Enable Debug Logging
```bash
LOG_LEVEL=debug ./bin/erpc-server -config test/websocket/test-ws-config.yaml
```

### Check WebSocket Connection
```bash
# Install wscat if not available
npm install -g wscat

# Connect manually
wscat -c ws://localhost:4000/main/evm/84532
```

### Monitor Metrics
If Prometheus is configured, check:
- `erpc_websocket_connections_active`
- `erpc_websocket_subscriptions_active`
- `erpc_websocket_notifications_sent_total`

## ✅ Success Criteria

Tests pass when:
- WebSocket connection establishes
- Subscriptions are created
- Notifications are received
- Unsubscribe works
- JSON-RPC calls succeed
- No errors or crashes

