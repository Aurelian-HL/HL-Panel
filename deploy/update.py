#!/usr/bin/env python3
"""Root-only, fixed-origin release updates; preserves configuration and data."""
import argparse
import fcntl
import hashlib
import json
import os
import platform
import grp
from pathlib import Path, PurePosixPath
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tarfile
import time
from datetime import datetime, timezone
import urllib.parse
import urllib.request

ROOT = Path('/opt/hl-panel')
CONFIG = Path('/etc/hl-panel')
STATE = Path('/var/lib/hl-panel')
SERVICE = 'hl-panel-control-api.service'
API = 'https://api.github.com/repos/Aurelian-HL/HL-Panel'
REPO = 'https://github.com/Aurelian-HL/HL-Panel'
TAG = re.compile(r'^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')
WRAPPER = '#!/usr/bin/env bash\n# Managed by HL-panel.\nset -Eeuo pipefail\nexec python3 /opt/hl-panel/current/deploy/update.py "$@"\n'
APP_LOCATIONS = Path('/etc/nginx/snippets/hl-panel-app-locations.conf')
API_PROXY = Path('/etc/nginx/snippets/hl-panel-api-proxy.conf')
MIGRATION_PROXY = Path('/etc/nginx/snippets/hl-panel-migration-proxy.conf')
PROC_OVERRIDE = Path('/etc/systemd/system/hl-panel-control-api.service.d/20-host-metrics.conf')
LEGACY_PROC_OVERRIDE_CONTENT = '# Managed by HL-panel: allow read-only host metrics.\n[Service]\nProcSubset=all\n'
PROC_OVERRIDE_CONTENT = LEGACY_PROC_OVERRIDE_CONTENT + 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n'
SUBSCRIPTION_LOCATION = '''location ^~ /api/v1/public/subscriptions/ {
    access_log off;
    include /etc/nginx/snippets/hl-panel-api-proxy.conf;
}'''
MIGRATION_LOCATION = '''location ^~ /api/v1/panel/migration/ {
    client_max_body_size 65m;
    include /etc/nginx/snippets/hl-panel-migration-proxy.conf;
    access_log off;
}'''


class UpdateError(RuntimeError):
    pass


def require(value, message):
    if not value:
        raise UpdateError(message)


def run(arguments, *, output=None, input_file=None):
    result = subprocess.run(arguments, stdin=input_file, stdout=output or subprocess.PIPE, stderr=subprocess.PIPE)
    require(result.returncode == 0, '命令失败：' + Path(arguments[0]).name + '；未输出私有配置，请查看服务日志。')
    return result.stdout if output is None else None


def request(url, limit=2*1024*1024):
    # Every URL is constructed from the fixed repository. GitHub asset redirects
    # carry no authentication headers and retain standard TLS verification.
    req = urllib.request.Request(url, headers={'User-Agent': 'HL-panel-updater', 'Accept': 'application/vnd.github+json'})
    for attempt in range(2):
        try:
            with urllib.request.urlopen(req, timeout=45) as response:
                data = response.read(limit+1)
            require(len(data)<=limit, '下载内容超过允许大小')
            return data
        except (OSError, urllib.error.URLError):
            if attempt == 1:
                raise UpdateError('无法下载 GitHub 正式发布；尚未切换程序，请稍后重试。') from None


def version_tuple(value):
    match = TAG.fullmatch(value)
    require(match is not None, '仅支持 v数字.数字.数字 的正式版本')
    return tuple(int(part) for part in match.groups())


def private_regular(path):
    require(path.is_file() and not path.is_symlink(), '缺少受管配置：' + str(path))
    info = path.stat()
    require(info.st_uid == 0 and not (info.st_mode & 0o022), '配置必须归 root 所有且不能被其他用户修改')


