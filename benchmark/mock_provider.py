#!/usr/bin/env python3
"""A deterministic mock of the OpenAI Chat Completions API for the benchmark.

Every call returns a FIXED token usage, so the cost RiskKernel meters is exactly
reproducible — no API key, no real spend, no variance. This is what makes the
"dollars saved" number defensible: the only thing that differs between the two
runs is whether RiskKernel's budget stopped the loop.

Usage:  python3 mock_provider.py [port] [prompt_tokens] [completion_tokens]
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 9099
PROMPT_TOKENS = int(sys.argv[2]) if len(sys.argv) > 2 else 1000
COMPLETION_TOKENS = int(sys.argv[3]) if len(sys.argv) > 3 else 1000


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            req = json.loads(self.rfile.read(length) or b"{}")
        except Exception:
            req = {}
        model = req.get("model", "gpt-4o")
        out = json.dumps({
            "id": "chatcmpl-bench",
            "object": "chat.completion",
            "model": model,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "...still looping..."},
                "finish_reason": "stop",
            }],
            "usage": {
                "prompt_tokens": PROMPT_TOKENS,
                "completion_tokens": COMPLETION_TOKENS,
                "total_tokens": PROMPT_TOKENS + COMPLETION_TOKENS,
            },
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):  # keep the benchmark output clean
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
