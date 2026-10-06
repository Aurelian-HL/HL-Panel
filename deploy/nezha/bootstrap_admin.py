"""Rotate the first-run Nezha administrator password over loopback."""

import argparse
import http.cookiejar
import json
import urllib.request


BASE_URL = "http://127.0.0.1:8008"


def request(opener, path, payload, token=None, csrf=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if csrf:
        headers["X-CSRF-Token"] = csrf
    body = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(BASE_URL + path, body, headers, method="POST")
    with opener.open(req, timeout=10) as response:
        result = json.load(response)
    if not result.get("success"):
        raise RuntimeError(f"Nezha rejected {path}")
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("password_file")
    args = parser.parse_args()
    with open(args.password_file, encoding="utf-8") as handle:
        new_password = handle.read().strip()
    if len(new_password) < 32:
        raise RuntimeError("new administrator password is too short")

    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    login = request(opener, "/api/v1/login", {"username": "admin", "password": "admin"})
    token = login["data"]["token"]
    csrf = next(cookie.value for cookie in jar if cookie.name == "nz-csrf")
    request(opener, "/api/v1/profile", {
        "original_password": "admin",
        "new_username": "admin",
        "new_password": new_password,
        "reject_password": False,
    }, token=token, csrf=csrf)
    request(opener, "/api/v1/login", {"username": "admin", "password": new_password})
    print("Nezha administrator password rotated and new login verified")


if __name__ == "__main__":
    main()
