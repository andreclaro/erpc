# WebSocket Subscriptions Testing Guide

## Current Status

✅ **Phase 1-3 Complete** - WebSocket subscriptions are fully implemented and ready for testing!

### Implemented Features
- ✅ WebSocket server (same port as HTTP)
- ✅ Connection management & lifecycle
- ✅ `eth_subscribe("newHeads")` - Get notified of new blocks
- ✅ `eth_subscribe("logs", filter)` - Get notified of new logs
- ✅ `eth_unsubscribe(subId)` - Cancel subscriptions
- ✅ Polling mechanism (2-second interval)
- ✅ Filter matching for logs (address + topics)
- ✅ Automatic cleanup on disconnect

### Not Yet Implemented (Phase 4)
- ⏳ Prometheus metrics
- ⏳ Dynamic poll interval based on blocktime
- ⏳ Comprehensive documentation

---

## Quick Test (Browser)

### Step 1: Start eRPC

```bash
# Build
make build

# Run with test config
./erpc --config test-ws-config.yaml
```

### Step 2: Open Test Page

Open `test_ws.html` in your browser (Chrome/Firefox recommended).

**URL format:** `ws://localhost:4000/{projectId}/evm/{chainId}`

Example:
- `ws://localhost:4000/main/evm/1` - Ethereum Mainnet
- `ws://localhost:4000/main/evm/137` - Polygon
- `ws://localhost:4000/main/evm/42161` - Arbitrum

### Step 3: Test Subscriptions

1. **Click "Connect"** - Should see "✅ WebSocket connected!"
2. **Click "Subscribe newHeads"** 
   - Should receive subscription ID: `{"result":"0x..."}`
   - Every ~2s: New block notifications
3. **Click "Unsubscribe"** - Stops notifications
4. **Click "Subscribe Logs"** - Get all new logs
5. **Click "Subscribe Logs (filtered)"** - Only USDT contract logs

---

## Command Line Test (wscat)

### Install wscat
```bash
npm install -g wscat
```

### Test newHeads Subscription

```bash
# Connect
wscat -c ws://localhost:4000/main/evm/1

# Subscribe
> {"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}

# You'll receive:
< {"jsonrpc":"2.0","id":1,"result":"0x1234..."}

# Then every ~2s:
< {"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0x1234...","result":{...}}}

# Unsubscribe
> {"jsonrpc":"2.0","id":2,"method":"eth_unsubscribe","params":["0x1234..."]}
```

### Test Logs Subscription

```bash
# Subscribe to all logs
> {"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs"]}

# Subscribe to specific contract
> {"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs",{"address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}

# Subscribe with topic filter
> {"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs",{"topics":["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]}]}
```

---

## JavaScript/Node.js Test

```javascript
const WebSocket = require('ws');

const ws = new WebSocket('ws://localhost:4000/main/evm/1');

ws.on('open', () => {
  console.log('Connected!');
  
  // Subscribe to newHeads
  ws.send(JSON.stringify({
    jsonrpc: '2.0',
    id: 1,
    method: 'eth_subscribe',
    params: ['newHeads']
  }));
});

ws.on('message', (data) => {
  const msg = JSON.parse(data);
  console.log('Received:', JSON.stringify(msg, null, 2));
});

ws.on('error', (error) => {
  console.error('Error:', error);
});
```

---

## Expected Behavior

### newHeads Subscription
- **Poll Interval:** Every 2 seconds
- **Method Called:** `eth_getBlockByNumber("latest", false)`
- **Notification:** Only when block number changes
- **Format:**
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

### Logs Subscription
- **Poll Interval:** Every 2 seconds
- **Method Called:** `eth_getLogs({fromBlock, toBlock, address?, topics?})`
- **Notification:** For each matching log
- **Filter Rules:**
  - `address`: string or array (OR logic)
  - `topics`: array of (string or array or null)
    - `null` = wildcard (matches any)
    - `string` = exact match
    - `array` = OR logic

---

## Troubleshooting

### WebSocket won't connect
```bash
# Check if eRPC is running
curl http://localhost:4000/healthcheck

# Check logs
LOG_LEVEL=debug ./erpc --config test-ws-config.yaml
```

### No notifications received
- **Check subscription was successful** - Did you get a subscription ID?
- **Wait 2-4 seconds** - Poller runs every 2 seconds
- **Check network** - Is the upstream RPC responding?
- **Check logs** - Look for "polling for new head" or "polling for logs"

### Logs subscription too noisy
Use filters to narrow down:
```javascript
{
  address: "0xSpecificContract",
  topics: ["0xSpecificEvent"]
}
```

### Connection drops
- Check `PingInterval` (default 30s) and `PongTimeout` (default 60s)
- Network/firewall issues?
- Check eRPC logs for errors

---

## What to Look For

### ✅ Good Signs
- WebSocket connects successfully
- Subscription returns ID like `0x1234abcd...`
- Notifications arrive every ~2 seconds
- Block numbers are incrementing
- Logs match your filter
- Unsubscribe returns `true`

### ❌ Problems
- Connection refused → eRPC not running
- "Method not found" → WebSocket not properly integrated
- No notifications → Poller not running or upstream issues
- Wrong logs → Filter matching bug
- Memory leaks → Subscriptions not cleaning up

---

## Next Steps

After testing confirms everything works:
1. ✅ Test basic connectivity
2. ✅ Test newHeads subscription
3. ✅ Test logs subscription with filters
4. ✅ Test unsubscribe
5. ✅ Test connection cleanup (disconnect while subscribed)
6. → **Move to Phase 4** (metrics, dynamic polling, docs)

