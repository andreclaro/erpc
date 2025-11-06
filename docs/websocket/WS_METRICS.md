# WebSocket Metrics Documentation

This document describes the Prometheus metrics exposed by eRPC's WebSocket subscription implementation.

## Overview

All WebSocket metrics are prefixed with `erpc_websocket_` and include labels for filtering by project, network, and subscription type.

## Connection Metrics

### `erpc_websocket_connections_active` (Gauge)
Current number of active WebSocket connections.

**Labels:**
- `project`: Project ID
- `network`: Network ID

**Usage:**
Monitor active connection count per network to track usage and capacity.

```promql
# Active connections per network
erpc_websocket_connections_active{project="main", network="evm:84532"}

# Total active connections across all networks
sum(erpc_websocket_connections_active)
```

### `erpc_websocket_connections_total` (Counter)
Total number of WebSocket connections established since startup.

**Labels:**
- `project`: Project ID
- `network`: Network ID

**Usage:**
Track connection establishment rate and historical patterns.

```promql
# Connection rate per minute
rate(erpc_websocket_connections_total[1m])
```

### `erpc_websocket_connections_closed_total` (Counter)
Total number of WebSocket connections closed.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `reason`: Close reason (`normal`, `error`, `timeout`, etc.)

**Usage:**
Monitor connection stability and identify issues.

```promql
# Connections closed due to errors
erpc_websocket_connections_closed_total{reason="error"}

# Close rate by reason
rate(erpc_websocket_connections_closed_total[5m])
```

## Subscription Metrics

### `erpc_websocket_subscriptions_active` (Gauge)
Current number of active subscriptions.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type (`newHeads`, `logs`)

**Usage:**
Monitor active subscriptions per type.

```promql
# Active newHeads subscriptions
erpc_websocket_subscriptions_active{type="newHeads"}

# Total active subscriptions
sum(erpc_websocket_subscriptions_active)
```

### `erpc_websocket_subscriptions_created_total` (Counter)
Total number of subscriptions created.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type

**Usage:**
Track subscription creation patterns.

```promql
# Subscription creation rate
rate(erpc_websocket_subscriptions_created_total[1m])

# Most popular subscription types
topk(5, sum by(type) (rate(erpc_websocket_subscriptions_created_total[5m])))
```

### `erpc_websocket_subscriptions_removed_total` (Counter)
Total number of subscriptions removed.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type
- `reason`: Removal reason (`unsubscribe`, `connection_closed`, `error`)

**Usage:**
Monitor subscription lifecycle and churn.

```promql
# Subscriptions removed due to errors
erpc_websocket_subscriptions_removed_total{reason="error"}

# Subscription churn rate
rate(erpc_websocket_subscriptions_removed_total[5m])
```

## Notification Metrics

### `erpc_websocket_notifications_sent_total` (Counter)
Total number of subscription notifications successfully sent to clients.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type

**Usage:**
Track notification delivery throughput.

```promql
# Notifications per second
rate(erpc_websocket_notifications_sent_total[1m])

# newHeads notifications delivered
erpc_websocket_notifications_sent_total{type="newHeads"}
```

### `erpc_websocket_notification_errors_total` (Counter)
Total number of errors sending notifications to clients.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type
- `error`: Error type (`send_failed`, `subscriber_not_found`)

**Usage:**
Monitor notification delivery failures.

```promql
# Notification error rate
rate(erpc_websocket_notification_errors_total[1m])

# Error rate percentage
rate(erpc_websocket_notification_errors_total[5m]) / 
  (rate(erpc_websocket_notifications_sent_total[5m]) + 
   rate(erpc_websocket_notification_errors_total[5m])) * 100
```

## Poll Metrics

### `erpc_websocket_polls_total` (Counter)
Total number of upstream polls for subscriptions.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type
- `result`: Poll result (`new_block`, `no_change`, `empty`, `success`)

**Usage:**
Monitor polling frequency and effectiveness.

```promql
# Polls per minute
rate(erpc_websocket_polls_total[1m])

# Polls that found new data
erpc_websocket_polls_total{result="new_block"}

# Poll efficiency (new data / total polls)
rate(erpc_websocket_polls_total{result="new_block"}[5m]) / 
  rate(erpc_websocket_polls_total[5m]) * 100
```

### `erpc_websocket_poll_errors_total` (Counter)
Total number of errors during subscription polling.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type
- `error`: Error type (`fetch_failed`, `parse_failed`, `fetch_block_number_failed`)

**Usage:**
Monitor upstream connectivity and data issues.

```promql
# Poll error rate
rate(erpc_websocket_poll_errors_total[1m])

# Errors by type
sum by(error) (rate(erpc_websocket_poll_errors_total[5m]))
```

### `erpc_websocket_poll_duration_seconds` (Histogram)
Duration of upstream polls for subscriptions.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Subscription type

**Buckets:** `0.05, 0.5, 5, 30` seconds (configurable via histogram buckets)

**Usage:**
Monitor poll latency and upstream performance.

