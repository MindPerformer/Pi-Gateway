/** Copy during the click handler, including LAN HTTP where Clipboard API is absent. */
export async function copyText(text: string): Promise<void> {
    if (navigator.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text)
            return
        } catch { /* Try the browser's legacy copy path. */
        }
    }
    const active = document.activeElement as HTMLElement | null
    const selection = window.getSelection()
    const ranges = selection ? Array.from({length: selection.rangeCount}, (_, i) => selection.getRangeAt(i).cloneRange()) : []
    const field = document.createElement('textarea')
    field.value = text
    field.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0'
    field.setAttribute('readonly', '')
    document.body.appendChild(field)
    try {
        field.select()
        if (!document.execCommand('copy')) throw new Error('Clipboard copy was blocked')
    } finally {
        field.remove()
        active?.focus({preventScroll: true})
        selection?.removeAllRanges()
        for (const range of ranges) selection?.addRange(range)
    }
}
