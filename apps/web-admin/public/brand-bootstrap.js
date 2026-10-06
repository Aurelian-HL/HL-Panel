try {
  const brand = JSON.parse(sessionStorage.getItem('hl_panel_brand_v1') || 'null')
  if (typeof brand?.panel_title === 'string' && brand.panel_title.trim()) {
    document.title = brand.panel_title.trim()
  }
} catch {
  // The public site settings request supplies the title when storage is unavailable.
}
