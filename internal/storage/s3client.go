package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client manages the connection to MaxIOFS
type S3Client struct {
	client   *s3.Client
	endpoint string
}

// BucketInfo contains bucket information
type BucketInfo struct {
	Name         string
	CreationDate string
}

// ObjectInfo contains object information
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	IsDir        bool
	ETag         string
}

// NewS3Client creates a new client to connect to MaxIOFS
func NewS3Client(endpoint, accessKeyID, secretAccessKey string, useSSL bool) (*S3Client, error) {
	// Configure credentials
	creds := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")

	// Configure custom endpoint for MaxIOFS
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		scheme := "https"
		if !useSSL {
			scheme = "http"
		}
		return aws.Endpoint{
			URL:               fmt.Sprintf("%s://%s", scheme, endpoint),
			SigningRegion:     "us-east-1",
			HostnameImmutable: true,
		}, nil
	})

	// Create configuration
	cfg := aws.Config{
		Region:                      "us-east-1",
		Credentials:                 creds,
		EndpointResolverWithOptions: customResolver,
	}

	// Create S3 client
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true // Important for custom endpoints
	})

	return &S3Client{
		client:   client,
		endpoint: endpoint,
	}, nil
}

// TestConnection verifies the connection to MaxIOFS
func (s *S3Client) TestConnection(ctx context.Context) error {
	_, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	return err
}

// ListBuckets lists all available buckets
func (s *S3Client) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	result, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("error listing buckets: %w", err)
	}

	buckets := make([]BucketInfo, 0, len(result.Buckets))
	for _, bucket := range result.Buckets {
		buckets = append(buckets, BucketInfo{
			Name:         aws.ToString(bucket.Name),
			CreationDate: bucket.CreationDate.Format("2006-01-02 15:04:05"),
		})
	}

	return buckets, nil
}

// ListObjects lists objects in a bucket with a prefix (recursive)
func (s *S3Client) ListObjects(ctx context.Context, bucketName, prefix string) ([]ObjectInfo, error) {
	fmt.Printf("[S3Client.ListObjects] bucket=%s prefix='%s' (RECURSIVE)\n", bucketName, prefix)

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
		Prefix: aws.String(prefix),
		// DO NOT use Delimiter to get recursive listing
	}

	var objects []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, input)

	// Iterate through all pages
	for paginator.HasMorePages() {
		result, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Printf("[S3Client.ListObjects] Error: %v\n", err)
			return nil, fmt.Errorf("error listing objects: %w", err)
		}

		fmt.Printf("[S3Client.ListObjects] Page Contents count: %d\n", len(result.Contents))

		// Add all files and directories
		for _, obj := range result.Contents {
			key := aws.ToString(obj.Key)
			size := aws.ToInt64(obj.Size)

			// Determine if it's a directory (ends with /)
			isDir := len(key) > 0 && key[len(key)-1] == '/'

			if isDir {
				fmt.Printf("[S3Client.ListObjects] Adding directory: %s\n", key)
			} else {
				fmt.Printf("[S3Client.ListObjects] Adding file: %s (size=%d)\n", key, size)
			}

			objects = append(objects, ObjectInfo{
				Key:          key,
				Size:         size,
				LastModified: *obj.LastModified,
				IsDir:        isDir,
				ETag:         aws.ToString(obj.ETag),
			})
		}
	}

	fmt.Printf("[S3Client.ListObjects] Total objects returned: %d\n", len(objects))
	return objects, nil
}

// UploadFile uploads a file to the bucket
func (s *S3Client) UploadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("error uploading file: %w", err)
	}

	return nil
}

// UploadData uploads data from memory to the bucket
func (s *S3Client) UploadData(ctx context.Context, bucketName, objectName string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("error uploading data: %w", err)
	}

	return nil
}

// DownloadFile downloads a file from the bucket
func (s *S3Client) DownloadFile(ctx context.Context, bucketName, objectName, destPath string) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("error downloading file: %w", err)
	}
	defer result.Body.Close()

	// Create destination file
	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	// Copy contents
	_, err = io.Copy(file, result.Body)
	if err != nil {
		return fmt.Errorf("error writing file: %w", err)
	}

	return nil
}

// GetObject retrieves an object for reading
func (s *S3Client) GetObject(ctx context.Context, bucketName, objectName string) (io.ReadCloser, int64, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("error getting object: %w", err)
	}

	size := aws.ToInt64(result.ContentLength)
	return result.Body, size, nil
}

// DeleteObject deletes an object from the bucket
func (s *S3Client) DeleteObject(ctx context.Context, bucketName, objectName string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("error deleting object: %w", err)
	}
	return nil
}

// CopyObject copies an object within the bucket (server-side, without downloading)
func (s *S3Client) CopyObject(ctx context.Context, bucketName, sourceKey, destKey string) error {
	copySource := fmt.Sprintf("%s/%s", bucketName, sourceKey)

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucketName),
		CopySource: aws.String(copySource),
		Key:        aws.String(destKey),
	})
	if err != nil {
		return fmt.Errorf("error copying object: %w", err)
	}
	return nil
}

// CreateBucket creates a new bucket
func (s *S3Client) CreateBucket(ctx context.Context, bucketName string) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		return fmt.Errorf("error creating bucket: %w", err)
	}
	return nil
}

// GetObjectName extracts the file name from the path
func GetObjectName(filePath string) string {
	return filepath.Base(filePath)
}
