package upstream

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// RequestCompressionZstdLevel mirrors Pi's REQUEST_COMPRESSION_ZSTD_LEVEL.
const RequestCompressionZstdLevel = 3

var (
	zstdEncoderOnce sync.Once
	zstdEncoder     *zstd.Encoder
	zstdEncoderErr  error
)

// compressZstd compresses a request body the way Pi does before the SSE call.
// The encoder is reused because it is safe for concurrent use via EncodeAll.
func compressZstd(body []byte) ([]byte, error) {
	zstdEncoderOnce.Do(func() {
		zstdEncoder, zstdEncoderErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(RequestCompressionZstdLevel)),
			zstd.WithEncoderConcurrency(1),
		)
	})
	if zstdEncoderErr != nil {
		return nil, fmt.Errorf("upstream: zstd encoder: %w", zstdEncoderErr)
	}
	return zstdEncoder.EncodeAll(body, make([]byte, 0, len(body)/2)), nil
}

// jsonUnmarshalBytes decodes JSON into dst.
func jsonUnmarshalBytes(raw []byte, dst any) error {
	return json.Unmarshal(raw, dst)
}
