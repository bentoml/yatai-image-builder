//nolint:unused
package fs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	urlpkg "net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/containerd/log"
	"github.com/pkg/errors"
)

var sentinelURL = urlpkg.URL{}

const (
	// Amazon Accelerated Transfer endpoint
	transferAccelEndpoint = "s3-accelerate.amazonaws.com"

	// Google Cloud Storage endpoint
	gcsEndpoint = "storage.googleapis.com"
)

func supportsTransferAcceleration(endpoint urlpkg.URL) bool {
	return endpoint.Hostname() == transferAccelEndpoint
}

func IsGoogleEndpoint(endpoint urlpkg.URL) bool {
	return endpoint.Hostname() == gcsEndpoint
}

// isVirtualHostStyle reports whether the given endpoint supports S3 virtual
// host style bucket name resolving. If a custom S3 API compatible endpoint is
// given, resolve the bucketname from the URL path.
func isVirtualHostStyle(endpoint urlpkg.URL) bool {
	return endpoint == sentinelURL || supportsTransferAcceleration(endpoint) || IsGoogleEndpoint(endpoint)
}

func parseEndpoint(endpoint string) (urlpkg.URL, error) {
	if endpoint == "" {
		return sentinelURL, nil
	}

	u, err := urlpkg.Parse(endpoint)
	if err != nil {
		return sentinelURL, errors.Wrapf(err, "failed to parse endpoint: %s", endpoint)
	}

	return *u, nil
}

// ignoreSigningHeaders excludes the listed headers
// from the request signature because some providers may alter them.
//
// See https://github.com/aws/aws-sdk-go-v2/issues/1816.
func ignoreSigningHeaders(o *s3.Options, headers []string) {
	o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
		if err := stack.Finalize.Insert(ignoreHeaders(headers), "Signing", middleware.Before); err != nil {
			return errors.Wrap(err, "failed to insert ignoreHeaders middleware")
		}

		if err := stack.Finalize.Insert(restoreIgnored(), "Signing", middleware.After); err != nil {
			return errors.Wrap(err, "failed to insert restoreIgnored middleware")
		}

		return nil
	})
}

type ignoredHeadersKey struct{}

func ignoreHeaders(headers []string) middleware.FinalizeMiddleware {
	return middleware.FinalizeMiddlewareFunc(
		"IgnoreHeaders",
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (out middleware.FinalizeOutput, metadata middleware.Metadata, err error) {
			req, ok := in.Request.(*smithyhttp.Request)
			if !ok {
				return out, metadata, &v4.SigningError{Err: fmt.Errorf("(ignoreHeaders) unexpected request middleware type %T", in.Request)}
			}

			ignored := make(map[string]string, len(headers))
			for _, h := range headers {
				ignored[h] = req.Header.Get(h)
				req.Header.Del(h)
			}

			ctx = middleware.WithStackValue(ctx, ignoredHeadersKey{}, ignored)

			return next.HandleFinalize(ctx, in)
		},
	)
}

func restoreIgnored() middleware.FinalizeMiddleware {
	return middleware.FinalizeMiddlewareFunc(
		"RestoreIgnored",
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (out middleware.FinalizeOutput, metadata middleware.Metadata, err error) {
			req, ok := in.Request.(*smithyhttp.Request)
			if !ok {
				return out, metadata, &v4.SigningError{Err: fmt.Errorf("(restoreIgnored) unexpected request middleware type %T", in.Request)}
			}

			ignored, _ := middleware.GetStackValue(ctx, ignoredHeadersKey{}).(map[string]string)
			for k, v := range ignored {
				req.Header.Set(k, v)
			}

			return next.HandleFinalize(ctx, in)
		},
	)
}

func extractRegionFromEndpointURL(endpointURL string) string {
	re := regexp.MustCompile(`s3[.-](?P<region>[^.]+)\.`)
	match := re.FindStringSubmatch(endpointURL)
	if len(match) > 1 {
		return match[1]
	}
	return "us-east-1"
}

func StatS3ObjectSize(ctx context.Context, client *s3.Client, bucket, objectKey string) (int64, error) {
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

func GetS3PresignedURL(ctx context.Context, client *s3.Client, bucket, objectKey string) (string, error) {
	presignClient := s3.NewPresignClient(client)
	presignResult, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &objectKey,
	}, func(p *s3.PresignOptions) {
		p.Expires = time.Hour
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to presign request")
	}
	return presignResult.URL, nil
}

func GetS3Client(ctx context.Context, enabledPresigned bool) (*s3.Client, error) {
	s3EndpointURL := os.Getenv("S3_ENDPOINT_URL")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if s3EndpointURL == "" || accessKey == "" || secretKey == "" {
		log.G(ctx).Error("S3_ENDPOINT_URL, AWS_ACCESS_KEY, and AWS_SECRET_ACCESS_KEY must be set")
	}

	endpointURL, err := parseEndpoint(s3EndpointURL)
	if err != nil {
		return nil, err
	}

	// use virtual-host-style if the endpoint is known to support it,
	// otherwise use the path-style approach.
	isVirtualHostStyle := isVirtualHostStyle(endpointURL)

	useAccelerate := supportsTransferAcceleration(endpointURL)
	// AWS SDK handles transfer acceleration automatically. Setting the
	// Endpoint to a transfer acceleration endpoint would cause bucket
	// operations fail.
	if useAccelerate {
		endpointURL = sentinelURL
	}

	log.G(ctx).Infof("s3 endpoint: %s", endpointURL.String())

	region := extractRegionFromEndpointURL(endpointURL.String())

	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			PartitionID:       "aws",
			URL:               endpointURL.String(),
			SigningRegion:     region,
			HostnameImmutable: true,
		}, nil
	})

	cfg := aws.Config{
		Region:                      region,
		Credentials:                 credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		EndpointResolverWithOptions: resolver,
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = !isVirtualHostStyle
		o.UseAccelerate = useAccelerate
		if !enabledPresigned {
			// Google Cloud Storage alters the Accept-Encoding header, which breaks the v2 request signature
			// (https://github.com/aws/aws-sdk-go-v2/issues/1816)
			if IsGoogleEndpoint(endpointURL) {
				ignoreSigningHeaders(o, []string{"Accept-Encoding"})
			}
		}
	})

	return client, nil
}
