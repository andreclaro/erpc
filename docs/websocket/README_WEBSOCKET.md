# WebSocket Support - Quick Start

## 🎉 Features

- ✅ **WebSocket Subscriptions** - `eth_subscribe` and `eth_unsubscribe`
- ✅ **newHeads** - Real-time block notifications
- ✅ **logs** - Real-time log notifications with filtering
- ✅ **Regular RPC calls** - All JSON-RPC methods work over WebSocket
- ✅ **Same port as HTTP** - Automatic WebSocket upgrade
- ✅ **Enabled by default** - No extra configuration needed

---

## 🚀 Quick Test

### 1. Start eRPC

```bash
# Build
make build

# Run with test config
./bin/erpc-server -config test-ws-config.yaml
```

### 2. Run Automated Test

```bash
# One-liner (installs dependencies and runs tests)
./test-websocket.sh

# Or manually with Node.js
npm install ws
node test-websocket.js
```

**Expected output:**
```
🧪 eRPC WebSocket Subscription Test
====================================

✅ WebSocket connected!
--- Test 1: newHeads Subscription ---
✅ newHeads: Subscribed with ID 0x...
📬 newHeads: Block 0x... (0x...)
✅ newHeads: First notification received!

--- Test 2: logs Subscription ---
✅ logs: Subscribed with ID 0x...

--- Test 3: JSON-RPC Calls over WebSocket ---
✅ eth_blockNumber: 0x...
✅ eth_chainId: 0x1
✅ eth_gasPrice: 0x...

--- Test 4: Unsubscribe ---
✅ Unsubscribed from 0x...

Test Summary
============
Duration: 15234ms
Subscriptions:
  - newHeads notifications: 3
  - logs notifications: 5
  - Subscriptions created: 2
JSON-RPC Calls:
  - Tests passed: 6/6

✅ All tests passed!
```

---

## 📡 WebSocket URL Format

```
ws://localhost:4000/{projectId}/{architecture}/{chainId}
```

**Examples:**
- `ws://localhost:4000/main/evm/1` - Ethereum Mainnet
- `ws://localhost:4000/main/evm/137` - Polygon
- `ws://localhost:4000/main/evm/42161` - Arbitrum One

---

## 💻 Usage Examples

### Subscribe to New Blocks

```javascript
const ws = new WebSocket('ws://localhost:4000/main/evm/1');

ws.onopen = () => {
  ws.send(JSON.stringify({
    jsonrpc: '2.0',
    id: 1,
    method: 'eth_subscribe',
    params: ['newHeads']
  }));
};

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  
  // Subscription response
  if (msg.id === 1) {
    console.log('Subscription ID:', msg.result);
  }
  
  // Block notifications
  if (msg.method === 'eth_subscription') {
    console.log('New block:', msg.params.result.number);
  }
};
```

### Subscribe to Logs with Filter

```javascript
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  id: 2,
  method: 'eth_subscribe',
  params: ['logs', {
    address: '0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48', // USDC
    topics: ['0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef'] // Transfer
  }]
}));
```

### Regular JSON-RPC Calls

```javascript
// eth_blockNumber
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  id: 3,
  method: 'eth_blockNumber',
  params: []
}));

// eth_getBalance
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  id: 4,
  method: 'eth_getBalance',
  params: ['0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045', 'latest']
}));

// eth_call
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  id: 5,
  method: 'eth_call',
  params: [{
    to: '0x...',
    data: '0x...'
  }, 'latest']
}));
```

### Unsubscribe

```javascript
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  id: 6,
  method: 'eth_unsubscribe',
  params: ['0x...'] // subscription ID
}));
```

---

## 🧪 Manual Testing

### Using wscat

```bash
npm install -g wscat
wscat -c ws://localhost:4000/main/evm/1

# Subscribe
> {"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}
< {"jsonrpc":"2.0","id":1,"result":"0x1234..."}

# Get block number
> {"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}
< {"jsonrpc":"2.0","id":2,"result":"0x..."}

# Unsubscribe
> {"jsonrpc":"2.0","id":3,"method":"eth_unsubscribe","params":["0x1234..."]}
< {"jsonrpc":"2.0","id":3,"result":true}
```

### Using Browser

Open `test_ws.html` in your browser for a visual test interface.

---

## ⚙️ Configuration

WebSocket is **enabled by default**. To customize:

```yaml
server:
  httpPort: 4000
  websocket:
    enabled: true
    maxConnectionsPerNetwork: 10000
    maxSubscriptionsPerConnection: 100
    pingInterval: 30s
    pongTimeout: 60s
    readBufferSize: 4096
    writeBufferSize: 4096
```

---

## 🔄 Polling Behavior

- **Poll Interval**: 2 seconds (configurable in Phase 4)
- **newHeads**: Polls `eth_getBlockByNumber("latest")` every 2s
- **logs**: Polls `eth_getLogs({fromBlock, toBlock})` every 2s
- **Notifications**: Only sent when new data is detected

---

## 📊 Supported Subscription Types

### newHeads
Get notified of new block headers.

**Parameters**: None

**Notification Format**:
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x...",
    "result": {
      "number": "0x...",
      "hash": "0x...",
      "parentHash": "0x...",
      "timestamp": "0x...",
      ...
    }
  }
}
```

### logs
Get notified of new logs matching a filter.

**Parameters**: Optional filter object
- `address`: string or array of addresses
- `topics`: array of topics (supports OR logic and wildcards)

**Notification Format**:
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x...",
    "result": {
      "address": "0x...",
      "topics": ["0x..."],
      "data": "0x...",
      "blockNumber": "0x...",
      "transactionHash": "0x...",
      ...
    }
  }
}
```

---

## 🐛 Troubleshooting

### WebSocket won't connect
```bash
# Check if eRPC is running
curl http://localhost:4000/healthcheck

# Check debug logs
./bin/erpc-server -config test-ws-config.yaml
```

### No notifications received
- Wait at least 2-4 seconds (polling interval)
- Check subscription was successful (got subscription ID)
- Check upstream RPC is responding
- Look for "polling for new head" in debug logs

### Too many logs
Use filters to narrow down:
```javascript
{
  address: "0xSpecificContract",
  topics: ["0xSpecificEventSignature"]
}
```

---

## 📚 More Information

- **Design Document**: `WS_DESIGN.md`
- **Testing Guide**: `WS_TESTING.md`
- **Implementation Status**: `WS_STATUS.md`

---

## 🎯 Next Steps (Phase 4)

- [ ] Add Prometheus metrics
- [ ] Dynamic poll interval based on blocktime
- [ ] Comprehensive documentation
- [ ] Integration tests
- [ ] Performance benchmarks

