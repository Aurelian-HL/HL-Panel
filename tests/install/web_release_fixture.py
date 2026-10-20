"""Fixed loopback release metadata for the disposable Web-update baseline.

Only a Go build overlay changes the baseline's release-list URL. The real
candidate archive and production source retain GitHub's URL and normal policy.
"""
import argparse
import atexit
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import threading


ADDRESS = ('127.0.0.1', 19994)
ROUTE = '/repos/Aurelian-HL/HL-Panel/releases?per_page=100'
URL = 'http://127.0.0.1:19994'+ROUTE
ORIGINAL = 'const apiURL = "https://api.github.com'+ROUTE+'"'


def prepare_overlay(repository, destination):
    """Create an external overlay; never edit or package production sources."""
    source = repository/'internal/control/releases/service.go'
    original = source.read_text(encoding='utf-8')
    assert original.count(ORIGINAL) == 1, 'Release URL changed; review fixture'
    destination.mkdir()
    replaced = destination/'service.go'
    replaced.write_text(original.replace(ORIGINAL, 'const apiURL = '+json.dumps(URL)), encoding='utf-8')
    overlay = destination/'overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(source.resolve()): str(replaced.resolve())}})+'\n', encoding='utf-8')
    assert source.read_text(encoding='utf-8') == original
    return overlay


class WebReleaseFixture:
    def __init__(self, version):
        assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
        assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
        assert re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version)
        self.requests = 0
        fixture = self
        metadata = json.dumps([{'tag_name': tag, 'draft': False, 'prerelease': False,
            'published_at': '2026-01-01T00:00:00Z'} for tag in ('v0.1.0', version)]).encode()

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path != ROUTE or self.headers.get('Authorization'):
                    self.send_error(404)
                    return
                fixture.requests += 1
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(metadata)))
                self.end_headers()
                self.wfile.write(metadata)

            def log_message(self, *_):
                pass

        self.server = ThreadingHTTPServer(ADDRESS, Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        atexit.register(self.close)

    def close(self):
        if self.thread.is_alive():
            self.server.shutdown()
            self.server.server_close()
            self.thread.join(timeout=5)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-directory', type=Path, required=True)
    args = parser.parse_args()
    assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
    print(prepare_overlay(Path(__file__).resolve().parents[2], args.output_directory))