def configuration():
    private_regular(CONFIG/'control-api.env')
    values = {}
    for line in (CONFIG/'control-api.env').read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        key, value = line.split('=',1)
        parts = shlex.split(value)
        require(len(parts)<=1, '服务配置格式无效')
        values[key] = parts[0] if parts else ''
    require(values.get('CONTROL_ALLOW_VOLATILE_STORE') == 'false', '更新只支持持久化 PostgreSQL 实例')
    require(values.get('CONTROL_DATABASE_URL_FILE') == str(CONFIG/'database-url'), '不支持非受管数据库路径')
    private_regular(CONFIG/'database-url')
    dsn = urllib.parse.urlsplit((CONFIG/'database-url').read_text().strip())
    require(dsn.scheme in ('postgres','postgresql') and dsn.hostname in ('127.0.0.1','localhost') and dsn.port in (None,5432), '自动更新仅支持安装器创建的本机 PostgreSQL；未修改任何数据')
    database = urllib.parse.unquote(dsn.path.lstrip('/'))
    require(database == 'hl_panel_control', '数据库不属于受管 HL-panel 实例')
    listen = values.get('CONTROL_LISTEN_ADDRESS','')
    require(re.fullmatch(r'127\.0\.0\.1:[0-9]{1,5}',listen), 'API 监听配置无效')
    return database, 'http://' + listen


def current_info(origin):
    # Never proxy local health requests to an external system proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(origin+'/api/v1/public/site-info',timeout=5) as response:
        return json.load(response)


def healthy(origin, version):
    for _ in range(30):
        try:
            if current_info(origin).get('platform_version') == version:
                run(['systemctl','is-active','--quiet',SERVICE])
                return
        except (OSError, ValueError, UpdateError):
            pass
        time.sleep(1)
    raise UpdateError('新版本启动或版本核验失败')


def digest(path):
    checksum = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024*1024), b''):
            checksum.update(chunk)
    return checksum.hexdigest()


def extract_verified(archive, destination):
    with tarfile.open(archive,'r:gz') as source:
        names = set()
        total = 0
        for entry in source.getmembers():
            name = entry.name.removeprefix('./').rstrip('/')
            if name in ('','.') and entry.isdir():
                continue
            parts = PurePosixPath(name)
            require(name and not parts.is_absolute() and all(part not in ('','.','..') for part in name.split('/')) and ':' not in name and '\\' not in name, '安装包路径不安全')
            require(entry.isfile() or entry.isdir(), '安装包不允许链接或特殊文件')
            require(name not in names,'安装包包含重复路径')
            names.add(name)
            total += entry.size
            require(total<=1024*1024*1024, '安装包解压大小过大')
        source.extractall(destination,filter='data') if sys.version_info >= (3,12) else source.extractall(destination)
    manifest = destination/'SHA256SUMS'
    require(manifest.is_file(),'安装包缺少文件摘要')
    verified = set()
    for line in manifest.read_text().splitlines():
        expected, name = line.split('  ',1)
        name = name.removeprefix('./')
        require(re.fullmatch('[0-9a-f]{64}',expected) and name in names and name not in verified,'文件摘要格式无效')
        path = destination/name
        require(path.is_file() and digest(path)==expected,'安装包文件摘要校验失败')
        verified.add(name)
    files={str(path.relative_to(destination)) for path in destination.rglob('*') if path.is_file()}
    require(files==verified|{'SHA256SUMS'},'安装包包含未校验文件')
    for asset in ('bin/control-api','bin/usage-migrate','bin/edge-agent','web-admin/index.html','deploy/update.py',
                  'deploy/panel-update-worker.py','deploy/systemd/hl-panel-update.socket','deploy/systemd/hl-panel-update.service'):
        require(asset in verified,'安装包缺少必需资产：'+asset)
    for path in destination.rglob('*'):
        path.chmod(0o755 if path.is_dir() or str(path.relative_to(destination)).startswith('bin/') else 0o644)
    # The updater's private umask must not make the release root inaccessible
    # to the unprivileged service and Nginx users.
    destination.chmod(0o755)


def switch(target):
    pending=ROOT/('.next-current-'+str(os.getpid()))
    require(not pending.exists() and not pending.is_symlink(),'更新链接已被占用')
    pending.symlink_to(target)
    pending.replace(ROOT/'current')


