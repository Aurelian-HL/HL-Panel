"""Create a server-scoped Nezha inventory PAT without printing its value."""

import argparse
import http.cookiejar
import json
import os
import pathlib
import urllib.request


BASE_URL = "http://127.0.0.1:8008"


def post(opener, path, payload, token=None, csrf=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if csrf:
        headers["X-CSRF-Token"] = csrf
    req = urllib.request.Request(
        BASE_URL + path,
        json.dumps(payload).encode("utf-8"),
        headers,
        method="POST",
    )
    with opener.open(req, timeout=10) as response:
        result = json.load(response)
    if not result.get("success"):
        raise RuntimeError(f"Nezha rejected {path}")
    return result["data"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("password_file")
    parser.add_argument("pat_file")
    parser.add_argument("server_ids", nargs="+", type=int)
    args = parser.parse_args()
    server_ids = sorted(set(args.server_ids))
    if not server_ids or min(server_ids) < 1:
        raise RuntimeError("server IDs must be positive")
    if pathlib.Path(args.pat_file).exists():
        raise RuntimeError("refusing to replace an existing PAT")

    password = pathlib.Path(args.password_file).read_text(encoding="utf-8").strip()
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    session = post(opener, "/api/v1/login", {"username": "admin", "password": password})
    csrf = next(cookie.value for cookie in jar if cookie.name == "nz-csrf")
    created = post(opener, "/api/v1/api-tokens", {
        "name": "hl-panel-inventory",
        "scopes": ["nezha:inventory:read"],
        "server_ids": server_ids,
        "expires_in_days": 365,
    }, token=session["token"], csrf=csrf)
    token = created["token"]
    if not isinstance(token, str) or not token.startswith("nzp_"):
        raise RuntimeError("Nezha did not return a PAT")
    fd = os.open(args.pat_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        handle.write(token)
    print(f"Created server-scoped read-only PAT for {len(server_ids)} servers")


if __name__ == "__main__":
    main()
