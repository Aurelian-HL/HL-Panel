"""CI-only signed cache provisioning. No product authorization bypass exists."""
import os
from pathlib import Path
import subprocess


def seed_license(domain, directory, owner=None):
    if not any(os.environ.get(key) == 'true' for key in
               ('HL_PANEL_DISPOSABLE_INSTALL_TEST', 'HL_PANEL_DISPOSABLE_MIGRATION_TEST')):
        raise RuntimeError('Requires an explicitly disposable CI environment')
    fixture = Path(os.environ['HL_PANEL_LICENSE_FIXTURE_DIR'])
    directory = Path(directory)
    directory.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([str(fixture/'licensefixture'), '--fixture', str(fixture),
                    '--domain', domain, '--state', str(directory)], check=True)
    if owner:
        import pwd
        account = pwd.getpwnam(owner)
        for path in [*directory.iterdir(), directory]:
            os.chown(path, account.pw_uid, account.pw_gid)
    return {'CONTROL_LICENSE_STATE_DIR': str(directory)}
