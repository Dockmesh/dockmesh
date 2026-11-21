#!/usr/bin/env python3
"""Logs the create requests the docker CLI sends to the daemon.

Listens on a unix socket, forwards every request to the Docker socket and
appends the JSON body of each service, network, volume, secret and config
create request to a log file, one JSON object per line. Every request is
sent upstream with "Connection: close" so each one passes through here.

Usage: sockproxy.py <listen.sock> <log.jsonl> [/var/run/docker.sock]
"""
import json
import os
import re
import socket
import sys
import threading

LISTEN, LOG = sys.argv[1], sys.argv[2]
UPSTREAM = sys.argv[3] if len(sys.argv) > 3 else "/var/run/docker.sock"
LOGGED = re.compile(r"^/v[0-9.]+/(services|networks|volumes|secrets|configs)/create$")
lock = threading.Lock()


def read_request(conn):
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = conn.recv(65536)
        if not chunk:
            return None
        buf += chunk
    head, body = buf.split(b"\r\n\r\n", 1)
    lines = head.decode("latin-1").split("\r\n")
    method, path, _ = lines[0].split(" ", 2)
    headers = []
    length, chunked = 0, False
    for line in lines[1:]:
        k, v = line.split(":", 1)
        if k.lower() == "content-length":
            length = int(v.strip())
        if k.lower() == "transfer-encoding" and "chunked" in v.lower():
            chunked = True
        if k.lower() in ("connection", "transfer-encoding", "content-length"):
            continue
        headers.append(line)
    if chunked:
        while not body.endswith(b"0\r\n\r\n"):
            chunk = conn.recv(65536)
            if not chunk:
                break
            body += chunk
        raw, out = body, b""
        while raw:
            size_line, raw = raw.split(b"\r\n", 1)
            size = int(size_line.split(b";")[0], 16)
            if size == 0:
                break
            out += raw[:size]
            raw = raw[size + 2:]
        body = out
    else:
        while len(body) < length:
            chunk = conn.recv(65536)
            if not chunk:
                break
            body += chunk
    return method, path, headers, body


def handle(client):
    try:
        req = read_request(client)
        if req is None:
            return
        method, path, headers, body = req
        if method == "POST" and LOGGED.match(path.split("?")[0]) and body:
            with lock, open(LOG, "a") as f:
                f.write(json.dumps({"path": path.split("?")[0], "body": json.loads(body)}) + "\n")
        up = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        up.connect(UPSTREAM)
        head = "\r\n".join([f"{method} {path} HTTP/1.1"] + headers + [f"Content-Length: {len(body)}", "Connection: close"])
        up.sendall(head.encode("latin-1") + b"\r\n\r\n" + body)
        while True:
            chunk = up.recv(65536)
            if not chunk:
                break
            client.sendall(chunk)
        up.close()
    except (OSError, ValueError) as e:
        print(f"sockproxy: {e}", file=sys.stderr)
    finally:
        client.close()


def main():
    if os.path.exists(LISTEN):
        os.unlink(LISTEN)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(LISTEN)
    os.chmod(LISTEN, 0o600)
    srv.listen(64)
    while True:
        client, _ = srv.accept()
        threading.Thread(target=handle, args=(client,), daemon=True).start()


if __name__ == "__main__":
    main()
