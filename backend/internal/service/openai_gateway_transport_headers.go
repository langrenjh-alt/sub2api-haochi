package service

import "net/http"

// applyOpenAIStreamingTransportHeaders keeps SSE on the lowest-latency wire
// path. Go's Transport otherwise adds "Accept-Encoding: gzip" automatically;
// a streaming compressor is allowed to buffer small lifecycle events before
// producing an output block. SSE is already compact, so requesting identity
// encoding avoids that extra buffering without affecting non-streaming JSON or
// image responses.
func applyOpenAIStreamingTransportHeaders(header http.Header, stream bool) {
	if header == nil || !stream {
		return
	}
	header.Set("Accept-Encoding", "identity")
}
