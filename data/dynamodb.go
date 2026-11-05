package data

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	DynamoDBDriverName = "dynamodb"
)

var _ Connector = (*DynamoDBConnector)(nil)

type DynamoDBConnector struct {
	id                string
	logger            *zerolog.Logger
	initializer       *util.Initializer
	writeClient       *dynamodb.Client
	readClient        *dynamodb.Client
	table             string
	ttlAttributeName  string
	partitionKeyName  string
	rangeKeyName      string
	reverseIndexName  string
	initTimeout       time.Duration
	getTimeout        time.Duration
	setTimeout        time.Duration
	statePollInterval time.Duration
	lockRetryInterval time.Duration
}

var _ DistributedLock = &dynamoLock{}

type dynamoLock struct {
	connector *DynamoDBConnector
	lockKey   string
}

func (l *dynamoLock) IsNil() bool {
	return l == nil || l.connector == nil
}

var sharedReadClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         util.DefaultOutboundDialer().DialContext,
		MaxIdleConns:        2048,
		MaxIdleConnsPerHost: 2048,
		MaxConnsPerHost:     0, // unlimited, let MaxIdle… guard memory
		IdleConnTimeout:     120 * time.Second,
		// The transport negotiates HTTP/2 automatically for https endpoints,
		// so no manual http2.ConfigureTransport (v1 needed it; v2 does not).
	},
}
var sharedWriteClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         util.DefaultOutboundDialer().DialContext,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
		MaxConnsPerHost:     0, // unlimited, let MaxIdle… guard memory
		IdleConnTimeout:     120 * time.Second,
	},
}

func NewDynamoDBConnector(
	ctx context.Context,
	logger *zerolog.Logger,
	id string,
	cfg *common.DynamoDBConnectorConfig,
) (*DynamoDBConnector, error) {
	lg := logger.With().Str("connector", id).Logger()
	lg.Debug().Interface("config", cfg).Msg("creating dynamodb connector")

	connector := &DynamoDBConnector{
		id:                id,
		logger:            &lg,
		table:             cfg.Table,
		partitionKeyName:  cfg.PartitionKeyName,
		rangeKeyName:      cfg.RangeKeyName,
		reverseIndexName:  cfg.ReverseIndexName,
		ttlAttributeName:  cfg.TTLAttributeName,
		initTimeout:       cfg.InitTimeout.Duration(),
		getTimeout:        cfg.GetTimeout.Duration(),
		setTimeout:        cfg.SetTimeout.Duration(),
		statePollInterval: cfg.StatePollInterval.Duration(),
		lockRetryInterval: cfg.LockRetryInterval.Duration(),
	}

	// create an Initializer to handle (re)connecting
	connector.initializer = util.NewInitializer(ctx, &lg, nil)

	connectTask := util.NewBootstrapTask(fmt.Sprintf("dynamodb-connect/%s", id), func(ctx context.Context) error {
		return connector.connectTask(ctx, cfg)
	})

	if err := connector.initializer.ExecuteTasks(ctx, connectTask); err != nil {
		lg.Error().Err(err).Msg("failed to initialize DynamoDB on first attempt (will retry in background)")
		// Return the connector so the app can proceed, but note that it's not ready yet.
		return connector, nil
	}

	return connector, nil
}

func (d *DynamoDBConnector) connectTask(ctx context.Context, cfg *common.DynamoDBConnectorConfig) error {
	if cfg == nil || cfg.Region == "" {
		return common.NewTaskFatal(fmt.Errorf("missing region for store.dynamodb"))
	}

	awsCfg, err := createAWSConfig(ctx, cfg.Auth, cfg.Region)
	if err != nil {
		return common.NewTaskFatal(err)
	}

	clientOpts := func(httpClient *http.Client) func(*dynamodb.Options) {
		return func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.HTTPClient = httpClient
			// v1 applied MaxRetries verbatim (0 = no retries). v2's MaxAttempts
			// is the TOTAL attempt count, and 0 means "unlimited" — map
			// maxRetries+1 so 0 retries becomes exactly one attempt.
			o.Retryer = retry.AddWithMaxAttempts(retry.NewStandard(), max(cfg.MaxRetries+1, 1))
		}
	}

	d.writeClient = dynamodb.NewFromConfig(awsCfg, clientOpts(sharedWriteClient))
	d.readClient = dynamodb.NewFromConfig(awsCfg, clientOpts(sharedReadClient))

	if cfg.Table == "" {
		return common.NewTaskFatal(fmt.Errorf("missing table name for dynamodb connector"))
	}

	err = createTableIfNotExists(ctx, d.logger, d.writeClient, cfg)
	if err != nil {
		return err
	}

	err = ensureGlobalSecondaryIndexes(ctx, d.logger, d.writeClient, cfg)
	if err != nil {
		return err
	}

	return nil
}

