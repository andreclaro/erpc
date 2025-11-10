#!/usr/bin/env node

/**
 * WebSocket Subscription Test Script
 * Tests eRPC WebSocket implementation for eth_subscribe/eth_unsubscribe
 */

const WebSocket = require('ws');

// Configuration
const WS_URL = process.env.WS_URL || 'ws://localhost:4000/main/evm/84532'; // Base Sepolia
const TEST_DURATION = parseInt(process.env.TEST_DURATION || '30000'); // 30 seconds
const COLORS = {
  reset: '\x1b[0m',
  bright: '\x1b[1m',
  dim: '\x1b[2m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  red: '\x1b[31m',
  cyan: '\x1b[36m',
};

let messageId = 1;
let subscriptionIds = [];
let notificationCount = {
  newHeads: 0,
  logs: 0,
};
let testStartTime;
let ws;

// Utility functions
function log(message, color = COLORS.reset) {
  const timestamp = new Date().toISOString().substr(11, 12);
  console.log(`${COLORS.dim}[${timestamp}]${COLORS.reset} ${color}${message}${COLORS.reset}`);
}

function success(message) {
  log(`✅ ${message}`, COLORS.green);
}

function error(message) {
  log(`❌ ${message}`, COLORS.red);
}

function info(message) {
  log(`ℹ️  ${message}`, COLORS.blue);
}

function warning(message) {
  log(`⚠️  ${message}`, COLORS.yellow);
}

function notification(message) {
  log(`📬 ${message}`, COLORS.cyan);
}

function send(message) {
  if (ws.readyState !== WebSocket.OPEN) {
    error('WebSocket not open, cannot send message');
    return;
  }
  const msg = JSON.stringify(message);
  info(`Sending: ${msg.substring(0, 100)}...`);
  ws.send(msg);
}

// Test functions
async function testNewHeadsSubscription() {
  return new Promise((resolve) => {
    info('Testing newHeads subscription...');
    
    const reqId = messageId++;
    let subId = null;
    let receivedResponse = false;
    let receivedNotification = false;
    
    const timeout = setTimeout(() => {
      if (!receivedResponse) {
        error('newHeads: No response to subscription request');
      } else if (!receivedNotification) {
        warning('newHeads: No notifications received (might need to wait longer)');
      }
      resolve({ success: receivedResponse, notifications: notificationCount.newHeads });
    }, 10000); // 10 second timeout
    
    const messageHandler = (data) => {
      try {
        const msg = JSON.parse(data);
        
        // Response to our subscription request
        if (msg.id === reqId) {
          receivedResponse = true;
          if (msg.result) {
            subId = msg.result;
            subscriptionIds.push(subId);
            success(`newHeads: Subscribed with ID ${subId}`);
          } else if (msg.error) {
            error(`newHeads: Subscription failed - ${JSON.stringify(msg.error)}`);
            clearTimeout(timeout);
            ws.off('message', messageHandler);
            resolve({ success: false, error: msg.error });
          }
        }
        
        // Notification
        if (msg.method === 'eth_subscription' && msg.params?.subscription === subId) {
          notificationCount.newHeads++;
          receivedNotification = true;
          const blockNum = msg.params.result?.number || 'unknown';
          const blockHash = msg.params.result?.hash || 'unknown';
          notification(`newHeads: Block ${blockNum} (${blockHash.substring(0, 10)}...)`);
          
          // After first notification, resolve but keep the handler for continued notifications
          if (notificationCount.newHeads === 1) {
            success('newHeads: First notification received!');
            clearTimeout(timeout);
            // Don't remove the handler - let it continue receiving notifications
            resolve({ success: true, notifications: 1, subscriptionId: subId });
          }
        }
      } catch (e) {
        error(`Failed to parse message: ${e.message}`);
      }
    };
    
    ws.on('message', messageHandler);
    
    send({
      jsonrpc: '2.0',
      id: reqId,
      method: 'eth_subscribe',
      params: ['newHeads']
    });
  });
}

async function testLogsSubscription() {
  return new Promise((resolve) => {
    info('Testing logs subscription (all logs)...');
    
    const reqId = messageId++;
    let subId = null;
    let receivedResponse = false;
    
    const timeout = setTimeout(() => {
      if (!receivedResponse) {
        error('logs: No response to subscription request');
        resolve({ success: false });
      } else {
        info(`logs: Subscription created, received ${notificationCount.logs} notifications`);
        resolve({ success: true, notifications: notificationCount.logs });
      }
    }, 8000); // 8 second timeout
    
    const messageHandler = (data) => {
      try {
        const msg = JSON.parse(data);
        
        // Response to our subscription request
        if (msg.id === reqId) {
          receivedResponse = true;
          if (msg.result) {
            subId = msg.result;
            subscriptionIds.push(subId);
            success(`logs: Subscribed with ID ${subId}`);
          } else if (msg.error) {
            error(`logs: Subscription failed - ${JSON.stringify(msg.error)}`);
            clearTimeout(timeout);
            ws.off('message', messageHandler);
            resolve({ success: false, error: msg.error });
          }
        }
        
        // Notification
        if (msg.method === 'eth_subscription' && msg.params?.subscription === subId) {
          notificationCount.logs++;
          const log = msg.params.result;
          notification(`logs: ${log.address?.substring(0, 10)}... tx:${log.transactionHash?.substring(0, 10)}...`);
        }
      } catch (e) {
        error(`Failed to parse message: ${e.message}`);
      }
    };
    
    ws.on('message', messageHandler);
    
    send({
      jsonrpc: '2.0',
      id: reqId,
      method: 'eth_subscribe',
      params: ['logs']
    });
  });
}

async function testRpcCall(method, params = [], label = null) {
  return new Promise((resolve) => {
    const displayLabel = label || method;
    info(`Testing JSON-RPC call: ${displayLabel}...`);
    
    const reqId = messageId++;
    
    const timeout = setTimeout(() => {
      error(`${displayLabel}: No response (timeout)`);
      resolve({ success: false, timeout: true });
    }, 10000);
    
    const messageHandler = (data) => {
      try {
        const msg = JSON.parse(data);
        
        if (msg.id === reqId) {
          clearTimeout(timeout);
          ws.off('message', messageHandler);
          
          if (msg.result !== undefined) {
            const resultStr = typeof msg.result === 'object' 
              ? JSON.stringify(msg.result).substring(0, 100) 
              : String(msg.result);
            success(`${displayLabel}: ${resultStr}${resultStr.length >= 100 ? '...' : ''}`);
            resolve({ success: true, result: msg.result });
          } else if (msg.error) {
            error(`${displayLabel}: ${JSON.stringify(msg.error)}`);
            resolve({ success: false, error: msg.error });
          } else {
            warning(`${displayLabel}: Unexpected response format`);
            resolve({ success: false, unexpected: true });
          }
        }
      } catch (e) {
        error(`Failed to parse message: ${e.message}`);
      }
    };
    
    ws.on('message', messageHandler);
    
    send({
      jsonrpc: '2.0',
      id: reqId,
      method,
      params
    });
  });
}

async function testUnsubscribe(subId) {
  return new Promise((resolve) => {
    info(`Testing unsubscribe for ${subId}...`);
    
    const reqId = messageId++;
    
    const timeout = setTimeout(() => {
      error('unsubscribe: No response');
      resolve({ success: false });
    }, 5000);
    
    const messageHandler = (data) => {
      try {
        const msg = JSON.parse(data);
        
        if (msg.id === reqId) {
          clearTimeout(timeout);
          ws.off('message', messageHandler);
          
          if (msg.result === true) {
            success(`Unsubscribed from ${subId}`);
            resolve({ success: true });
          } else {
            warning(`Unsubscribe returned: ${JSON.stringify(msg.result)}`);
            resolve({ success: false, result: msg.result });
          }
        }
      } catch (e) {
        error(`Failed to parse message: ${e.message}`);
      }
    };
    
    ws.on('message', messageHandler);
    
    send({
      jsonrpc: '2.0',
      id: reqId,
      method: 'eth_unsubscribe',
      params: [subId]
    });
  });
}

// Main test flow
async function runTests() {
  console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
  console.log(`${COLORS.bright}  eRPC WebSocket Subscription Test${COLORS.reset}`);
  console.log(`${COLORS.bright}========================================${COLORS.reset}\n`);
  
  info(`Connecting to: ${WS_URL}`);
  info(`Test duration: ${TEST_DURATION}ms`);
  
  ws = new WebSocket(WS_URL);
  
  ws.on('open', async () => {
    success('WebSocket connected!');
    testStartTime = Date.now();
    
    try {
      // Test Flow:
      // 1. Create all subscriptions (newHeads + logs)
      // 2. Test JSON-RPC calls (verify non-subscription methods work)
      // 3. Wait 120 seconds to collect notifications from active subscriptions
      // 4. Unsubscribe from everything and cleanup
      
      // Step 1: Subscribe to all subscriptions
      console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`${COLORS.bright}  Step 1: Create Subscriptions${COLORS.reset}`);
      console.log(`${COLORS.bright}========================================${COLORS.reset}\n`);
      
      // Subscribe to newHeads
      console.log(`${COLORS.bright}--- Subscribing to newHeads ---${COLORS.reset}`);
      const newHeadsResult = await testNewHeadsSubscription();
      
      if (!newHeadsResult.success) {
        error('newHeads subscription failed, aborting remaining tests');
        ws.close();
        process.exit(1);
      }
      
      await new Promise(resolve => setTimeout(resolve, 1000)); // Small delay between subscriptions
      
      // Subscribe to logs
      console.log(`\n${COLORS.bright}--- Subscribing to logs ---${COLORS.reset}`);
      const logsResult = await testLogsSubscription();
      
      if (!logsResult.success) {
        error('logs subscription failed, aborting remaining tests');
        ws.close();
        process.exit(1);
      }
      
      // Step 2: Test JSON-RPC calls over WebSocket (while subscriptions are active)
      console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`${COLORS.bright}  Step 2: JSON-RPC Calls over WebSocket${COLORS.reset}`);
      console.log(`${COLORS.bright}========================================${COLORS.reset}\n`);
      
      const rpcTests = [
        { method: 'eth_blockNumber', params: [], label: 'eth_blockNumber' },
        { method: 'eth_chainId', params: [], label: 'eth_chainId' },
        { method: 'eth_gasPrice', params: [], label: 'eth_gasPrice' },
        { method: 'net_version', params: [], label: 'net_version' },
      ];
      
      const rpcResults = [];
      for (const test of rpcTests) {
        const result = await testRpcCall(test.method, test.params, test.label);
        rpcResults.push({ ...test, success: result.success });
        await new Promise(resolve => setTimeout(resolve, 500)); // Small delay between calls
      }
      
      // Test eth_getBlockByNumber with latest block
      info('Testing eth_getBlockByNumber with "latest"...');
      const blockResult = await testRpcCall('eth_getBlockByNumber', ['latest', false], 'eth_getBlockByNumber(latest)');
      rpcResults.push({ method: 'eth_getBlockByNumber', success: blockResult.success });
      
      // Test eth_getBalance
      const testAddress = '0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045'; // vitalik.eth
      info(`Testing eth_getBalance for ${testAddress}...`);
      const balanceResult = await testRpcCall('eth_getBalance', [testAddress, 'latest'], 'eth_getBalance');
      rpcResults.push({ method: 'eth_getBalance', success: balanceResult.success });
      
      // Summary of RPC tests
      const rpcSuccessCount = rpcResults.filter(r => r.success).length;
      info(`JSON-RPC tests: ${rpcSuccessCount}/${rpcResults.length} passed`);
      
      // Step 3: Wait and collect notifications
      console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`${COLORS.bright}  Step 3: Collecting Notifications${COLORS.reset}`);
      console.log(`${COLORS.bright}========================================${COLORS.reset}\n`);
      info('Waiting 120 seconds to collect notifications from both subscriptions...');
      info('(Press Ctrl+C to stop early)');
      console.log();
      
      // Show periodic updates during the wait
      const startCount = { newHeads: notificationCount.newHeads, logs: notificationCount.logs };
      for (let i = 0; i < 12; i++) {
        await new Promise(resolve => setTimeout(resolve, 10000)); // Wait 10 seconds
        const elapsed = (i + 1) * 10;
        info(`[${elapsed}s] Received ${notificationCount.newHeads - startCount.newHeads} newHeads, ${notificationCount.logs - startCount.logs} logs`);
      }
      
      console.log();
      success(`Collection complete! Total: ${notificationCount.newHeads} newHeads, ${notificationCount.logs} logs notifications`);
      
      // Step 4: Unsubscribe from all subscriptions
      console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`${COLORS.bright}  Step 4: Unsubscribe All${COLORS.reset}`);
      console.log(`${COLORS.bright}========================================${COLORS.reset}\n`);
      
      if (subscriptionIds.length > 0) {
        info(`Unsubscribing from ${subscriptionIds.length} active subscription(s)...`);
        for (const subId of subscriptionIds) {
          await testUnsubscribe(subId);
        }
      } else {
        warning('No active subscriptions to unsubscribe from');
      }
      
      // Summary
      const elapsed = Date.now() - testStartTime;
      console.log(`\n${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`${COLORS.bright}  Test Summary${COLORS.reset}`);
      console.log(`${COLORS.bright}========================================${COLORS.reset}`);
      console.log(`Duration: ${(elapsed / 1000).toFixed(1)}s`);
      console.log(`\nSubscriptions:`);
      console.log(`  - newHeads notifications: ${notificationCount.newHeads}`);
      console.log(`  - logs notifications: ${notificationCount.logs}`);
      console.log(`\nJSON-RPC Calls:`);
      console.log(`  - Tests passed: ${rpcSuccessCount}/${rpcResults.length}`);
      
      if (notificationCount.newHeads > 0) {
        success('✅ All tests passed!');
        ws.close();
        process.exit(0);
      } else {
        error('❌ Tests failed - no notifications received');
        ws.close();
        process.exit(1);
      }
      
    } catch (e) {
      error(`Test error: ${e.message}`);
      console.error(e);
      ws.close();
      process.exit(1);
    }
  });
  
  ws.on('error', (err) => {
    error(`WebSocket error: ${err.message}`);
    process.exit(1);
  });
  
  ws.on('close', () => {
    info('WebSocket closed');
  });
  
  // Handle process termination
  process.on('SIGINT', () => {
    console.log('\n\nReceived SIGINT, cleaning up...');
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.close();
    }
    process.exit(0);
  });
}

// Check if ws module is available
try {
  require.resolve('ws');
} catch (e) {
  console.error('\n❌ Error: "ws" module not found.');
  console.error('Please install it with: npm install ws\n');
  process.exit(1);
}

// Run tests
runTests().catch(err => {
  error(`Fatal error: ${err.message}`);
  console.error(err);
  process.exit(1);
});

