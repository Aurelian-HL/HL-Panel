package subscriptions

import (
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

type packageEntry struct {
	name string
	data []byte
}

const offlineInstructions = "HL-panel 离线节点导入\r\n" +
	"本文件夹中的二维码直接包含节点配置，不是订阅地址。扫码导入不需要访问面板或下载在线订阅。\r\n" +
	"小火箭：解压后将 PNG 保存到相册，在小火箭扫码入口选择相册图片；也可以在另一块屏幕打开图片，用小火箭扫描。\r\n" +
	"v2rayN：使用客户端的扫描二维码功能；也可复制对应单节点链接.txt 中的完整链接，从剪贴板导入。\r\n" +
	"每条线路单独一个二维码，昵称使用规则名。多条线路请逐个导入，不要用系统相机当作订阅地址打开。\r\n" +
	"二维码和单节点链接都是下载时的快照，无法自动更新。规则名、入口域名或凭据改变后，请重新下载本包并重新导入。\r\n" +
	"若链接超出二维码容量，将保留完整单节点链接并附二维码说明，请使用剪贴板导入。\r\n" +
	"离线导入只省去订阅下载；连接节点仍需要网络，并仍受规则状态、流量额度和节点协议约束。\r\n" +
	"这不会修正设备时间，也不会关闭 TLS / Reality 的安全验证。连接时仍提示时间或证书错误，请开启系统自动日期与时间。\r\n" +
	"二维码和链接包含节点访问凭据，请勿公开或发给无关人员。\r\n"

// Offline exports use the same eligible lines as the online feed. They encode
// a native node URI, never a panel address or a subscription token.
func offlinePackageEntries(lines []Resolved) ([]packageEntry, error) {
	entries := []packageEntry{{"离线二维码/使用说明.txt", append([]byte{0xef, 0xbb, 0xbf}, []byte(offlineInstructions)...)}}
	index := 0
	for _, line := range lines {
		if line.URI == "" {
			continue
		}
		proxy, err := ParseProxy(line.URI, line.Name)
		if err != nil {
			return nil, err
		}
		index++
		base := fmt.Sprintf("离线二维码/%03d-%s", index, PackageBaseName(line.Name))
		entries = append(entries, packageEntry{base + "-单节点链接.txt", []byte(proxy.URI + "\r\n")})
		code, err := qrcode.New(proxy.URI, qrcode.Medium)
		if err != nil {
			// Valid long URIs must remain importable even beyond QR capacity.
			entries = append(entries, packageEntry{base + "-二维码说明.txt", append([]byte{0xef, 0xbb, 0xbf}, []byte("节点链接超出单个二维码容量，完整配置已保留在同名的单节点链接.txt 中。请复制完整链接并在客户端从剪贴板导入。\r\n")...)})
			continue
		}
		// Integer module scaling and the default quiet zone keep dense codes
		// readable, without reducing the configuration or rule name.
		modules := len(code.Bitmap())
		scale := 6
		if modules*scale < 384 {
			scale = (384 + modules - 1) / modules
		}
		png, err := code.PNG(modules * scale)
		if err != nil {
			return nil, err
		}
		entries = append(entries, packageEntry{base + "-节点二维码.png", png})
	}
	return entries, nil
}