```promql
# Median poll duration
histogram_quantile(0.5, rate(erpc_websocket_poll_duration_seconds_bucket[5m]))

# 95th percentile poll duration
histogram_quantile(0.95, rate(erpc_websocket_poll_duration_seconds_bucket[5m]))

# Average poll duration
rate(erpc_websocket_poll_duration_seconds_sum[5m]) / 
  rate(erpc_websocket_poll_duration_seconds_count[5m])
```

## Message Metrics

### `erpc_websocket_messages_received_total` (Counter)
Total number of messages received from WebSocket clients.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `method`: JSON-RPC method (`eth_subscribe`, `eth_unsubscribe`, `eth_blockNumber`, etc.)

**Usage:**
Track client request patterns.

```promql
# Messages per second
rate(erpc_websocket_messages_received_total[1m])

# Most used methods
topk(10, sum by(method) (rate(erpc_websocket_messages_received_total[5m])))
```

### `erpc_websocket_messages_sent_total` (Counter)
Total number of messages sent to WebSocket clients.

**Labels:**
- `project`: Project ID
- `network`: Network ID
- `type`: Message type (`response`, `notification`, `error`)

**Usage:**
Track outbound message throughput.

```promql
# Outbound messages per second
rate(erpc_websocket_messages_sent_total[1m])

# Response vs notification ratio
sum(rate(erpc_websocket_messages_sent_total{type="response"}[5m])) /
  sum(rate(erpc_websocket_messages_sent_total{type="notification"}[5m]))
```

## Example Dashboards

### Connection Health Dashboard

```promql
# Active connections
erpc_websocket_connections_active

# Connection churn (connects vs disconnects)
rate(erpc_websocket_connections_total[5m]) -
rate(erpc_websocket_connections_closed_total[5m])

# Average connection lifetime
rate(erpc_websocket_connections_closed_total[1h]) /
rate(erpc_websocket_connections_total[1h])
```

### Subscription Performance Dashboard

```promql
# Notification throughput
sum(rate(erpc_websocket_notifications_sent_total[1m])) by (type)

# Notification latency (poll duration as proxy)
histogram_quantile(0.95, 
  sum(rate(erpc_websocket_poll_duration_seconds_bucket[5m])) by (le, type))

# Error rate
rate(erpc_websocket_notification_errors_total[5m]) /
  (rate(erpc_websocket_notifications_sent_total[5m]) +
   rate(erpc_websocket_notification_errors_total[5m]))
```

### Polling Efficiency Dashboard

```promql
# Poll frequency
rate(erpc_websocket_polls_total[1m])

# Useful polls percentage
rate(erpc_websocket_polls_total{result="new_block"}[5m]) /
  rate(erpc_websocket_polls_total[5m]) * 100

# Poll errors
rate(erpc_websocket_poll_errors_total[5m])
```

## Alerting Examples

### Connection Alerts

```yaml
# Too many active connections
- alert: WebSocketConnectionsHigh
  expr: erpc_websocket_connections_active > 5000
  for: 5m
  annotations:
    summary: "High number of WebSocket connections"

# High connection close rate
- alert: WebSocketHighCloseRate
  expr: rate(erpc_websocket_connections_closed_total[5m]) > 100
  for: 5m
  annotations:
    summary: "WebSocket connections closing rapidly"
```

### Notification Alerts

```yaml
# High notification error rate
- alert: WebSocketNotificationErrors
  expr: |
    rate(erpc_websocket_notification_errors_total[5m]) /
    (rate(erpc_websocket_notifications_sent_total[5m]) +
     rate(erpc_websocket_notification_errors_total[5m])) > 0.05
  for: 5m
  annotations:
    summary: "WebSocket notification error rate above 5%"
```

### Polling Alerts

```yaml
# High poll error rate
- alert: WebSocketPollErrors
  expr: rate(erpc_websocket_poll_errors_total[5m]) > 10
  for: 5m
  annotations:
    summary: "High rate of WebSocket polling errors"

# Slow polls
- alert: WebSocketSlowPolls
  expr: |
    histogram_quantile(0.95,
      rate(erpc_websocket_poll_duration_seconds_bucket[5m])) > 2
  for: 10m
  annotations:
    summary: "WebSocket polls taking longer than 2 seconds"
```

## Configuration

WebSocket metrics are automatically enabled when WebSocket support is enabled. No additional configuration is required.

To expose metrics for Prometheus scraping, configure the metrics endpoint in your eRPC configuration:

```yaml
server:
  httpPort: 4000
  webSocket:
    enabled: true
```

Then configure your Prometheus to scrape the `/metrics` endpoint (if exposed by your setup).

## Best Practices

1. **Monitor connection health**: Track `erpc_websocket_connections_active` and close rates
2. **Watch notification delivery**: Alert on high error rates
3. **Track poll efficiency**: Monitor how often polls find new data
4. **Set up latency alerts**: Alert on high poll duration (p95 > 5s)
5. **Capacity planning**: Use active connections and subscriptions for scaling decisions
6. **Debug with labels**: Use project/network/type labels to isolate issues

## Related Documentation

- [WebSocket Design](WS_DESIGN.md) - Architecture and design decisions
- [WebSocket Testing](WS_TESTING.md) - Testing guide
- [Quick Start](QUICKSTART_WS.md) - Getting started with WebSocket support