def subscription_nginx_update(destination, backup):
    """Add bounded managed locations; preserve custom domains/TLS/routes."""
    packaged = destination/'deploy/nginx/snippets/hl-panel-app-locations.conf'
    if not packaged.is_file():
        return
    packaged_text = packaged.read_text()
    private_regular(APP_LOCATIONS)
    current = APP_LOCATIONS.read_text()
    additions = []
    for location, path in [(SUBSCRIPTION_LOCATION, '/api/v1/public/subscriptions/'), (MIGRATION_LOCATION, '/api/v1/panel/migration/')]:
        if location in packaged_text and location not in current:
            require(path not in current, '自定义 Nginx 路由已存在；现有配置未覆盖，请核对：'+path)
            additions.append(location)
    if not additions:
        return
    proxy = None
    if MIGRATION_LOCATION in additions:
        require(not MIGRATION_PROXY.exists() and not MIGRATION_PROXY.is_symlink(), '迁移代理配置已被占用，现有配置未覆盖')
        private_regular(API_PROXY)
        proxy = API_PROXY.read_text()
        for directive in ('proxy_read_timeout', 'proxy_send_timeout'):
            proxy, count = re.subn(r'(?m)^\s*'+directive+r'\s+[^;]+;', directive+' 120s;', proxy)
            require(count == 1, '自定义 API 超时配置不兼容，现有配置未覆盖')
    saved = backup/'nginx-app-locations.conf'
    shutil.copyfile(APP_LOCATIONS, saved)
    pending = APP_LOCATIONS.with_name('.hl-panel-app-locations-'+str(os.getpid()))
    try:
        if proxy is not None:
            (backup/'migration-proxy-added').write_text('hl-panel-migration-proxy.conf\n')
            MIGRATION_PROXY.write_text(proxy)
            MIGRATION_PROXY.chmod(0o644)
        pending.write_text(current.rstrip()+'\n\n'+'\n\n'.join(additions)+'\n')
        pending.chmod(0o644)
        pending.replace(APP_LOCATIONS)
        run(['nginx','-t'])
        run(['systemctl','reload','nginx'])
    except Exception:
        shutil.copyfile(saved, APP_LOCATIONS)
        APP_LOCATIONS.chmod(0o644)
        if (backup/'migration-proxy-added').is_file() and MIGRATION_PROXY.exists():
            MIGRATION_PROXY.unlink()
        run(['nginx','-t'])
        run(['systemctl','reload','nginx'])
        raise
    finally:
        if pending.exists():
            pending.unlink()


def restore_subscription_nginx(backup):
    saved = backup/'nginx-app-locations.conf'
    if saved.is_file():
        private_regular(saved)
        shutil.copyfile(saved, APP_LOCATIONS)
        APP_LOCATIONS.chmod(0o644)
        if (backup/'migration-proxy-added').is_file() and MIGRATION_PROXY.exists():
            private_regular(MIGRATION_PROXY)
            MIGRATION_PROXY.unlink()
        run(['nginx','-t'])
        run(['systemctl','reload','nginx'])


def host_metrics_update(destination, backup):
    """A bounded drop-in preserves the administrator's existing service unit."""
    packaged = destination/'deploy/systemd/hl-panel-control-api.service'
    if not packaged.is_file() or '\nProcSubset=all\n' not in packaged.read_text():
        return
    require(not PROC_OVERRIDE.is_symlink(), '面板指标服务配置不能为链接')
    if PROC_OVERRIDE.exists():
        private_regular(PROC_OVERRIDE)
        existing = PROC_OVERRIDE.read_text()
        if existing == PROC_OVERRIDE_CONTENT:
            return
        require(existing == LEGACY_PROC_OVERRIDE_CONTENT,
                '面板指标服务配置路径已被自定义文件占用，现有文件未覆盖')
    PROC_OVERRIDE.parent.mkdir(mode=0o755, exist_ok=True)
    require(not PROC_OVERRIDE.parent.is_symlink(), '面板服务配置目录不能为链接')
    directory_info = PROC_OVERRIDE.parent.stat()
    require(directory_info.st_uid == os.geteuid() and not directory_info.st_mode & 0o022,
            '面板服务配置目录必须归更新用户所有且不能被其他用户修改')
    if PROC_OVERRIDE.exists():
        shutil.copyfile(PROC_OVERRIDE, backup/'host-metrics-previous.conf')
    else:
        (backup/'host-metrics-added').write_text('20-host-metrics.conf\n')
    PROC_OVERRIDE.write_text(PROC_OVERRIDE_CONTENT)
    PROC_OVERRIDE.chmod(0o644)
    run(['systemctl','daemon-reload'])


def migration_domain_access(backup):
    # domain.conf contains only public installation settings; allow the panel
    # service to read them without granting access to root secrets.
    path = CONFIG/'domain.conf'
    if path.exists():
        private_regular(path)
        allowed = {'DOMAIN', 'PUBLIC_IP', 'IP_HTTPS_PORT', 'CERTBOT_CERT_NAME'}
        lines = [line for line in path.read_text().splitlines() if line and not line.startswith('#')]
        require(all('=' in line and line.split('=', 1)[0] in allowed for line in lines),
                '域名配置包含未知字段，拒绝扩大读取权限')
        info = path.stat()
        (backup/'domain-access.json').write_text(json.dumps({'uid': info.st_uid, 'gid': info.st_gid, 'mode': info.st_mode & 0o777}))
        os.chown(path, 0, grp.getgrnam('hlpanel').gr_gid)
        path.chmod(0o640)


