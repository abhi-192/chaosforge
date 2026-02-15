package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Writer struct {
	s3        *s3.Client
	bucket    string
	kmsKeyArn string
}

func New(s3c *s3.Client, bucket string, kmsKeyArn string) *Writer {
	return &Writer{s3: s3c, bucket: bucket, kmsKeyArn: kmsKeyArn}
}

func (w *Writer) PutJSON(ctx context.Context, key string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return w.PutBytes(ctx, key, b, "application/json")
}

func (w *Writer) PutBytes(ctx context.Context, key string, data []byte, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(w.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	}
	if w.kmsKeyArn != "" {
		in.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		in.SSEKMSKeyId = aws.String(w.kmsKeyArn)
	}
	_, err := w.s3.PutObject(ctx, in)
	if err != nil {
		return fmt.Errorf("put evidence %s: %w", key, err)
	}
	return nil
}
