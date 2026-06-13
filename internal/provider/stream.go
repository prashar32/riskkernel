package provider

import "context"

// Streamer is the optional interface a provider implements to support streaming
// chat. The gateway type-asserts for it; a provider that doesn't implement it
// (Anthropic, Ollama, the stubs, for now) makes a streaming request fall back to a
// clear "unsupported" error rather than silently buffering.
type Streamer interface {
	// ChatStream starts a streaming completion. The returned ChatStream yields the
	// provider's raw SSE chunks verbatim (so the client receives authentic,
	// untranslated SSE) while it accumulates token usage. Honor ctx so the
	// governor's kill switch / time budget interrupt an in-flight stream.
	ChatStream(ctx context.Context, req Request) (ChatStream, error)
}

// ChatStream is an open streaming response. Recv returns the next raw SSE chunk to
// forward to the client (io.EOF when the stream ends). After io.EOF, Usage and
// Model report the call's final accounting, parsed from the stream.
type ChatStream interface {
	Recv() ([]byte, error)
	Usage() Usage
	Model() string
	Close() error
}