def restore_domain_access(backup):
    metadata = backup/'domain-access.json'
    if metadata.is_file():
        private_regular(metadata)
        original = json.loads(metadata.read_text())
        path = CONFIG/'domain.conf'
        private_regular(path)
        os.chown(path, original['uid'], original['gid'])
        path.chmod(original['mode'])


def restore_host_metrics(backup):
    previous = backup/'host-metrics-previous.conf'
    if previous.is_file():
        private_regular(previous)
        require(previous.read_text() == LEGACY_PROC_OVERRIDE_CONTENT, '面板指标配置备份内容无效')
        if PROC_OVERRIDE.exists():
            private_regular(PROC_OVERRIDE)
            require(PROC_OVERRIDE.read_text() == PROC_OVERRIDE_CONTENT, '面板指标配置已被修改，拒绝覆盖')
        shutil.copyfile(previous, PROC_OVERRIDE)
        PROC_OVERRIDE.chmod(0o644)
        run(['systemctl','daemon-reload'])
        return
    if (backup/'host-metrics-added').is_file():
        if PROC_OVERRIDE.exists():
            private_regular(PROC_OVERRIDE)
            require(PROC_OVERRIDE.read_text() == PROC_OVERRIDE_CONTENT, '面板指标配置已被修改，拒绝覆盖')
            PROC_OVERRIDE.unlink()
        run(['systemctl','daemon-reload'])


def web_update_channel(destination, backup):
    """Install only the fixed-purpose, locally permissioned update units."""
    sources = [destination/'deploy/systemd'/name for name in ('hl-panel-update.socket', 'hl-panel-update.service')]
    require(all(path.is_file() for path in sources), '发布包缺少网页更新服务')
    changes = []
    for source in sources:
        target = Path('/etc/systemd/system')/source.name
        if target.exists() or target.is_symlink():
            private_regular(target)
            require(target.read_text().startswith('# Managed by HL-panel:'), '网页更新服务路径已被其他配置占用，未覆盖')
            if target.read_bytes() == source.read_bytes():
                continue
            shutil.copyfile(target, backup/source.name)
            changes.append({'name': source.name, 'added': False})
        else:
            changes.append({'name': source.name, 'added': True})
        # Record intent before writing so a partial write can be undone.
        (backup/'web-update-units.json').write_text(json.dumps(changes))
        pending = target.with_name('.'+target.name+'-'+str(os.getpid()))
        try:
            shutil.copyfile(source, pending)
            pending.chmod(0o644)
            pending.replace(target)
        finally:
            if pending.exists():
                pending.unlink()
    run(['systemctl','daemon-reload'])


def restore_web_update_channel(backup):
    journal = backup/'web-update-units.json'
    if not journal.is_file():
        return
    changes = json.loads(journal.read_text())
    if any(change['added'] for change in changes):
        run(['systemctl','disable','--now','hl-panel-update.socket'])
        run(['systemctl','stop','hl-panel-update.service'])
    for change in changes:
        require(change['name'] in ('hl-panel-update.socket','hl-panel-update.service'), '网页更新恢复记录无效')
        target = Path('/etc/systemd/system')/change['name']
        if target.exists():
            private_regular(target)
        if change['added']:
            target.unlink(missing_ok=True)
        else:
            private_regular(backup/change['name'])
            shutil.copyfile(backup/change['name'], target)
            target.chmod(0o644)
    run(['systemctl','daemon-reload'])


