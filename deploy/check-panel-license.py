#!/usr/bin/env python3
"""Read-only candidate preflight, before stopping a running panel."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys


def check(binary, env_file):
    values = {}
    for line in Path(env_file).read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        key, value = line.split('=', 1)
        if key.startswith('CONTROL_LICENSE_'):
            parts = shlex.split(value)
            if len(parts) != 1:
                raise ValueError('授权配置格式无效')
            values[key] = parts[0]
    directory = Path(values.get('CONTROL_LICENSE_STATE_DIR', '/var/lib/hl-panel-license'))
    if directory != Path('/var/lib/hl-panel-license') or directory.is_symlink():
        raise ValueError('请使用受管授权状态目录')
    for name in ('instance-key', 'identity.json'):
        path = directory/name
        if path.is_symlink() or not path.is_file():
            raise ValueError('尚未接入本机签名授权')
    arguments = ['runuser', '-u', 'hlpanel', '--', str(binary), 'license', 'status', '--state-dir', str(directory)]
    for key, flag in [('CONTROL_LICENSE_DOMAIN', '--domain'), ('CONTROL_LICENSE_INSTANCE_ID', '--instance')]:
        if values.get(key):
            arguments.extend([flag, values[key]])
    result = subprocess.run(arguments, capture_output=True, timeout=15, env={key:value for key,value in os.environ.items() if not key.startswith('CONTROL_LICENSE_')})
    if result.returncode:
        raise ValueError('候选版本无法读取或校验本机授权身份')
    status = json.loads(result.stdout)
    awaiting = (status.get('status') == 'unactivated' and not status.get('license_id')
                and not status.get('last_error') and not status.get('management_allowed'))
    if not status.get('enabled') or (not status.get('license_id') and not awaiting):
        raise ValueError('本机尚未完成有效签名授权绑定')
    return status


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--env-file', type=Path, required=True)
    args = parser.parse_args()
    try:
        check(args.binary, args.env_file)
    except (OSError, ValueError, subprocess.SubprocessError):
        print('升级前授权检查未通过，当前服务未停止。请先使用已校验发行包的 deploy/setup-panel-license.sh --domain 管理域名 完成授权接入；勿删除授权身份或使用全新安装覆盖现有面板。', file=sys.stderr)
        return 1
    print('升级前本机授权身份检查通过（待验证或到期时限制管理，存量服务继续保留；升级不等于授权激活）。')
    return 0


if __name__ == '__main__':
    sys.exit(main())
