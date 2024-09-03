package blob

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/errors"
)

type BlobStorage interface {
	Upload(ctx context.Context, name string, data io.Reader) error
	Download(ctx context.Context, name string) (io.ReadCloser, error)
	Exists(ctx context.Context, name string) (bool, error)
}

type S3Compatible struct {
	client *minio.Client
	bucket string
}

func NewS3Compatible(client *minio.Client, bucket string) *S3Compatible {
	return &S3Compatible{client: client, bucket: bucket}
}

func (s *S3Compatible) Upload(ctx context.Context, name string, data io.Reader) error {
	_, err := s.client.PutObject(ctx, s.bucket, name, data, -1, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to upload to S3/MinIO: %v", err)
	}
	return nil
}

func (s *S3Compatible) Download(ctx context.Context, name string) (io.ReadCloser, error) {
	return s.client.GetObject(ctx, s.bucket, name, minio.GetObjectOptions{})
}

func (s *S3Compatible) Exists(ctx context.Context, name string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, name, minio.StatObjectOptions{})
	if err != nil {
		if errors.Is(err, minio.ErrorResponse{Code: "NoSuchKey"}) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if object exists: %v", err)
	}
	return true, nil
}

type GCSStorage struct {
	client *storage.Client
	bucket string
}

func NewGCSStorage(client *storage.Client, bucket string) *GCSStorage {
	return &GCSStorage{client: client, bucket: bucket}
}

func (g *GCSStorage) Upload(ctx context.Context, name string, data io.Reader) error {
	writer := g.client.Bucket(g.bucket).Object(name).NewWriter(ctx)
	_, err := io.Copy(writer, data)
	if err != nil {
		return fmt.Errorf("failed to upload to GCS: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize GCS upload: %v", err)
	}
	return nil
}

func (g *GCSStorage) Download(ctx context.Context, name string) (io.ReadCloser, error) {
	return g.client.Bucket(g.bucket).Object(name).NewReader(ctx)
}

func (g *GCSStorage) Exists(ctx context.Context, name string) (bool, error) {
	_, err := g.client.Bucket(g.bucket).Object(name).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if object exists: %v", err)
	}
	return true, nil
}

type ABSStorage struct {
	client    *azblob.Client
	container string
}

func NewABSStorage(client *azblob.Client, container string) *ABSStorage {
	return &ABSStorage{client: client, container: container}
}

func (a *ABSStorage) Upload(ctx context.Context, name string, data io.Reader) error {
	_, err := a.client.UploadStream(ctx, a.container, name, data, nil)
	if err != nil {
		return fmt.Errorf("failed to upload to ABS: %v", err)
	}
	return nil
}

func (a *ABSStorage) Download(ctx context.Context, name string) (io.ReadCloser, error) {
	downloadResponse, err := a.client.DownloadStream(ctx, a.container, name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download from ABS: %v", err)
	}
	return downloadResponse.Body, nil
}

func (a *ABSStorage) Exists(ctx context.Context, name string) (bool, error) {
	_, err := a.client.DownloadStream(ctx, a.container, name, nil)
	if err != nil {
		if respErr, ok := err.(*azcore.ResponseError); ok {
			if respErr.ErrorCode == "ContainerNotFound" {
				return false, nil
			}
		}
		return false, fmt.Errorf("failed to check if object exists: %v", err)
	}
	return true, nil
}

func NewBlobStorage() (BlobStorage, error) {
	storageType := strings.ToLower(os.Getenv("STORAGE_TYPE"))

	switch storageType {
	case "s3", "minio":
		endpoint := os.Getenv("S3_ENDPOINT")
		accessKeyID := os.Getenv("S3_ACCESS_KEY_ID")
		secretAccessKey := os.Getenv("S3_SECRET_ACCESS_KEY")
		bucket := os.Getenv("S3_BUCKET")
		useSSL := os.Getenv("S3_USE_SSL") != "false"

		client, err := minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
			Secure: useSSL,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create S3/MinIO client: %v", err)
		}
		return NewS3Compatible(client, bucket), nil

	case "gcs":
		bucket := os.Getenv("GCS_BUCKET")
		client, err := storage.NewClient(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to create GCS client: %v", err)
		}
		return NewGCSStorage(client, bucket), nil

	case "abs":
		tenantID := os.Getenv("ABS_TENANT_ID")
		clientID := os.Getenv("ABS_CLIENT_ID")
		clientSecret := os.Getenv("ABS_CLIENT_SECRET")
		accountName := os.Getenv("ABS_ACCOUNT_NAME")
		containerName := os.Getenv("ABS_CONTAINER_NAME")
		tokenCredential, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create ABS token credential: %v", err)
		}
		client, err := azblob.NewClient(fmt.Sprintf("https://%s.blob.core.windows.net/%s", accountName, containerName), tokenCredential, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create ABS client: %v", err)
		}
		return NewABSStorage(client, containerName), nil

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}
