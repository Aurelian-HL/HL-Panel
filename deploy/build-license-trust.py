#!/usr/bin/env python3
"""Validate public release trust variables and emit safe Go linker arguments.

Only public issuer keys belong here. Test signing keys are provisioned separately
under tests/install and are never inputs to this official build path.
"""
import base64
import json
import os
import re
import sys
from urllib.parse import urlsplit


def linker_flags(environment):
    values = [environment.get('HL_PANEL_LICENSE_' + name, '') for name in
              ('ISSUER_URL', 'ISSUER_ID', 'PUBLIC_KEYS_B64')]
    url, identity, encoded = values
    if not all(values) or any(re.search(r'[\s\x00-\x1f\x7f\'"\\]', value) for value in values):
        raise ValueError('All three public HL_PANEL_LICENSE trust variables are required')
    for value in (url, identity):
        parsed = urlsplit(value)
        if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError('Official issuer trust requires HTTPS without credentials, query or fragment')
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError('Invalid issuer port')
    if urlsplit(url).path not in ('', '/'):
        raise ValueError('Issuer URL must be an origin')
    if not re.fullmatch(r'[A-Za-z0-9_-]+', encoded):
        raise ValueError('Public keys must use unpadded base64url JSON')
    keys = json.loads(base64.urlsafe_b64decode(encoded + '=' * (-len(encoded) % 4)))
    if not isinstance(keys, dict) or not keys:
        raise ValueError('At least one issuer public key is required')
    for key_id, key in keys.items():
        if not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', key_id) or not isinstance(key, str) or not re.fullmatch(r'[A-Za-z0-9_-]{43}', key):
            raise ValueError('Invalid Ed25519 public key')
        raw = base64.urlsafe_b64decode(key + '=')
        if len(raw) != 32 or base64.urlsafe_b64encode(raw).decode().rstrip('=') != key:
            raise ValueError('Noncanonical Ed25519 public key')
    if 'disposable-ci-only' in keys:
        raise ValueError('Disposable CI trust cannot be used for an official artifact')
    return f'-X main.panelLicenseIssuerURL={url} -X main.panelLicenseIssuerID={identity} -X main.panelLicensePublicKeysB64={encoded}'


if __name__ == '__main__':
    try:
        print(linker_flags(os.environ))
    except (ValueError, TypeError, UnicodeError) as error:
        print('Release licensing trust rejected: ' + str(error), file=sys.stderr)
        sys.exit(1)
