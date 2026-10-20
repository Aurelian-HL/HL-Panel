"""Loopback-only issuer include preservation check on disposable Linux CI hosts."""
import os
from pathlib import Path
import ssl
import subprocess
import urllib.request


class IssuerNginxFixture:
    domain = 'hl-panel-smoke.invalid'
    site = Path('/etc/nginx/sites-available/hl-panel.conf')
    snippet = Path('/etc/nginx/snippets/hl-license-issuer-locations.conf')
    route = '/api/v2/public/hl-license/__ci_include_probe'
    response = b'isolated-issuer-include-preserved\n'

    def __init__(self, domain='hl-panel-smoke.invalid'):
        assert os.geteuid() == 0
        assert os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
        assert domain in ('hl-panel-smoke.invalid', 'hl-panel.invalid')
        self.domain = domain
        assert not self.snippet.exists() and not self.snippet.is_symlink()
        original = self.site.read_text()
        # Only the domain's HTTPS server receives this independent include.
        # Do not expose issuer routes on HTTP or the IP-address server.
        servers = original.split('server {')
        matching = [index for index, server in enumerate(servers)
                    if 'server_name '+self.domain+';' in server
                    and 'listen 443 ssl' in server]
        assert len(matching) == 1, 'Expected exactly one disposable HTTPS domain server'
        marker = '    include /etc/nginx/snippets/hl-panel-app-locations.conf;'
        index = matching[0]
        assert servers[index].count(marker) == 1
        servers[index] = servers[index].replace(marker,
            '    include '+str(self.snippet)+';\n'+marker)
        self.snippet.write_text('location = '+self.route+' {\n'
            '    default_type text/plain;\n'
            '    return 200 "isolated-issuer-include-preserved\\n";\n}\n')
        self.snippet.chmod(0o644)
        self.site.write_text('server {'.join(servers))
        self.site_bytes = self.site.read_bytes()
        self.snippet_bytes = self.snippet.read_bytes()
        subprocess.run(['nginx', '-t'], check=True)
        subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
        self.verify()

    def verify(self):
        assert self.site.read_bytes() == self.site_bytes, 'Issuer include site changed'
        assert self.snippet.read_bytes() == self.snippet_bytes, 'Issuer include changed'
        subprocess.run(['nginx', '-t'], check=True)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
        request = urllib.request.Request('https://127.0.0.1'+self.route,
            headers={'Host': self.domain})
        with opener.open(request, timeout=10) as response:
            assert response.status == 200 and response.read() == self.response
