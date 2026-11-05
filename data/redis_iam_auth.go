package data

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/erpc/erpc/common"
)

const (
	elastiCacheService = "elasticache"
	iamTokenValidity   = 15 * time.Minute

	// ElastiCache forcibly disconnects IAM-authenticated connections after 12h.
	// Rotate proactively at 11h so every new connection gets a fresh token
	// well before the server-side cliff.
	iamRedisConnMaxLifetime       = 11 * time.Hour
	iamRedisConnMaxLifetimeJitter = 30 * time.Minute
)

// emptyBodySHA256 is the SHA-256 of an empty payload, required by the SigV4
// presigner as the payload hash for a bodyless GET.
const emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// generateElastiCacheIAMToken produces a SigV4-presigned auth token for
// ElastiCache IAM authentication. The output is the presigned URL with the URL
// scheme stripped, which is used as the Redis AUTH password.
//
// Ref: https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/auth-iam.html
//
// Note on URL scheme: SigV4 does NOT include the URL scheme in the canonical
// request, so http:// and https:// produce identical signatures and tokens.
// We use https:// to match the AWS Go SDK convention in feature/rds/auth's
// BuildAuthToken, which documents "the scheme is arbitrary and is only needed
// because validation of the URL requires one." We strip both prefixes
// defensively in case the SDK normalizes the URL differently.
func generateElastiCacheIAMToken(ctx context.Context, awsCfg aws.Config, cfg *common.RedisIAMAuthConfig) (string, error) {
	// AWS lowercases cache names at creation time; SetDefaults normalizes this
	// already, but be defensive in case the struct is mutated after config load.
	cacheName := strings.ToLower(cfg.CacheName)

	req, err := http.NewRequest(http.MethodGet, "https://"+cacheName+"/", nil)
	if err != nil {
		return "", fmt.Errorf("elasticache iam: build request: %w", err)
	}

	q := req.URL.Query()
	q.Set("Action", "connect")
	q.Set("User", cfg.UserID)
	// X-Amz-Expires is the presigned-URL validity; v2's PresignHTTP does not
	// set it (v1's signer.Presign did), so set it explicitly.
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int64(iamTokenValidity/time.Second)))
	req.URL.RawQuery = q.Encode()

	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("elasticache iam: retrieve credentials: %w", err)
	}

	signer := v4.NewSigner()
	signedURI, _, err := signer.PresignHTTP(ctx, creds, req, emptyBodySHA256, elastiCacheService, awsCfg.Region, time.Now())
	if err != nil {
		return "", fmt.Errorf("elasticache iam: presign: %w", err)
	}

	// Strip whichever scheme the SDK produced — same defensive pattern as
	// feature/rds/auth's BuildAuthToken.
	if strings.HasPrefix(signedURI, "https://") {
		return signedURI[len("https://"):], nil
	}
	if strings.HasPrefix(signedURI, "http://") {
		return signedURI[len("http://"):], nil
	}
	return signedURI, nil
}

// newElastiCacheCredentialsProvider returns a go-redis CredentialsProviderContext
// callback. go-redis calls this function when establishing each new physical
// connection, so combined with ConnMaxLifetime ≈ 11h, every new connection
// receives a fresh token that is well within the 15-minute validity window.
func newElastiCacheCredentialsProvider(
	awsCfg aws.Config,
	cfg *common.RedisIAMAuthConfig,
) func(ctx context.Context) (string, string, error) {
	return func(ctx context.Context) (username, password string, err error) {
		token, err := generateElastiCacheIAMToken(ctx, awsCfg, cfg)
		if err != nil {
			return "", "", err
		}
		return cfg.UserID, token, nil
	}
}