func createTableIfNotExists(
	ctx context.Context,
	logger *zerolog.Logger,
	client *dynamodb.Client,
	cfg *common.DynamoDBConnectorConfig,
) error {
	logger.Debug().Msgf("creating dynamodb table '%s' if not exists with partition key '%s' and range key %s", cfg.Table, cfg.PartitionKeyName, cfg.RangeKeyName)
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(cfg.Table),
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String(cfg.PartitionKeyName),
				KeyType:       types.KeyTypeHash,
			},
			{
				AttributeName: aws.String(cfg.RangeKeyName),
				KeyType:       types.KeyTypeRange,
			},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String(cfg.PartitionKeyName),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String(cfg.RangeKeyName),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		BillingMode: types.BillingModePayPerRequest,
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			{
				IndexName: aws.String(cfg.ReverseIndexName),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String(cfg.RangeKeyName),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String(cfg.PartitionKeyName),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
		},
	})

	var resourceInUse *types.ResourceInUseException
	if err != nil && !errors.As(err, &resourceInUse) {
		logger.Error().Err(err).Msgf("failed to create dynamodb table %s", cfg.Table)
		return err
	}

	if err := dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(cfg.Table),
	}, 5*time.Minute); err != nil {
		logger.Error().Err(err).Msgf("failed to wait for dynamodb table %s to be created", cfg.Table)
		return err
	}

	_, err = client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(cfg.Table),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String(cfg.TTLAttributeName),
			Enabled:       aws.Bool(true),
		},
	})

	if err != nil && !strings.Contains(err.Error(), "already enabled") {
		logger.Error().Err(err).Msg("failed to enable TTL on table")
		return err
	}

	logger.Debug().Msgf("dynamodb table '%s' is ready", cfg.Table)

	return nil
}

func ensureGlobalSecondaryIndexes(
	ctx context.Context,
	logger *zerolog.Logger,
	client *dynamodb.Client,
	cfg *common.DynamoDBConnectorConfig,
) error {
	if cfg.ReverseIndexName == "" {
		return nil
	}

	logger.Debug().Msgf("ensuring global secondary index '%s' for table '%s'", cfg.ReverseIndexName, cfg.Table)

	currentTable, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(cfg.Table),
	})

	if err != nil {
		return err
	}

	for _, gsi := range currentTable.Table.GlobalSecondaryIndexes {
		if aws.ToString(gsi.IndexName) == cfg.ReverseIndexName {
			logger.Debug().Msgf("global secondary index '%s' already exists", cfg.ReverseIndexName)
			return nil
		}
	}

	_, err = client.UpdateTable(ctx, &dynamodb.UpdateTableInput{
		TableName: aws.String(cfg.Table),
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String(cfg.PartitionKeyName),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String(cfg.RangeKeyName),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		GlobalSecondaryIndexUpdates: []types.GlobalSecondaryIndexUpdate{
			{
				Create: &types.CreateGlobalSecondaryIndexAction{
					IndexName: aws.String(cfg.ReverseIndexName),
					KeySchema: []types.KeySchemaElement{
						{
							AttributeName: aws.String(cfg.RangeKeyName),
							KeyType:       types.KeyTypeHash,
						},
						{
							AttributeName: aws.String(cfg.PartitionKeyName),
							KeyType:       types.KeyTypeRange,
						},
					},
					Projection: &types.Projection{
						ProjectionType: types.ProjectionTypeAll,
					},
					// TODO Add capacity unit configuration based on the table's capacity type
				},
			},
		},
	})

	if err != nil {
		logger.Error().Err(err).Msgf("failed to create global secondary index '%s' for table %s", cfg.ReverseIndexName, cfg.Table)
	} else {
		logger.Debug().Msgf("global secondary index '%s' created for table %s", cfg.ReverseIndexName, cfg.Table)
	}

	return err
}

