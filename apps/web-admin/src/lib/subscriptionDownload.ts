export function downloadSubscriptionPackage(pack: { filename: string; data_base64: string }): void {
 const bytes = Uint8Array.from(atob(pack.data_base64), c => c.charCodeAt(0))
 const url = URL.createObjectURL(new Blob([bytes], { type: 'application/zip' }))
 const anchor = document.createElement('a')
 anchor.href = url; anchor.download = pack.filename; anchor.hidden = true
 document.body.append(anchor); anchor.click(); anchor.remove()
 setTimeout(() => URL.revokeObjectURL(url), 1000)
}
