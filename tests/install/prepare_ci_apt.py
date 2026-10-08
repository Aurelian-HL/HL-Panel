#!/usr/bin/env python3
"""Bound package downloads on disposable GitHub-hosted Ubuntu test VMs."""
import os
from pathlib import Path


def normalize_sources(source):
    for old, new in (
        ('http://azure.archive.ubuntu.com/ubuntu', 'https://archive.ubuntu.com/ubuntu'),
        ('https://azure.archive.ubuntu.com/ubuntu', 'https://archive.ubuntu.com/ubuntu'),
        ('http://archive.ubuntu.com/ubuntu', 'https://archive.ubuntu.com/ubuntu'),
        ('http://security.ubuntu.com/ubuntu', 'https://security.ubuntu.com/ubuntu'),
    ):
        source = source.replace(old, new)
    return source


def main():
    assert os.geteuid() == 0, 'Requires root on a disposable CI VM'
    assert os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true', 'Explicit CI isolation required'
    assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'Requires a GitHub-hosted runner'
    assert 'ID=ubuntu' in Path('/etc/os-release').read_text(), 'Requires Ubuntu'
    apt = Path('/etc/apt')
    sources = [apt / 'sources.list', apt / 'apt-mirrors.txt']
    sources += list((apt / 'sources.list.d').glob('*.list'))
    sources += list((apt / 'sources.list.d').glob('*.sources'))
    for path in sources:
        if not path.is_file():
            continue
        original = path.read_text()
        updated = normalize_sources(original)
        if updated != original:
            path.write_text(updated)
            print('Normalized CI Ubuntu mirror: ' + str(path))
    (apt / 'apt.conf.d' / '99hl-panel-ci-downloads').write_text(
        'Acquire::http::Timeout "20";\n'
        'Acquire::https::Timeout "20";\n'
        'Acquire::Retries "2";\n'
        'Acquire::Languages "none";\n'
    )


if __name__ == '__main__':
    main()