func (d *DynamoDBConnector) Id() string {
	return d.id
}

func (d *DynamoDBConnector) Set(ctx context.Context, partitionKey, rangeKey string, value []byte, ttl *time.Duration) error {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.Set")
	defer span.End()

	if common.IsTracingDetailed {
		span.SetAttributes(
			attribute.String("partition_key", partitionKey),
			attribute.String("range_key", rangeKey),
			attribute.Int("value_size", len(value)),
		)
	}

	if d.writeClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return err
	}

	if len(value) > 1024 {
		d.logger.Debug().Int("len", len(value)).Str("partitionKey", partitionKey).Str("rangeKey", rangeKey).Interface("ttl", ttl).Msg("putting item in dynamodb")
	} else {
		d.logger.Debug().Int("len", len(value)).Str("partitionKey", partitionKey).Str("rangeKey", rangeKey).Interface("ttl", ttl).Msg("putting item in dynamodb")
	}

	item := map[string]types.AttributeValue{
		d.partitionKeyName: &types.AttributeValueMemberS{
			Value: partitionKey,
		},
		d.rangeKeyName: &types.AttributeValueMemberS{
			Value: rangeKey,
		},
		"value": &types.AttributeValueMemberB{
			Value: value, // Using Binary attribute type
		},
	}

	ctx, cancel := context.WithTimeout(ctx, d.setTimeout)
	defer cancel()

	// Add TTL if provided
	if ttl != nil && *ttl > 0 {
		expirationTime := time.Now().Add(*ttl).Unix()
		item[d.ttlAttributeName] = &types.AttributeValueMemberN{
			Value: fmt.Sprintf("%d", expirationTime),
		}
	}

	_, err := d.writeClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.table),
		Item:      item,
	})

	if err != nil {
		common.SetTraceSpanError(span, err)
	}

	return err
}

