package stargzs3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/containerd/log"
	"github.com/containerd/stargz-snapshotter/fs/remote"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-image-builder/bento-image-snapshotter/fs"

	"github.com/bentoml/yatai-image-builder/common"
)

type ResolveHandler struct{}

func (r *ResolveHandler) Handle(ctx context.Context, desc ocispec.Descriptor) (remote.Fetcher, int64, error) {
	client, err := fs.GetS3Client(ctx, false)
	if err != nil {
		return nil, 0, errors.Wrap(err, "failed to get s3 client")
	}
	bucket := desc.Annotations[common.DescriptorAnnotationBucket]
	objectKey := desc.Annotations[common.DescriptorAnnotationObjectKey]
	size, err := fs.StatS3ObjectSize(ctx, client, bucket, objectKey)
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
	_, err := fs.StatS3ObjectSize(context.Background(), f.client, f.bucket, f.objectKey)
	return errors.Wrapf(err, "failed to stat %s/%s", f.bucket, f.objectKey)
}

func (f *fetcher) GenID(off int64, size int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%s-%d-%d", f.bucket, f.objectKey, off, size)))
	return hex.EncodeToString(sum[:])
}
