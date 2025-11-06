# WebSocket Quick Start

## Status: Production Ready ✅

## Start Server
```bash
./bin/erpc-server -config test-ws-config.yaml
```

## Run Tests
```bash
./test-websocket.sh
```

## Working Features
- ✅ eth_subscribe("newHeads") 
- ✅ eth_subscribe("logs")
- ✅ eth_unsubscribe
- ✅ All JSON-RPC methods over WebSocket

## Test Results
- 18 newHeads notifications in 35s
- 6/6 JSON-RPC tests passed
- Zero errors

See `WS_DESIGN.md` and `README_WEBSOCKET.md` for details.