func (d *DynamoDBConnector) Get(ctx context.Context, index, partitionKey, rangeKey string, _ interface{}) ([]byte, error) {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.Get")
	defer span.End()

	if common.IsTracingDetailed {
		span.SetAttributes(
			attribute.String("index", index),
			attribute.String("partition_key", partitionKey),
			attribute.String("range_key", rangeKey),
		)
	}

	if d.readClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return nil, err
	}

	var value []byte

	if index == ConnectorReverseIndex {
		if rangeKey == "" || strings.HasSuffix(rangeKey, "*") {
			err := fmt.Errorf("when using reverse index rangeKey must be a non-empty string and not contain wildcards (rangeKey: '%s', partitionKey: '%s')", rangeKey, partitionKey)
			common.SetTraceSpanError(span, err)
			return nil, err
		}
		var keyCondition string = "#pkey = :pkey"
		var exprAttrNames map[string]string = map[string]string{
			"#pkey": d.rangeKeyName,
		}
		var exprAttrValues map[string]types.AttributeValue = map[string]types.AttributeValue{
			":pkey": &types.AttributeValueMemberS{
				Value: rangeKey,
			},
		}

		// In the reverse index, the rangeKey is the HASH key and partitionKey is the RANGE key
		if partitionKey != "" && partitionKey != "*" {
			if strings.HasSuffix(partitionKey, "*") {
				keyCondition += " AND begins_with(#rkey, :rkey)"
			} else {
				keyCondition += " AND #rkey = :rkey"
			}
			exprAttrNames["#rkey"] = d.partitionKeyName
			exprAttrValues[":rkey"] = &types.AttributeValueMemberS{
				Value: strings.TrimSuffix(partitionKey, "*"),
			}
		}

		// Build a query that returns newest first, requesting value and ttl.
		// We fetch up to 10 items and will pick the first non-expired one.
		now := time.Now().Unix()
		qi := &dynamodb.QueryInput{
			TableName:                 aws.String(d.table),
			IndexName:                 aws.String(d.reverseIndexName),
			KeyConditionExpression:    aws.String(keyCondition),
			ExpressionAttributeNames:  exprAttrNames,
			ExpressionAttributeValues: exprAttrValues,
			ScanIndexForward:          aws.Bool(false), // newest first
			Limit:                     aws.Int32(10),   // examine a handful to avoid pagination
			ProjectionExpression:      aws.String("#val, #ttl"),
			Select:                    types.SelectSpecificAttributes,
			FilterExpression:          aws.String("attribute_not_exists(#ttl) OR #ttl = :zero OR #ttl > :now"),
		}
		// Add aliases required by Projection/Filter
		qi.ExpressionAttributeNames["#val"] = "value"
		qi.ExpressionAttributeNames["#ttl"] = d.ttlAttributeName
		qi.ExpressionAttributeValues[":now"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)}
		qi.ExpressionAttributeValues[":zero"] = &types.AttributeValueMemberN{Value: "0"}

		ctx, cancel := context.WithTimeout(ctx, d.getTimeout)
		defer cancel()

		d.logger.Debug().Str("index", d.reverseIndexName).Str("partitionKey", partitionKey).Str("rangeKey", rangeKey).Msg("getting item from dynamodb")
		// v1 applied request.WithResponseReadTimeout(getTimeout) here; v2 has
		// no response-read-timeout option, so the deadline on this Query call
		// is the equivalent bound.
		result, err := d.readClient.Query(ctx, qi)
		if err != nil {
			common.SetTraceSpanError(span, err)
			return nil, err
		}
		// Find the first non-expired item from the page (FilterExpression already helps)
		var chosen map[string]types.AttributeValue
		for _, it := range result.Items {
			// Extra safeguard: client-side TTL validation
			if ttlAttr, exists := it[d.ttlAttributeName]; exists {
				if ttl, ok := ttlAttr.(*types.AttributeValueMemberN); ok && ttl.Value != "" && ttl.Value != "0" {
					expirationTime, perr := strconv.ParseInt(ttl.Value, 10, 64)
					if perr == nil && now > expirationTime {
						continue // skip expired
					}
				}
			}
			chosen = it
			break
		}

		if chosen == nil {
			err := common.NewErrRecordNotFound(partitionKey, rangeKey, DynamoDBDriverName)
			common.SetTraceSpanError(span, err)
			return nil, err
		}

		// Backward compatibility: check both B and S attributes
		if b, ok := chosen["value"].(*types.AttributeValueMemberB); ok {
			value = b.Value
		} else if s, ok := chosen["value"].(*types.AttributeValueMemberS); ok {
			// Legacy string value - treat as final decompressed value
			value = []byte(s.Value)
		} else {
			return nil, fmt.Errorf("value attribute is neither binary nor string")
		}
	} else {
		ky := map[string]types.AttributeValue{
			d.partitionKeyName: &types.AttributeValueMemberS{
				Value: partitionKey,
			},
			d.rangeKeyName: &types.AttributeValueMemberS{
				Value: rangeKey,
			},
		}
		d.logger.Debug().Str("index", "n/a").Str("partitionKey", partitionKey).Str("rangeKey", rangeKey).Msg("getting item from dynamodb")
		result, err := d.readClient.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(d.table),
			Key:       ky,
		})

		if err != nil {
			common.SetTraceSpanError(span, err)
			return nil, err
		}

		if result.Item == nil {
			err := common.NewErrRecordNotFound(partitionKey, rangeKey, DynamoDBDriverName)
			common.SetTraceSpanError(span, err)
			return nil, err
		}

		// Check if the item has expired
		if ttlAttr, exists := result.Item[d.ttlAttributeName]; exists {
			if ttl, ok := ttlAttr.(*types.AttributeValueMemberN); ok && ttl.Value != "" && ttl.Value != "0" {
				expirationTime, err := strconv.ParseInt(ttl.Value, 10, 64)
				now := time.Now().Unix()
				if err == nil && now > expirationTime {
					err := common.NewErrRecordExpired(partitionKey, rangeKey, DynamoDBDriverName, now, expirationTime)
					common.SetTraceSpanError(span, err)
					return nil, err
				}
			}
		}

		// Backward compatibility: check both B and S attributes
		if b, ok := result.Item["value"].(*types.AttributeValueMemberB); ok {
			value = b.Value
		} else if s, ok := result.Item["value"].(*types.AttributeValueMemberS); ok {
			// Legacy string value - treat as final decompressed value
			value = []byte(s.Value)
		} else {
			return nil, fmt.Errorf("value attribute is neither binary nor string")
		}
	}

	if common.IsTracingDetailed {
		span.SetAttributes(
			attribute.Int("value_size", len(value)),
		)
	}

	return value, nil
}

