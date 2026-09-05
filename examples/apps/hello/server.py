#!/usr/bin/env python3
import argparse
import http.server
import os
import socketserver

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
args = parser.parse_args()

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            body = b"ok\n"
        else:
            body = (os.environ.get("GREETING", "hello") + "\n").encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

class Server(http.server.ThreadingHTTPServer):
    # http.server resolves the bound address back to a hostname on startup,
    # and reverse DNS for loopback can stall for tens of seconds on macOS
    # hosts with slow or broken resolvers. The example binds a literal
    # address, so use it directly and skip the lookup.
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        host, port = self.socket.getsockname()[:2]
        self.server_name = host
        self.server_port = port

server = Server(("127.0.0.1", args.port), Handler)
server.serve_forever()
