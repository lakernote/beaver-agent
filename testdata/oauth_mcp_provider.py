#!/usr/bin/env python3
"""Minimal OAuth + Streamable HTTP MCP provider for EasyAgent/Codex E2E tests.

This is intentionally not a real OAuth implementation. It auto-approves every
authorization request, keeps fake tokens in memory, and exposes one harmless
echo tool. It uses only Python's standard library.

Local:
  python3 testdata/oauth_mcp_provider.py

Remote Linux:
  python3 testdata/oauth_mcp_provider.py \
    --listen 0.0.0.0:19999 \
    --base-url http://10.224.58.180:19999
"""

from __future__ import annotations

import argparse
import json
import secrets
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlencode, urlparse, urlunparse


ACCESS_TOKEN = "local-test-access"
REFRESH_TOKEN = "local-test-refresh"
AUTHORIZATION_CODE = "local-test-code"


class Provider:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.lock = threading.Lock()
        self.clients: dict[str, dict] = {}
        self.valid_tokens: set[str] = set()

    def json_response(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        value: object,
        headers: dict[str, str] | None = None,
    ):
        body = json.dumps(value, separators=(",", ":")).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Cache-Control", "no-store")
        for key, value in (headers or {}).items():
            handler.send_header(key, value)
        handler.send_header("Content-Length", str(len(body)))
        handler.end_headers()
        handler.wfile.write(body)

    def oauth_error(self, handler: BaseHTTPRequestHandler, status: int, code: str, description: str):
        self.json_response(handler, status, {"error": code, "error_description": description})

    def protected_resource_metadata(self):
        return {
            "resource": self.base_url + "/mcp",
            "authorization_servers": [self.base_url],
            "scopes_supported": ["openid", "offline_access", "mcp"],
        }

    def authorization_server_metadata(self):
        return {
            "issuer": self.base_url,
            "authorization_endpoint": self.base_url + "/oauth/authorize",
            "token_endpoint": self.base_url + "/oauth/token",
            "registration_endpoint": self.base_url + "/oauth/register",
            "jwks_uri": self.base_url + "/oauth/jwks",
            "scopes_supported": ["openid", "offline_access", "mcp"],
            "response_types_supported": ["code"],
            "grant_types_supported": ["authorization_code", "refresh_token"],
            "token_endpoint_auth_methods_supported": ["none"],
            "code_challenge_methods_supported": ["S256"],
            "authorization_response_iss_parameter_supported": True,
        }

    def is_valid_token(self, token: str) -> bool:
        with self.lock:
            return token in self.valid_tokens

    def register(self, handler: BaseHTTPRequestHandler, payload: dict):
        redirect_uris = payload.get("redirect_uris")
        if not isinstance(redirect_uris, list) or not redirect_uris:
            self.oauth_error(handler, 400, "invalid_client_metadata", "redirect_uris is required")
            return
        client_id = "local-client-" + secrets.token_hex(4)
        with self.lock:
            self.clients[client_id] = payload
        self.json_response(
            handler,
            201,
            {
                "client_id": client_id,
                "redirect_uris": redirect_uris,
                "token_endpoint_auth_method": "none",
                "grant_types": payload.get("grant_types", ["authorization_code"]),
                "response_types": payload.get("response_types", ["code"]),
                "scope": payload.get("scope", ""),
            },
        )

    def authorize(self, handler, query):
        redirect_uri = query.get("redirect_uri", [""])[0].strip()
        state = query.get("state", [""])[0]
        if not redirect_uri or not state:
            self.oauth_error(handler, 400, "invalid_request", "redirect_uri and state are required")
            return
        parsed = urlparse(redirect_uri)
        if parsed.scheme not in ("http", "https") or not parsed.netloc:
            self.oauth_error(handler, 400, "invalid_request", "invalid redirect_uri")
            return
        callback_query = parse_qs(parsed.query, keep_blank_values=True)
        callback_query.update(
            {
                "code": [AUTHORIZATION_CODE],
                "state": [state],
                "iss": [self.base_url],
            }
        )
        callback = urlunparse(parsed._replace(query=urlencode(callback_query, doseq=True)))
        handler.send_response(302)
        handler.send_header("Location", callback)
        handler.end_headers()

    def token(self, handler, form):
        grant_type = form.get("grant_type", [""])[0]
        if grant_type == "authorization_code":
            if form.get("code", [""])[0] != AUTHORIZATION_CODE:
                self.oauth_error(handler, 400, "invalid_grant", "unknown authorization code")
                return
        elif grant_type == "refresh_token":
            if form.get("refresh_token", [""])[0] != REFRESH_TOKEN:
                self.oauth_error(handler, 400, "invalid_grant", "unknown refresh token")
                return
        else:
            self.oauth_error(handler, 400, "unsupported_grant_type", "grant_type is not supported")
            return

        with self.lock:
            self.valid_tokens.add(ACCESS_TOKEN)
        response = {
            "access_token": ACCESS_TOKEN,
            "token_type": "Bearer",
            "expires_in": 3600,
            "scope": form.get("scope", [""])[0],
        }
        # Deliberately omit refresh_token during refresh to exercise the
        # production code's refresh-token preservation behavior.
        if grant_type == "authorization_code":
            response["refresh_token"] = REFRESH_TOKEN
        self.json_response(handler, 200, response)