func (d *DynamoDBConnector) Lock(ctx context.Context, key string, ttl time.Duration) (DistributedLock, error) {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.Lock",
		trace.WithAttributes(
			attribute.String("lock_key", key),
			attribute.Int64("ttl_ms", ttl.Milliseconds()),
		),
	)
	defer span.End()

	if d.writeClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return nil, err
	}

	lockKey := fmt.Sprintf("%s:lock", key)

	// The 'ttl' parameter defines how long the lock will be held if acquired.
	// The overall acquisition timeout is governed by the parent 'ctx'.

	for {
		// Check if the parent context is already done (e.g., request cancelled or timed out)
		// This check is done at the beginning of each attempt and before waiting for retry.
		select {
		case <-ctx.Done():
			err := fmt.Errorf("lock acquisition cancelled or timed out for key '%s': %w", key, ctx.Err())
			common.SetTraceSpanError(span, err)
			return nil, err
		default:
			// Continue with lock acquisition attempt
		}

		// Calculate expiry time for the lock item itself IF it's acquired in this attempt.
		// This 'ttl' is the duration the lock will be held.
		lockItemExpiryTime := time.Now().Add(ttl).Unix()

		// Context for the individual PutItem attempt.
		// This is bounded by the connector's configured SetTimeout and the parent context 'ctx'.
		// If 'ctx' finishes, PutItemWithContext will be interrupted.
		putAttemptCtx, putAttemptCancel := context.WithTimeout(ctx, d.setTimeout)

		_, err := d.writeClient.PutItem(putAttemptCtx, &dynamodb.PutItemInput{
			TableName: aws.String(d.table),
			Item: map[string]types.AttributeValue{
				d.partitionKeyName: &types.AttributeValueMemberS{Value: lockKey},
				d.rangeKeyName:     &types.AttributeValueMemberS{Value: "lock"}, // Using a fixed value for the range key of lock items
				"expiry":           &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", lockItemExpiryTime)},
			},
			ConditionExpression: aws.String(
				"attribute_not_exists(#pk) OR #expiry < :now",
			),
			ExpressionAttributeNames: map[string]string{
				"#pk":     d.partitionKeyName,
				"#expiry": "expiry",
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":now": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Unix())},
			},
		})
		putAttemptCancel() // Release resources for this attempt's context immediately

		if err == nil {
			// Lock acquired successfully
			d.logger.Debug().Str("lockKey", lockKey).Dur("ttl", ttl).Msg("distributed lock acquired")
			return &dynamoLock{
				connector: d,
				lockKey:   lockKey,
			}, nil
		}

		// If an error occurred, check if it's a known AWS error we should retry on
		retryableError := false
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "ConditionalCheckFailedException":
				d.logger.Debug().Str("lockKey", lockKey).Dur("retryInterval", d.lockRetryInterval).
					Msg("lock currently held or contention, will retry")
				retryableError = true
			case "ProvisionedThroughputExceededException",
				"InternalServerError",    // Often retryable for DynamoDB
				"LimitExceededException": // Covers various limits, potentially including some throttling
				d.logger.Warn().Err(err).Str("lockKey", lockKey).Str("errorCode", apiErr.ErrorCode()).
					Dur("retryInterval", d.lockRetryInterval).
					Msg("DynamoDB capacity/limit/server error during lock acquisition, will retry")
				retryableError = true
			default:
				// Non-retryable AWS error for lock acquisition logic
				d.logger.Error().Err(err).Str("lockKey", lockKey).Str("errorCode", apiErr.ErrorCode()).
					Msg("non-retryable AWS error during lock acquisition attempt")
			}
		}

		if retryableError {
			// Wait for d.lockRetryInterval before retrying, but also respect parent context cancellation.
			select {
			case <-time.After(d.lockRetryInterval):
				// Continue to the next iteration of the loop to retry
			case <-ctx.Done(): // Parent context was cancelled/timed out while waiting
				wrappedErr := fmt.Errorf("lock acquisition timed out while waiting to retry for key '%s': %w", key, ctx.Err())
				common.SetTraceSpanError(span, wrappedErr)
				return nil, wrappedErr
			}
		} else {
			// An unexpected or non-retryable error occurred during the PutItem attempt
			wrappedErr := fmt.Errorf("failed to acquire lock for key '%s' during attempt: %w", key, err)
			common.SetTraceSpanError(span, wrappedErr)
			return nil, wrappedErr
		}
		// If a retryableError occurred and context wasn't done, the loop continues.
	}
}

