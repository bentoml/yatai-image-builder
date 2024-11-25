package stargzs3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/containerd/log"
	"github.com/containerd/stargz-snapshotter/fs/remote"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-image-builder/common"
)

func extractRegionFromEndpointURL(endpointURL string) string {
	re := regexp.MustCompile(`s3[.-](?P<region>[^.]+)\.`)
	match := re.FindStringSubmatch(endpointURL)
	if len(match) > 1 {
		return match[1]
	}
	return "us-east-1"
}

func statS3ObjectSize(ctx context.Context, client *s3.Client, bucket, objectKey string) (int64, error) {
	log.G(ctx).Infof("statting %s/%s", bucket, objectKey)
	output, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &bucket,
		Key:    &objectKey,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to head object")
	}
	if output.ContentLength == nil {
		return 0, errors.New("content length is nil")
	}
	return *output.ContentLength, nil
}

type ResolveHandler struct{}

func (r *ResolveHandler) Handle(ctx context.Context, desc ocispec.Descriptor) (remote.Fetcher, int64, error) {
	s3EndpointURL := os.Getenv("S3_ENDPOINT_URL")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if s3EndpointURL == "" || accessKey == "" || secretKey == "" {
		log.G(ctx).Error("S3_ENDPOINT_URL, AWS_ACCESS_KEY, and AWS_SECRET_ACCESS_KEY must be set")
	}

	log.G(ctx).Infof("s3 endpoint: %s", s3EndpointURL)

	region := extractRegionFromEndpointURL(s3EndpointURL)

	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			PartitionID:       "aws",
			URL:               s3EndpointURL,
			SigningRegion:     region,
			HostnameImmutable: true,
		}, nil
	})

	cfg := aws.Config{
		Region:                      region,
		Credentials:                 credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		EndpointResolverWithOptions: resolver,
	}

	client := s3.NewFromConfig(cfg)
	bucket := desc.Annotations[common.DescriptorAnnotationBucket]
	objectKey := desc.Annotations[common.DescriptorAnnotationObjectKey]
	size, err := statS3ObjectSize(ctx, client, bucket, objectKey)
	if err != nil {
		log.G(ctx).Errorf("failed to stat %s/%s: %v", bucket, objectKey, err)
		return nil, 0, nil
	}
	return &fetcher{bucket: bucket, objectKey: objectKey, size: size, client: client}, size, nil
}

type fetcher struct {
	bucket    string
	objectKey string
	size      int64

	client *s3.Client
}

func (f *fetcher) Fetch(ctx context.Context, off int64, size int64) (io.ReadCloser, error) {
	if off > f.size {
		return nil, errors.Errorf("offset is larger than the size of the blob %d(offset) > %d(blob size)", off, f.size)
	}

	o, s := int(off), int(size)
	input := &s3.GetObjectInput{
		Bucket: &f.bucket,
		Key:    &f.objectKey,
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", o, o+s-1)),
	}

	log.G(ctx).Debugf("fetching %s/%s with range %s", f.bucket, f.objectKey, *input.Range)
	output, err := f.client.GetObject(ctx, input)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get object %s/%s", f.bucket, f.objectKey)
	}

	return output.Body, nil
}

func (f *fetcher) Check() error {
	_, err := statS3ObjectSize(context.Background(), f.client, f.bucket, f.objectKey)
	return err
}

func (f *fetcher) GenID(off int64, size int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%s-%d-%d", f.bucket, f.objectKey, off, size)))
	return hex.EncodeToString(sum[:])
}