def rollback(backup):
    # Finish restoring even when a second stop signal arrives during rollback.
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    backup=backup.resolve()
    require(backup.parent==Path('/var/backups/hl-panel') and backup.stat().st_uid==0 and not backup.stat().st_mode&0o077,'备份路径必须是 root 私有的 HL-panel 备份目录')
    private_regular(backup/'update-state.json')
    state=json.loads((backup/'update-state.json').read_text())
    old=Path(state['previous_target'])
    require(old.parent==ROOT/'releases' and (old/'bin/control-api').is_file(),'旧版本路径无效')
    database,origin=configuration()
    require(digest(backup/'database.dump')==state['database_sha256'],'数据库备份摘要不一致，拒绝恢复')
    run(['systemctl','stop',SERVICE])
    restore_subscription_nginx(backup)
    restore_host_metrics(backup)
    restore_web_update_channel(backup)
    restore_domain_access(backup)
    with (backup/'database.dump').open('rb') as stream:
        run(['runuser','-u','postgres','--','pg_restore','--clean','--if-exists','--exit-on-error','--dbname='+database],input_file=stream)
    # pg_restore --clean only removes objects present in the old archive.
    # Remove the journal added by an unsuccessful schema-18 upgrade, otherwise
    # retrying the upgrade would append the restored legacy history twice.
    manifest = run(['pg_restore','--list',str(backup/'database.dump')]).decode('utf-8')
    if not re.search(r'(?m)^\d+;\s+\d+\s+\d+\s+TABLE\s+public\s+hl_panel_audit_journal\s', manifest):
        run(['runuser','-u','postgres','--','psql','-X','-v','ON_ERROR_STOP=1','--dbname='+database,
             '-c','DROP TABLE IF EXISTS public.hl_panel_audit_journal'])
    switch(old)
    run(['systemctl','start',SERVICE])
    healthy(origin,state['previous_version'])
    print('[HL-panel 更新] 已恢复升级前程序和数据库。')