class Handler(BaseHTTPRequestHandler):
    server_version = "easyagent-oauth-test/1.0"

    @property
    def provider(self) -> Provider:
        return self.server.provider  # type: ignore[attr-defined]

    def log_message(self, fmt, *args):
        sys.stderr.write("[oauth-mcp] " + (fmt % args) + "\n")

    def bearer_token(self) -> str:
        value = self.headers.get("Authorization", "")
        prefix = "Bearer "
        if value.lower().startswith(prefix.lower()):
            return value[len(prefix) :].strip()
        return ""

    def require_auth(self) -> bool:
        if self.provider.is_valid_token(self.bearer_token()):
            return True
        self.send_response(401)
        self.send_header(
            "WWW-Authenticate",
            'Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="mcp"'
            % self.provider.base_url,
        )
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"error":"invalid_token"}')
        return False

    def read_json(self) -> dict | None:
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 1024 * 1024:
                return None
            return json.loads(self.rfile.read(length))
        except (ValueError, json.JSONDecodeError):
            return None

    def send_mcp_result(self, request_id, result: object, headers: dict[str, str] | None = None):
        self.provider.json_response(
            self, 200, {"jsonrpc": "2.0", "id": request_id, "result": result}, headers
        )

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/":
            self.provider.json_response(
                self, 200, {"mcp_endpoint": self.provider.base_url + "/mcp", "oauth": "local-test-only"}
            )
        elif path == "/.well-known/oauth-protected-resource":
            self.provider.json_response(self, 200, self.provider.protected_resource_metadata())
        elif path == "/.well-known/oauth-authorization-server":
            self.provider.json_response(self, 200, self.provider.authorization_server_metadata())
        elif path == "/oauth/authorize":
            self.provider.authorize(self, parse_qs(urlparse(self.path).query, keep_blank_values=True))
        elif path == "/oauth/jwks":
            self.provider.json_response(self, 200, {"keys": []})
        elif path == "/mcp":
            if self.require_auth():
                self.send_response(405)
                self.send_header("Allow", "POST")
                self.end_headers()
        else:
            self.send_error(404)

    def do_DELETE(self):
        path = urlparse(self.path).path
        if path != "/mcp" or not self.require_auth():
            return
        self.send_response(204)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        path = urlparse(self.path).path
        if path == "/oauth/register":
            payload = self.read_json()
            if payload is None:
                self.provider.oauth_error(self, 400, "invalid_request", "invalid JSON")
            else:
                self.provider.register(self, payload)
            return
        if path == "/oauth/token":
            length = int(self.headers.get("Content-Length", "0"))
            form = parse_qs(self.rfile.read(length).decode("utf-8"), keep_blank_values=True)
            self.provider.token(self, form)
            return
        if path != "/mcp" or not self.require_auth():
            return

        message = self.read_json()
        if message is None:
            self.send_error(400, "invalid JSON")
            return
        method = message.get("method")
        request_id = message.get("id")
        if method == "initialize":
            self.send_mcp_result(
                request_id,
                {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "easyagent-oauth-test", "version": "1.0.0"},
                },
                {"Mcp-Session-Id": "local-test-session"},
            )
        elif method == "notifications/initialized":
            self.send_response(202)
            self.end_headers()
        elif method == "tools/list":
            self.send_mcp_result(
                request_id,
                {
                    "tools": [
                        {
                            "name": "oauth_echo",
                            "description": "Return the supplied message from the OAuth test provider",
                            "inputSchema": {
                                "type": "object",
                                "properties": {"message": {"type": "string"}},
                            },
                        }
                    ]
                },
            )
        elif method == "tools/call":
            params = message.get("params", {})
            arguments = params.get("arguments", {})
            message_text = arguments.get("message") or "hello from oauth mcp"
            self.send_mcp_result(
                request_id,
                {"content": [{"type": "text", "text": str(message_text)}], "isError": False},
            )
        else:
            self.send_mcp_result(request_id, {"error": {"code": -32601, "message": "method not found"}})


class TestHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True


def main() -> int:
    parser = argparse.ArgumentParser(description="local OAuth MCP provider for E2E tests")
    parser.add_argument("--listen", default="127.0.0.1:19999", help="listen address")
    parser.add_argument("--base-url", help="advertised URL for remote browsers, e.g. http://10.224.58.180:19999")
    args = parser.parse_args()

    host, port_text = args.listen.rsplit(":", 1)
    port = int(port_text)
    advertised = args.base_url.rstrip("/") if args.base_url else f"http://127.0.0.1:{port}"
    parsed = urlparse(advertised)
    if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.path:
        parser.error("--base-url must be an http(s) URL without a path")

    provider = Provider(advertised)
    server = TestHTTPServer((host, port), Handler)
    server.provider = provider  # type: ignore[attr-defined]
    server.timeout = 1

    def stop(signum, frame):
        # shutdown() must run outside serve_forever's signal-handler thread.
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    print(f"OAuth MCP test provider: {advertised}/mcp", flush=True)
    print("Fake approval is automatic; no real credentials are used.", flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
