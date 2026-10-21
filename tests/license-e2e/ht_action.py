"""Exercise the delivered HT handler without loading HT's production server.

Only an explicit isolated test may import the candidate modules. The session is
synthetic; handler authorization, request protection and issuer calls are real.
Inputs/results (including a one-time code) travel over private subprocess pipes.
"""
import json
import os
from pathlib import Path
import sys
from urllib.parse import urlsplit


def main():
    if os.environ.get('HL_LICENSE_PANEL_E2E') != 'true':
        raise RuntimeError('explicit isolated test required')
    endpoint = urlsplit(os.environ['HT_HL_LICENSE_ISSUER_URL'])
    if endpoint.scheme != 'https' or endpoint.hostname != '127.0.0.1':
        raise RuntimeError('only loopback HTTPS accepted')
    root = Path(os.environ['HL_LICENSE_PANEL_E2E_HT_ROOT']).resolve()
    if not (root / 'hl_license_api.py').is_file():
        raise RuntimeError('HT candidate handler missing')
    sys.path.insert(0, str(root))
    import hl_license_api
    import hl_license_service

    # Report only exception type and owning source line if the handler's safe
    # generic response hides a test integration defect. Never log values.
    original = hl_license_service.handle

    def diagnose(*args, **kwargs):
        try:
            return original(*args, **kwargs)
        except Exception as error:
            import traceback
            frames = traceback.extract_tb(error.__traceback__)
            print(type(error).__name__ + ':' + ','.join(
                Path(frame.filename).name + ':' + str(frame.lineno)
                for frame in frames), file=sys.stderr)
            raise

    hl_license_service.handle = diagnose

    # Go's private pipe carries UTF-8 JSON; Windows' console encoding must not
    # reinterpret Chinese reasons before the real HT service validates them.
    request = json.loads(sys.stdin.buffer.read().decode('utf-8'))

    class Handler:
        headers = {'Authorization': 'Bearer isolated-session',
                   'Content-Type': 'application/json',
                   'X-HL-License-Request': '1',
                   'Sec-Fetch-Site': 'same-origin',
                   'Idempotency-Key': request['key']}
        result = None

        def require_user(self):
            return {'id': 'synthetic-panel-e2e-admin', 'name': '隔离验收管理员',
                    'role': 'super_admin'}

        def send_json(self, status, data):
            self.result = {'status': status, 'data': data}

    handler = Handler()
    hl_license_api.handle(handler, request['method'],
                          hl_license_service.BASE + request['path'], request.get('body'))
    if handler.result is None:
        raise RuntimeError('HT handler returned no result')
    json.dump(handler.result, sys.stdout, ensure_ascii=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Do not print request bodies, codes, upstream credentials or tracebacks.
        print(type(error).__name__, file=sys.stderr)
        print('{"status":503,"data":{"code":"isolated_test_failed"}}')
        raise SystemExit(1) from None
