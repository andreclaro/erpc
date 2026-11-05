package data

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/erpc/erpc/common"
)

// createAWSConfig builds an AWS config for the given region using the
// credential source described by auth. Pass auth=nil to use the default
// credential chain (env vars, shared credentials file, EC2/ECS instance role,
// IRSA). Pass an empty region to let the SDK resolve it from AWS_REGION / IMDS.
//
// The returned config caches credentials internally — for EC2/ECS instance
// roles, IMDS calls are cached for ~6h with proactive refresh.
func createAWSConfig(ctx context.Context, auth *common.AwsAuthConfig, region string) (aws.Config, error) {
	optFns := []func(*config.LoadOptions) error{}
	if region != "" {
		optFns = append(optFns, config.WithRegion(region))
	}

	if auth == nil {
		return config.LoadDefaultConfig(ctx, optFns...)
	}

	switch auth.Mode {
	case "file":
		if auth.CredentialsFile != "" {
			// An empty file path must not be passed through: v1 treated it as
			// "use the default ~/.aws/credentials", v2 would read a bogus path.
			optFns = append(optFns, config.WithSharedCredentialsFiles([]string{auth.CredentialsFile}))
		}
		if auth.Profile != "" {
			optFns = append(optFns, config.WithSharedConfigProfile(auth.Profile))
		}
	case "env":
		optFns = append(optFns, config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			id := os.Getenv("AWS_ACCESS_KEY_ID")
			secret := os.Getenv("AWS_SECRET_ACCESS_KEY")
			if id == "" || secret == "" {
				return aws.Credentials{}, fmt.Errorf("env credentials require AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY to be set")
			}
			return aws.Credentials{
				AccessKeyID:     id,
				SecretAccessKey: secret,
				SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
				Source:          "EnvCredentials",
			}, nil
		})))
	case "secret":
		optFns = append(optFns, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(auth.AccessKeyID, auth.SecretAccessKey, ""),
		))
	default:
		return aws.Config{}, fmt.Errorf("unsupported auth.mode: %q (must be file, env, or secret)", auth.Mode)
	}
	return config.LoadDefaultConfig(ctx, optFns...)
}