func (l *dynamoLock) Unlock(ctx context.Context) error {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.Unlock",
		trace.WithAttributes(
			attribute.String("lock_key", l.lockKey),
		),
	)
	defer span.End()

	_, err := l.connector.writeClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(l.connector.table),
		Key: map[string]types.AttributeValue{
			l.connector.partitionKeyName: &types.AttributeValueMemberS{Value: l.lockKey},
			l.connector.rangeKeyName:     &types.AttributeValueMemberS{Value: "lock"},
		},
	})

	if err != nil {
		l.connector.logger.Warn().Err(err).Str("lockKey", l.lockKey).Msg("failed to release distributed lock")
	} else {
		l.connector.logger.Debug().Str("lockKey", l.lockKey).Msg("distributed lock released")
	}

	return err
}

func (d *DynamoDBConnector) WatchCounterInt64(ctx context.Context, key string) (<-chan CounterInt64State, func(), error) {
	if d.readClient == nil {
		return nil, nil, fmt.Errorf("DynamoDB client not initialized yet")
	}

	updates := make(chan CounterInt64State, 1)

	// Start polling goroutine
	ticker := time.NewTicker(d.statePollInterval)
	done := make(chan struct{})

	go func() {
		defer ticker.Stop()
		var lastUpdatedAt int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				st, ok, err := d.getSimpleValue(ctx, key)
				if err != nil {
					d.logger.Warn().Err(err).Str("key", key).Msg("failed to poll counter value")
					continue
				}
				if ok && st.UpdatedAt > lastUpdatedAt {
					lastUpdatedAt = st.UpdatedAt
					select {
					case updates <- st:
					default:
					}
				}
			}
		}
	}()

	// Get initial value
	if st, ok, err := d.getSimpleValue(ctx, key); err == nil && ok {
		updates <- st
	}

	cleanup := func() {
		close(done)
		close(updates)
	}

	return updates, cleanup, nil
}

func (d *DynamoDBConnector) getSimpleValue(ctx context.Context, key string) (CounterInt64State, bool, error) {
	ctx, span := common.StartDetailSpan(ctx, "DynamoDBConnector.getSimpleValue",
		trace.WithAttributes(
			attribute.String("key", key),
		),
	)
	defer span.End()

	result, err := d.readClient.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.table),
		Key: map[string]types.AttributeValue{
			d.partitionKeyName: &types.AttributeValueMemberS{Value: key},
			d.rangeKeyName:     &types.AttributeValueMemberS{Value: "value"},
		},
		ConsistentRead: aws.Bool(true),
	})

	if err != nil {
		return CounterInt64State{}, false, err
	}

	if result.Item == nil {
		return CounterInt64State{}, false, nil
	}

	// Check if value attribute exists
	v, exists := result.Item["value"]
	if !exists {
		d.logger.Debug().Str("key", key).Msg("counter value attribute not found in DynamoDB item")
		return CounterInt64State{}, false, nil
	}

	// Extract raw bytes (we store JSON in Binary; other types are treated as best-effort strings)
	var raw []byte
	switch tv := v.(type) {
	case *types.AttributeValueMemberB:
		raw = tv.Value
	case *types.AttributeValueMemberS:
		raw = []byte(tv.Value)
	case *types.AttributeValueMemberN:
		raw = []byte(tv.Value)
	default:
		return CounterInt64State{}, false, nil
	}

	var st CounterInt64State
	if err := common.SonicCfg.Unmarshal(raw, &st); err != nil || st.UpdatedAt <= 0 {
		// No backward compatibility: treat parse errors as missing
		return CounterInt64State{}, false, nil
	}

	return st, true, nil
}

func (d *DynamoDBConnector) PublishCounterInt64(ctx context.Context, key string, value CounterInt64State) error {
	// DynamoDB connector doesn't have a push-based pub/sub mechanism.
	// Shared counters are propagated via polling WatchCounterInt64, and the authoritative state
	// is stored via Set(). Publish is therefore a best-effort no-op.
	return nil
}

