const WebSocket = require('./.test-ws/node_modules/ws');

console.log('🚀 Connecting to Base Sepolia WebSocket...');
const ws = new WebSocket('ws://localhost:4000/main/evm/84532');

ws.on('open', () => {
  console.log('✅ Connected! Subscribing to newHeads...');
  ws.send(JSON.stringify({
    jsonrpc: '2.0',
    id: 1,
    method: 'eth_subscribe',
    params: ['newHeads']
  }));
});

let blockCount = 0;
ws.on('message', (data) => {
  const msg = JSON.parse(data.toString());
  
  if (msg.id === 1 && msg.result) {
    console.log('✅ Subscribed with ID:', msg.result);
    return;
  }
  
  if (msg.method === 'eth_subscription') {
    blockCount++;
    console.log(`\n🎉 BLOCK #${blockCount} RECEIVED!`);
    console.log('   Block number:', msg.params.result.number);
    console.log('   Block hash:', msg.params.result.hash);
    console.log('   Timestamp:', new Date(parseInt(msg.params.result.timestamp, 16) * 1000).toISOString());
    
    if (blockCount >= 3) {
      console.log('\n✅ SUCCESS! Received 3 blocks, WebSocket subscriptions are working!');
      process.exit(0);
    }
  }
});

ws.on('error', (err) => {
  console.error('❌ Error:', err.message);
  process.exit(1);
});

ws.on('close', () => {
  console.log('Connection closed');
});

// Wait up to 30 seconds for blocks (Base Sepolia has ~2s block time)
setTimeout(() => {
  console.log(`\n⏱️  Timeout - received ${blockCount} blocks in 30 seconds`);
  if (blockCount > 0) {
    console.log('✅ WebSocket subscriptions ARE working!');
    process.exit(0);
  }
  process.exit(1);
}, 30000);
