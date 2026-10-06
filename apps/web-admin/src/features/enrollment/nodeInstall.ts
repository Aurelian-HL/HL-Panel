export const nodeRepository = 'https://github.com/Aurelian-HL/HL-Panel'

export function nodeInstallCommand(origin: string, token: string): string {
  const url = new URL(origin)
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || (url.pathname !== '/' && url.pathname !== '') || !/^[a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]{1,5})?$/.test(url.host)) {
    throw new Error('请通过 HTTPS 域名或 IPv4 地址打开面板后生成安装命令；节点需要信任面板证书。')
  }
  if (!/^[A-Za-z0-9_-]{8,256}$/.test(token)) throw new Error('注册令牌格式无效，请重新生成')
  return `(set +x; set -e; f=$(mktemp); trap 'rm -f -- "$f"' EXIT; curl -fSL --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 60 https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/install-node.sh -o "$f"; bash "$f" --panel-url '${url.origin}' --token '${token}')`
}
