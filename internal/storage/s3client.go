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

// S3Client maneja la conexión con MaxIOFS
type S3Client struct {
	client   *s3.Client
	endpoint string
}

// BucketInfo contiene información de un bucket
type BucketInfo struct {
	Name         string
	CreationDate string
}

// ObjectInfo contiene información de un objeto
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	IsDir        bool
	ETag         string
}

// NewS3Client crea un nuevo cliente para conectar con MaxIOFS
func NewS3Client(endpoint, accessKeyID, secretAccessKey string, useSSL bool) (*S3Client, error) {
	// Configurar credenciales
	creds := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")

	// Configurar endpoint personalizado para MaxIOFS
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

	// Crear configuración
	cfg := aws.Config{
		Region:                      "us-east-1",
		Credentials:                 creds,
		EndpointResolverWithOptions: customResolver,
	}

	// Crear cliente S3
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true // Importante para endpoints personalizados
	})

	return &S3Client{
		client:   client,
		endpoint: endpoint,
	}, nil
}

// TestConnection verifica la conexión con MaxIOFS
func (s *S3Client) TestConnection(ctx context.Context) error {
	_, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	return err
}

// ListBuckets lista todos los buckets disponibles
func (s *S3Client) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	result, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("error listando buckets: %w", err)
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

// ListObjects lista objetos en un bucket con un prefijo (recursivo)
func (s *S3Client) ListObjects(ctx context.Context, bucketName, prefix string) ([]ObjectInfo, error) {
	fmt.Printf("[S3Client.ListObjects] bucket=%s prefix='%s' (RECURSIVE)\n", bucketName, prefix)

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
		Prefix: aws.String(prefix),
		// NO usar Delimiter para obtener listado recursivo
	}

	var objects []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, input)

	// Iterar por todas las páginas
	for paginator.HasMorePages() {
		result, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Printf("[S3Client.ListObjects] Error: %v\n", err)
			return nil, fmt.Errorf("error listando objetos: %w", err)
		}

		fmt.Printf("[S3Client.ListObjects] Page Contents count: %d\n", len(result.Contents))

		// Agregar todos los archivos y directorios
		for _, obj := range result.Contents {
			key := aws.ToString(obj.Key)
			size := aws.ToInt64(obj.Size)

			// Determinar si es un directorio (termina en /)
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

// UploadFile sube un archivo al bucket
func (s *S3Client) UploadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("error abriendo archivo: %w", err)
	}
	defer file.Close()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("error subiendo archivo: %w", err)
	}

	return nil
}

// UploadData sube datos desde memoria al bucket
func (s *S3Client) UploadData(ctx context.Context, bucketName, objectName string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("error subiendo datos: %w", err)
	}

	return nil
}

// DownloadFile descarga un archivo del bucket
func (s *S3Client) DownloadFile(ctx context.Context, bucketName, objectName, destPath string) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("error descargando archivo: %w", err)
	}
	defer result.Body.Close()

	// Crear archivo destino
	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("error creando archivo: %w", err)
	}
	defer file.Close()

	// Copiar contenido
	_, err = io.Copy(file, result.Body)
	if err != nil {
		return fmt.Errorf("error escribiendo archivo: %w", err)
	}

	return nil
}

// GetObject obtiene un objeto para leer
func (s *S3Client) GetObject(ctx context.Context, bucketName, objectName string) (io.ReadCloser, int64, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("error obteniendo objeto: %w", err)
	}

	size := aws.ToInt64(result.ContentLength)
	return result.Body, size, nil
}

// DeleteObject elimina un objeto del bucket
func (s *S3Client) DeleteObject(ctx context.Context, bucketName, objectName string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("error eliminando objeto: %w", err)
	}
	return nil
}

// CopyObject copia un objeto dentro del bucket (server-side, sin descargar)
func (s *S3Client) CopyObject(ctx context.Context, bucketName, sourceKey, destKey string) error {
	copySource := fmt.Sprintf("%s/%s", bucketName, sourceKey)

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucketName),
		CopySource: aws.String(copySource),
		Key:        aws.String(destKey),
	})
	if err != nil {
		return fmt.Errorf("error copiando objeto: %w", err)
	}
	return nil
}

// CreateBucket crea un nuevo bucket
func (s *S3Client) CreateBucket(ctx context.Context, bucketName string) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		return fmt.Errorf("error creando bucket: %w", err)
	}
	return nil
}

// GetObjectName extrae el nombre del archivo del path
func GetObjectName(filePath string) string {
	return filepath.Base(filePath)
}