func (d *DynamoDBConnector) Delete(ctx context.Context, partitionKey, rangeKey string) error {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.Delete")
	defer span.End()

	if common.IsTracingDetailed {
		span.SetAttributes(
			attribute.String("partition_key", partitionKey),
			attribute.String("range_key", rangeKey),
		)
	}

	if d.writeClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return err
	}

	d.logger.Debug().Str("partitionKey", partitionKey).Str("rangeKey", rangeKey).Msg("deleting item from dynamodb")

	ctx, cancel := context.WithTimeout(ctx, d.setTimeout)
	defer cancel()

	_, err := d.writeClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.table),
		Key: map[string]types.AttributeValue{
			d.partitionKeyName: &types.AttributeValueMemberS{
				Value: partitionKey,
			},
			d.rangeKeyName: &types.AttributeValueMemberS{
				Value: rangeKey,
			},
		},
	})

	if err != nil {
		common.SetTraceSpanError(span, err)
	}

	return err
}

func (d *DynamoDBConnector) List(ctx context.Context, index string, limit int, paginationToken string) ([]KeyValuePair, string, error) {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.List")
	defer span.End()

	if common.IsTracingDetailed {
		span.SetAttributes(
			attribute.String("index", index),
			attribute.Int("limit", limit),
		)
	}

	if d.readClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return nil, "", err
	}

	ctx, cancel := context.WithTimeout(ctx, d.getTimeout)
	defer cancel()

	// Parse pagination token. Tokens are produced and consumed within one
	// connector only (never persisted), so the JSON encoding is free to
	// differ from the v1 SDK's AttributeValue JSON shape.
	var exclusiveStartKey map[string]string
	if paginationToken != "" {
		decoded, err := base64.StdEncoding.DecodeString(paginationToken)
		if err != nil {
			return nil, "", fmt.Errorf("invalid pagination token: %w", err)
		}
		err = json.Unmarshal(decoded, &exclusiveStartKey)
		if err != nil {
			return nil, "", fmt.Errorf("invalid pagination token format: %w", err)
		}
	}
	exclusiveStartKeyAttr := make(map[string]types.AttributeValue, len(exclusiveStartKey))
	for k, v := range exclusiveStartKey {
		exclusiveStartKeyAttr[k] = &types.AttributeValueMemberS{Value: v}
	}

	var result *dynamodb.ScanOutput
	var err error

	if index == ConnectorReverseIndex {
		// Use the reverse index (GSI)
		result, err = d.readClient.Scan(ctx, &dynamodb.ScanInput{
			TableName:         aws.String(d.table),
			IndexName:         aws.String(d.reverseIndexName),
			Limit:             aws.Int32(int32(limit)),
			ExclusiveStartKey: exclusiveStartKeyAttr,
		})
	} else {
		// Use the main table
		result, err = d.readClient.Scan(ctx, &dynamodb.ScanInput{
			TableName:         aws.String(d.table),
			Limit:             aws.Int32(int32(limit)),
			ExclusiveStartKey: exclusiveStartKeyAttr,
		})
	}

	if err != nil {
		common.SetTraceSpanError(span, err)
		return nil, "", err
	}

	results := make([]KeyValuePair, 0, len(result.Items))
	now := time.Now().Unix()

	for _, item := range result.Items {
		// Check if item has expired
		if ttlAttr, exists := item[d.ttlAttributeName]; exists {
			if ttl, ok := ttlAttr.(*types.AttributeValueMemberN); ok && ttl.Value != "" && ttl.Value != "0" {
				expirationTime, err := strconv.ParseInt(ttl.Value, 10, 64)
				if err == nil && now > expirationTime {
					continue // Skip expired items
				}
			}
		}

		// Extract partition and range keys
		partitionKey := ""
		rangeKey := ""
		if pk, ok := item[d.partitionKeyName].(*types.AttributeValueMemberS); ok {
			partitionKey = pk.Value
		}
		if rk, ok := item[d.rangeKeyName].(*types.AttributeValueMemberS); ok {
			rangeKey = rk.Value
		}

		// Extract value
		var value []byte
		if b, ok := item["value"].(*types.AttributeValueMemberB); ok {
			value = b.Value
		} else if s, ok := item["value"].(*types.AttributeValueMemberS); ok {
			value = []byte(s.Value)
		}

		if partitionKey != "" && rangeKey != "" {
			results = append(results, KeyValuePair{
				PartitionKey: partitionKey,
				RangeKey:     rangeKey,
				Value:        value,
			})
		}
	}

	// Prepare next token
	nextToken := ""
	if len(result.LastEvaluatedKey) > 0 {
		tokenData := make(map[string]string, len(result.LastEvaluatedKey))
		for k, v := range result.LastEvaluatedKey {
			sv, ok := v.(*types.AttributeValueMemberS)
			if !ok {
				return nil, "", fmt.Errorf("failed to create pagination token: last-evaluated key %q is not a string attribute", k)
			}
			tokenData[k] = sv.Value
		}
		tokenJSON, err := json.Marshal(tokenData)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create pagination token: %w", err)
		}
		nextToken = base64.StdEncoding.EncodeToString(tokenJSON)
	}

	return results, nextToken, nil
}

