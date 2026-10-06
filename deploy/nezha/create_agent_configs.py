"""Write per-node Nezha Agent configs without printing the shared secret."""

import argparse
import http.cookiejar
import json
import os
import pathlib
import urllib.request
import uuid


BASE_URL = "http://127.0.0.1:8008"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("password_file")
    parser.add_argument("output_directory")
    parser.add_argument("node_names", nargs="+")
    args = parser.parse_args()

    password = pathlib.Path(args.password_file).read_text(encoding="utf-8").strip()
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    login_request = urllib.request.Request(
        BASE_URL + "/api/v1/login",
        json.dumps({"username": "admin", "password": password}).encode(),
        {"Content-Type": "application/json"},
        method="POST",
    )
    with opener.open(login_request, timeout=10) as response:
        login = json.load(response)
    if not login.get("success"):
        raise RuntimeError("Nezha login failed")

    profile_request = urllib.request.Request(
        BASE_URL + "/api/v1/profile",
        headers={"Authorization": "Bearer " + login["data"]["token"]},
    )
    with opener.open(profile_request, timeout=10) as response:
        profile = json.load(response)
    if not profile.get("success"):
        raise RuntimeError("Nezha profile request failed")
    secret = profile["data"]["agent_secret"]
    if not isinstance(secret, str) or len(secret) < 16:
        raise RuntimeError("Nezha agent secret is invalid")

    output = pathlib.Path(args.output_directory)
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    for node_name in args.node_names:
        if not node_name.replace("-", "").isalnum():
            raise RuntimeError("invalid node name")
        destination = output / (node_name + ".yaml")
        if destination.exists():
            raise RuntimeError("refusing to replace an existing agent UUID")
        config = (
            f"server: xzf.hongle.cc:443\n"
            f"client_secret: {json.dumps(secret)}\n"
            f"uuid: {uuid.uuid4()}\n"
            "tls: true\n"
            "insecure_tls: false\n"
            "disable_auto_update: true\n"
            "disable_force_update: true\n"
            "disable_command_execute: true\n"
            "disable_nat: true\n"
            "disable_send_query: true\n"
        )
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(config)
    print(f"Created {len(args.node_names)} protected Agent configs")


if __name__ == "__main__":
    main()