def main():
    parser=argparse.ArgumentParser(description='保留数据更新 HL-panel；不会重装或重置管理员密码')
    parser.add_argument('--version',default='latest')
    parser.add_argument('--check',action='store_true')
    parser.add_argument('--rollback',type=Path)
    args=parser.parse_args()
    require(os.geteuid()==0,'请使用 root 执行')
    require(platform.machine() in ('x86_64','amd64'),'自动更新仅支持 Linux amd64')
    os.umask(0o077)
    # One updater at a time, including a manual rollback.
    require(ROOT.is_dir() and not ROOT.is_symlink() and ROOT.stat().st_uid==0 and not ROOT.stat().st_mode&0o022,'未找到 root 管理的 HL-panel 程序目录')
    descriptor=os.open(ROOT/'.update.lock',os.O_WRONLY|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(descriptor,'a') as lock:
        try:
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:
            raise UpdateError('已有更新任务运行，请勿重复执行') from None
        if args.rollback:
            rollback(args.rollback)
            return
        require((ROOT/'current').is_symlink(),'未发现现有 HL-panel；请使用全新安装命令')
        previous=(ROOT/'current').resolve()
        require(previous.parent==ROOT/'releases','当前程序不在受管发布目录')
        database,origin=configuration()
        run(['systemctl','is-active','--quiet',SERVICE])
        old_version=current_info(origin)['platform_version']
        version_tuple(old_version)
        target=args.version
        if target=='latest':
            target=json.loads(request(API+'/releases/latest'))['tag_name']
        target_version=version_tuple(target)
        metadata=json.loads(request(API+'/releases/tags/'+target))
        require(metadata.get('tag_name')==target and not metadata.get('draft') and not metadata.get('prerelease') and metadata.get('published_at'),'目标不是 GitHub 正式发布')
        if args.check:
            print(json.dumps({'current_version':old_version,'target_version':target,'update_available':target_version>version_tuple(old_version)}))
            return
        if target_version==version_tuple(old_version):
            print('[HL-panel 更新] 已是目标正式版本，无需更新。')
            return
        require(target_version>version_tuple(old_version),'不允许降级；请使用对应备份中的回滚命令')
        updater=Path('/usr/local/sbin/hl-panel-update')
        if updater.exists() or updater.is_symlink():
            private_regular(updater)
            require('# Managed by HL-panel.' in updater.read_text(),'更新命令路径已被其他程序占用')
        stamp=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')+'-'+os.urandom(3).hex()
        backup=Path('/var/backups/hl-panel')/(target+'-'+stamp)
        backup.mkdir(parents=True,mode=0o700)
        backup.parent.chmod(0o700)
        print('[HL-panel 更新] 本次备份目录：'+str(backup),flush=True)
        archive=backup/'release.tar.gz'
        base=REPO+'/releases/download/'+target+'/'
        assets={item['name'] for item in metadata.get('assets',[])}
        require({'hl-panel-linux-amd64.tar.gz','hl-panel-linux-amd64.tar.gz.sha256'}<=assets,'发布包不完整')
        print('[HL-panel 更新] 下载同标签安装包，当前服务继续运行。',flush=True)
        archive.write_bytes(request(base+'hl-panel-linux-amd64.tar.gz',256*1024*1024))
        checksum=request(base+'hl-panel-linux-amd64.tar.gz.sha256',4096).decode().strip().split()
        require(len(checksum)==2 and checksum[1]=='hl-panel-linux-amd64.tar.gz' and re.fullmatch('[0-9a-f]{64}',checksum[0]),'发布包摘要格式无效')
        require(digest(archive)==checksum[0],'发布包 SHA256 校验失败')
        destination=ROOT/'releases'/(target+'-'+stamp)
        destination.mkdir(mode=0o755)
        extract_verified(archive,destination)
        # Domains, TLS and custom routes remain intact. A bounded subscription
        # location is added below, with its own backup and rollback.
        # Runtime/schema changes are handled by the verified new binaries.
        with tarfile.open(backup/'config-state.tar.gz','w:gz') as saved:
            saved.add(CONFIG,arcname='etc/hl-panel')
            if STATE.is_dir():
                saved.add(STATE,arcname='var/lib/hl-panel')
        stopped=False
        try:
            print('[HL-panel 更新] 暂停本面板，备份数据库；账号、规则和证书全部保留。',flush=True)
            run(['systemctl','stop',SERVICE])
            stopped=True
            with (backup/'database.dump').open('wb') as stream:
                run(['runuser','-u','postgres','--','pg_dump','-Fc','--dbname='+database],output=stream)
            require((backup/'database.dump').stat().st_size>0,'数据库备份为空')
            with (backup/'database-manifest.txt').open('wb') as stream:
                run(['pg_restore','--list',str(backup/'database.dump')],output=stream)
            state={'previous_target':str(previous),'previous_version':old_version,'target_version':target,
                   'database_sha256':digest(backup/'database.dump'),'config_sha256':digest(backup/'config-state.tar.gz'),
                   'release_sha256':checksum[0],'created_at_utc':datetime.now(timezone.utc).isoformat()}
            (backup/'update-state.json').write_text(json.dumps(state,indent=2)+'\n')
            shutil.copyfile(__file__,backup/'rollback.py')
            (backup/'rollback.sh').write_text('#!/usr/bin/env bash\nset -Eeuo pipefail\nexec python3 '+shlex.quote(str(backup/'rollback.py'))+' --rollback '+shlex.quote(str(backup))+'\n')
            (backup/'rollback.sh').chmod(0o700)
            print('[HL-panel 更新] 手动回滚命令：bash '+str(backup/'rollback.sh'),flush=True)
            run([str(destination/'bin/usage-migrate'),'apply','-dsn-file',str(CONFIG/'database-url')])
            run([str(destination/'bin/usage-migrate'),'verify','-dsn-file',str(CONFIG/'database-url')])
            subscription_nginx_update(destination, backup)
            host_metrics_update(destination, backup)
            migration_domain_access(backup)
            web_update_channel(destination, backup)
            switch(destination)
            run(['systemctl','enable','--now','hl-panel-update.socket'])
            run(['systemctl','start',SERVICE])
            healthy(origin,target)
        except Exception:
            if (backup/'update-state.json').is_file():
                print('[HL-panel 更新] 新版本未通过检查，正在恢复升级前程序与数据库。',flush=True)
                rollback(backup)
            elif stopped:
                run(['systemctl','start',SERVICE])
            raise
        updater.write_text(WRAPPER)
        updater.chmod(0o755)
        print('[HL-panel 更新] 成功：'+old_version+' → '+target)
        print('备份目录：'+str(backup))
        print('回滚命令：bash '+str(backup/'rollback.sh'))
        print('账号、已修改密码、规则、数据库、域名与证书均保留；请刷新面板。')


if __name__=='__main__':
    def interrupted(*_):
        raise UpdateError('更新被中断；已进入恢复流程，请核对备份与服务状态')
    signal.signal(signal.SIGTERM, interrupted)
    try:
        main()
    except UpdateError as error:
        print('[HL-panel 更新] 错误：'+str(error),file=sys.stderr)
        sys.exit(1)
    except Exception:
        print('[HL-panel 更新] 操作失败；未输出私有数据。请核对备份目录与面板服务状态。',file=sys.stderr)
        sys.exit(1)
