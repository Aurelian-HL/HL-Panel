"""Show non-secret Nezha server IDs and report timestamps over loopback."""

import argparse
import http.cookiejar
import json
import pathlib
import urllib.request


BASE_URL = "http://127.0.0.1:8008"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("password_file")
    args = parser.parse_args()
    password = pathlib.Path(args.password_file).read_text(encoding="utf-8").strip()
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    login = urllib.request.Request(
        BASE_URL + "/api/v1/login",
        json.dumps({"username": "admin", "password": password}).encode(),
        {"Content-Type": "application/json"},
        method="POST",
    )
    with opener.open(login, timeout=10) as response:
        session = json.load(response)
    if not session.get("success"):
        raise RuntimeError("Nezha login failed")
    inventory = urllib.request.Request(
        BASE_URL + "/api/v1/server",
        headers={"Authorization": "Bearer " + session["data"]["token"]},
    )
    with opener.open(inventory, timeout=10) as response:
        servers = json.load(response)
    if not servers.get("success"):
        raise RuntimeError("Nezha inventory request failed")
    for server in servers["data"]:
        geoip = server.get("geoip") or {}
        print({
            "id": server.get("id"),
            "name": server.get("name"),
            "public_ip": geoip.get("ip"),
            "last_active": server.get("last_active"),
        })
    print(f"Server count: {len(servers['data'])}")


if __name__ == "__main__":
    main()
