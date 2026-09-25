package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

func init() {
	storage.NewS3 = New
}

// Client implements storage for Amazon S3 and S3-compatible services.
// Cloudflare R2 speaks the same API and uses this client too.
type Client struct {
	client *s3.Client
	bucket string
}

var (
	_ storage.ObjectStore   = (*Client)(nil)
	_ storage.BucketCreator = (*Client)(nil)
)

// New creates a new S3 storage client
func New(cfg *storage.StorageConfig) (storage.ObjectStore, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return NewClient(s3.NewFromConfig(awsCfg, buildS3Options(cfg)), cfg.Bucket), nil
}

// NewClient wraps an SDK client configured for a provider's endpoint.
func NewClient(client *s3.Client, bucket string) *Client {
	return &Client{client: client, bucket: bucket}
}

// buildS3Options returns the functional options applied to the S3 client.
// When a custom endpoint is configured (i.e. an S3-compatible provider such as
// Backblaze B2, MinIO or Wasabi rather than AWS), it points the client at that
// endpoint and relaxes checksum behavior to WhenRequired. The AWS SDK's default
// (WhenSupported) sends x-amz-checksum integrity headers that several
// S3-compatible providers reject; leaving the endpoint empty preserves the
// AWS-native defaults unchanged.
func buildS3Options(cfg *storage.StorageConfig) func(*s3.Options) {
	return func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(storage.NormalizeEndpoint(cfg.Endpoint))
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
			// Path-style addressing for servers that don't resolve buckets as
			// subdomains (Ceph RGW, MinIO without wildcard DNS). Left false for
			// AWS, which prefers virtual-hosted style.
			o.UsePathStyle = cfg.UsePathStyle
		}
	}
}

// Put stores data at key if pre holds and returns the new ETag.
func (c *Client) Put(ctx context.Context, key string, data []byte, pre storage.Precondition) (string, error) {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/octet-stream"),
	}
	if pre.IfNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	if pre.IfMatch != "" {
		in.IfMatch = aws.String(pre.IfMatch)
	}
	out, err := c.client.PutObject(ctx, in)
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, mapError(err))
	}
	return aws.ToString(out.ETag), nil
}

// Get returns an object's data and ETag.
func (c *Client) Get(ctx context.Context, key string) ([]byte, string, error) {
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("get %s: %w", key, mapError(err))
	}
	defer func() { _ = out.Body.Close() }()
	data, err := storage.ReadAllLimited(out.Body, key)
	if err != nil {
		return nil, "", err
	}
	return data, aws.ToString(out.ETag), nil
}

// mapError adds the storage sentinel the engine acts on to S3 errors, keeping
// the original error in the chain for messages.
func mapError(err error) error {
	var apiErr smithy.APIError
	code := ""
	if errors.As(err, &apiErr) {
		code = apiErr.ErrorCode()
	}
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) {
		return err
	}
	switch status := respErr.HTTPStatusCode(); {
	case status == http.StatusPreconditionFailed:
		return fmt.Errorf("%w: %w", storage.ErrPreconditionFailed, err)
	// S3 answers a conditional write that races another one with 409.
	case status == http.StatusConflict && code == "ConditionalRequestConflict":
		return fmt.Errorf("%w: %w", storage.ErrPreconditionFailed, err)
	case status == http.StatusNotFound && code != "NoSuchBucket":
		return fmt.Errorf("%w: %w", storage.ErrNotFound, err)
	}
	return err
}

// Delete removes the object with the given key
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete %s: %w", key, err)
	}
	return nil
}

// List returns all objects with the given prefix
func (c *Client) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	var objects []storage.ObjectInfo
	var continuationToken *string

	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			ContinuationToken: continuationToken,
		}
		if prefix != "" {
			input.Prefix = aws.String(prefix)
		}

		result, err := c.client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range result.Contents {
			objects = append(objects, storage.ObjectInfo{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
			})
		}

		if !aws.ToBool(result.IsTruncated) {
			break
		}
		continuationToken = result.NextContinuationToken
	}

	return objects, nil
}

// Head returns metadata for the given key without downloading content
func (c *Client) Head(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	result, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("head %s: %w", key, mapError(err))
	}

	return &storage.ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(result.ContentLength),
		LastModified: aws.ToTime(result.LastModified),
		ETag:         aws.ToString(result.ETag),
	}, nil
}

// CreateBucket creates the configured bucket. R2 and S3 refuse this unless
// the credentials allow managing buckets.
func (c *Client) CreateBucket(ctx context.Context) error {
	_, err := c.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return fmt.Errorf("create bucket %s: %w", c.bucket, err)
	}
	return nil
}

// BucketExists checks if the configured bucket exists
func (c *Client) BucketExists(ctx context.Context) (bool, error) {
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	})
	if err != nil {
		var notFound *types.NotFound
		var noSuchBucket *types.NoSuchBucket
		if errors.As(err, &notFound) || errors.As(err, &noSuchBucket) {
			return false, nil
		}
		var re *awshttp.ResponseError
		if errors.As(err, &re) && re.HTTPStatusCode() == 403 {
			// R2 answers 403, not 404, when the token may not see the bucket,
			// whether or not it exists.
			return false, fmt.Errorf("the credentials may not use the bucket %q (HTTP 403): it does not exist and "+
				"they may not create it, or the API token is limited to other buckets. Give the token read and "+
				"write access to this bucket (for R2: an Object Read & Write token for it), creating the bucket first if needed", c.bucket)
		}
		return false, fmt.Errorf("failed to check bucket: %w", err)
	}
	return true, nil
}
