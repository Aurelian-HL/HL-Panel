#!/usr/bin/env bash
# Sourced by the installer only after the immutable release has been verified.
# Keep identity outside the installation rollback directory: successful binding
# must survive a later package/database installation failure.
license_state_permissions() {
  python3 - "$1" "$2" <<'PY'
import os, pathlib, pwd, stat, sys
directory, owner = pathlib.Path(sys.argv[1]), pwd.getpwnam(sys.argv[2])
if directory.is_symlink() or not directory.is_dir():
    raise SystemExit('授权目录必须是普通目录')
paths = list(directory.iterdir())
allowed = {'instance-id', 'instance-key', 'state.json', 'identity.json'}
for path in paths:
    info = path.lstat()
    if path.name not in allowed or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise SystemExit('授权目录存在非受管文件或链接，请保留目录并人工检查')
for path in paths:
    os.chown(path, owner.pw_uid, owner.pw_gid, follow_symlinks=False)
    path.chmod(0o600)
os.chown(directory, owner.pw_uid, owner.pw_gid, follow_symlinks=False)
directory.chmod(0o700)
PY
}

activate_panel_license() {
  local binary="$1" domain="$2" state_dir="$3" code_file="${4:-}" trust status challenge answer
  domain="${domain,,}"
  LICENSE_DOMAIN="$domain"
  [[ ! -L "$state_dir" ]] || fail "授权状态目录不能是符号链接"
  if [[ -d "$state_dir" ]]; then license_state_permissions "$state_dir" root; fi
  install -d -o root -g root -m 0700 "$state_dir"
  trust="$("$binary" license trust)" || fail "发行包未配置 HT 可信授权服务，不能安装；请使用新的授权发行包"
  LICENSE_ISSUER_URL="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["issuer_url"])' <<<"$trust")"
  # Preserve an existing identity before generating files or contacting issuer.
  python3 - "$state_dir" "$domain" <<'PY' || fail "现有授权身份与本次域名或实例不一致，文件未覆盖；请核对参数，换绑须先在 HT 显式处理"
import json, pathlib, sys
directory, domain = pathlib.Path(sys.argv[1]), sys.argv[2]
identity = directory/'identity.json'
if identity.exists():
    try:
        if identity.stat().st_size > 2048:
            raise ValueError('oversized identity')
        stored = json.loads(identity.read_text(encoding='utf-8'))
        instance = (directory/'instance-id').read_text(encoding='ascii').strip()
        if (not isinstance(stored, dict) or stored.get('domain') != domain
                or stored.get('instance_id') != instance):
            raise ValueError('identity mismatch')
    except (OSError, ValueError, UnicodeError):
        raise SystemExit(1) from None
PY
  if [[ -f "$state_dir/instance-id" ]]; then
    LICENSE_INSTANCE_ID="$(cat "$state_dir/instance-id")"
  else
    LICENSE_INSTANCE_ID="hlp-$(openssl rand -hex 16)"
    (umask 077; printf '%s\n' "$LICENSE_INSTANCE_ID" > "$state_dir/instance-id")
  fi
  [[ "$LICENSE_INSTANCE_ID" =~ ^[A-Za-z0-9._:-]{8,128}$ ]] || fail "本机授权实例编号无效，请检查授权状态目录"
  local args=(--domain "$domain" --instance "$LICENSE_INSTANCE_ID" --state-dir "$state_dir")
  python3 - "$state_dir" "$domain" "$LICENSE_INSTANCE_ID" <<'PY'
import json, os, pathlib, sys, tempfile
directory, domain, instance = sys.argv[1:]
if (pathlib.Path(directory)/'identity.json').exists():
    raise SystemExit(0)  # Already checked above; do not rewrite a bound identity.
with tempfile.NamedTemporaryFile(mode='w', dir=directory, prefix='.identity-', delete=False) as pending:
    json.dump({'domain':domain, 'instance_id':instance}, pending)
    pending.flush()
    os.fsync(pending.fileno())
os.replace(pending.name, pathlib.Path(directory)/'identity.json')
PY
  "$binary" license sync "${args[@]}" >/dev/null 2>&1 || true
  status="$("$binary" license status "${args[@]}")" || fail "本机授权缓存校验失败，请保留状态目录并检查文件权限"
  if python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("management_allowed") else 1)' <<<"$status"; then
    log "已验证本机现有授权，继续安装"
    return 0
  fi
  if python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("license_id") else 1)' <<<"$status"; then
    fail "本机已有绑定但授权暂不可用。请在 HT 续期或恢复网络后重跑安装；无需重新占用授权码，请勿删除 $state_dir"
  fi
  printf '\n本项目仅供个人合法用途，实验性功能请勿用于关键生产环境。\n一码仅限一个管理域名和实例，禁止转售、分享授权码或复制实例密钥。\n面板会在本机保存账号、规则和统计；授权服务只接收授权和必要审计信息，不接收客户转发内容。\n'
  local license_code=""
  if [[ -n "$code_file" ]]; then
    [[ -f "$code_file" && ! -L "$code_file" && -r "$code_file" ]] || fail "授权码文件不存在、不可读或不是普通文件"
    [[ "$(stat -c %u -- "$code_file")" == 0 && $((8#$(stat -c %a -- "$code_file") & 077)) == 0 ]] || fail "授权码文件必须由 root 持有，权限为 600 或更严格"
    (($(stat -c %s -- "$code_file") <= 514)) || fail "授权码文件过大"
    license_code="$(cat -- "$code_file")"
  else
    [[ -r /dev/tty && -w /dev/tty ]] || fail "需要授权码；请在终端隐藏输入，或提供 --license-code-file /root/授权码文件"
    read -r -s -p "请输入 HT 授权码（隐藏输入）：" license_code </dev/tty
    printf '\n' >/dev/tty
  fi
  challenge="$(printf '%s' "$license_code" | "$binary" license prepare "${args[@]}")" || fail "获取授权域名证明失败；未永久绑定授权码，可以重试"
  unset license_code
  python3 -c 'import json,sys; c=json.load(sys.stdin); print("请添加 DNS TXT：\n记录名："+c["dns_name"]+"\n记录值："+c["dns_value"]+"\n证明到期："+c["expires_at"])' <<<"$challenge"
  while true; do
    [[ -r /dev/tty && -w /dev/tty ]] || fail "DNS 证明等待验证。授权实例已保存在 $state_dir；解析生效后在交互终端重跑安装，不要删除该目录"
    read -r -p "DNS 解析生效后回车验证，输入 q 退出：" answer </dev/tty
    [[ "$answer" != q ]] || fail "已暂停安装，保留授权实例供下次继续"
    if status="$("$binary" license commit "${args[@]}")"; then break; fi
    log "验证未完成；检查 TXT 值和解析传播，证明过期时请重新运行安装"
  done
  python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("management_allowed") else 1)' <<<"$status" || fail "授权尚未有效，请在 HT 续期后重新安装"
}
