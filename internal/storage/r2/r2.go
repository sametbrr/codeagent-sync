// Package r2 connects to Cloudflare R2. R2 speaks the S3 API, including
// conditional writes (If-Match / If-None-Match on PUT); it ignores If-Match
// on DELETE. The operations themselves live in the s3 package.
package r2

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/s3"
)

func init() {
	storage.NewR2 = New
}

// New creates a new R2 storage client
func New(cfg *storage.StorageConfig) (storage.ObjectStore, error) {
	endpoint := cfg.GetEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("R2 endpoint could not be determined")
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})
	return s3.NewClient(client, cfg.Bucket), nil
}