// DeleteByPattern scans matching partition/range rows and batch-deletes them.
// DynamoDB has no rvi# companion keys (reverse lookups use a GSI).
func (d *DynamoDBConnector) DeleteByPattern(ctx context.Context, opts PatternDeleteOpts) (int64, int64, error) {
	ctx, span := common.StartSpan(ctx, "DynamoDBConnector.DeleteByPattern")
	defer span.End()

	if d.readClient == nil || d.writeClient == nil {
		err := fmt.Errorf("DynamoDB client not initialized yet")
		common.SetTraceSpanError(span, err)
		return 0, 0, err
	}

	var matched, deleted int64
	var startKey map[string]types.AttributeValue

	for {
		out, err := d.readClient.Scan(ctx, &dynamodb.ScanInput{
			TableName:         aws.String(d.table),
			ExclusiveStartKey: startKey,
			Limit:             aws.Int32(100),
		})
		if err != nil {
			common.SetTraceSpanError(span, err)
			return matched, deleted, fmt.Errorf("dynamodb scan: %w", err)
		}

		var batch []types.WriteRequest
		for _, item := range out.Items {
			pkAttr, pkOk := item[d.partitionKeyName].(*types.AttributeValueMemberS)
			rkAttr, rkOk := item[d.rangeKeyName].(*types.AttributeValueMemberS)
			if !pkOk || !rkOk {
				continue
			}
			pk, rk := pkAttr.Value, rkAttr.Value
			if !partitionMatchesScope(pk, opts) || !rangeMatchesMethod(rk, opts.Method) {
				continue
			}
			matched++
			if opts.DryRun {
				continue
			}
			batch = append(batch, types.WriteRequest{
				DeleteRequest: &types.DeleteRequest{
					Key: map[string]types.AttributeValue{
						d.partitionKeyName: &types.AttributeValueMemberS{Value: pk},
						d.rangeKeyName:     &types.AttributeValueMemberS{Value: rk},
					},
				},
			})
			if len(batch) == 25 {
				n, err := d.flushDeleteBatch(ctx, batch)
				deleted += n
				if err != nil {
					common.SetTraceSpanError(span, err)
					return matched, deleted, err
				}
				batch = batch[:0]
			}
		}
		if !opts.DryRun && len(batch) > 0 {
			n, err := d.flushDeleteBatch(ctx, batch)
			deleted += n
			if err != nil {
				common.SetTraceSpanError(span, err)
				return matched, deleted, err
			}
		}

		if out.LastEvaluatedKey == nil {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	return matched, deleted, nil
}

func (d *DynamoDBConnector) flushDeleteBatch(ctx context.Context, batch []types.WriteRequest) (int64, error) {
	req := map[string][]types.WriteRequest{d.table: batch}
	out, err := d.writeClient.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: req,
	})
	if err != nil {
		return 0, fmt.Errorf("dynamodb BatchWriteItem: %w", err)
	}
	deleted := int64(len(batch))
	if unprocessed := out.UnprocessedItems[d.table]; len(unprocessed) > 0 {
		deleted -= int64(len(unprocessed))
		return deleted, fmt.Errorf("dynamodb BatchWriteItem left %d unprocessed deletes", len(unprocessed))
	}
	return deleted, nil
}
