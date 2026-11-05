"""A fake OpenAI-compatible model for checking a resumed conversation (E3) with synthetic data.

    python3 scripts/e2e/fakemodel.py <log>     prints the port; stop it with Ctrl-C

It logs the first two and last three messages of every request it receives, then answers
"E3 reply". Unlike setup's stand-in, it logs content, so use it only with the fixtures."""
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

log = open(sys.argv[1], "a", encoding="utf-8")


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = [m for m in body.get("messages", []) if m.get("role") != "system"]
        text = lambda m: (m["role"] + ": " + json.dumps(m.get("content"))[:80])
        shown = [text(m) for m in messages[:2]] + ["..."] + [text(m) for m in messages[-3:]]
        log.write(json.dumps({"model": body.get("model"), "messages": len(messages), "order": shown}) + "\n")
        log.flush()
        chunk = lambda d: f"data: {json.dumps(d)}\n\n".encode()
        base = {"id": "e3", "created": 0, "model": body.get("model")}
        if body.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            self.wfile.write(chunk({**base, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": {"role": "assistant", "content": "E3 reply"}, "finish_reason": None}]}))
            self.wfile.write(chunk({**base, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}))
            self.wfile.write(b"data: [DONE]\n\n")
            return
        reply = json.dumps({**base, "object": "chat.completion", "choices": [{"index": 0, "message": {"role": "assistant", "content": "E3 reply"}, "finish_reason": "stop"}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(reply)))
        self.end_headers()
        self.wfile.write(reply)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
print(server.server_address[1], flush=True)
server.serve_forever()
