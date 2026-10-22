import {expect, test} from './admin.fixture'

test('減少動態偏好覆蓋 inline 動畫而保留完成事件與正常模式', async ({page, admin}) => {
    // Request the fixture so all API traffic and page errors remain checked.
    expect(admin.captures.length).toBeGreaterThan(0)
    await page.emulateMedia({reducedMotion: 'reduce'})
    await page.goto('/captures')
    await page.evaluate(() => {
        const styles = document.createElement('style')
        styles.textContent = '@keyframes motion-contract { from { opacity: .9; } to { opacity: 1; } }'
        document.head.append(styles)
        const probe = document.createElement('div')
        probe.id = 'reduced-motion-contract'
        probe.textContent = 'Motion contract'
        // Inline normal declarations have higher priority than any normal
        // stylesheet/layer rule. The accessibility override must still win.
        Object.assign(probe.style, {
            animationName: 'motion-contract', animationDuration: '2s', animationIterationCount: 'infinite',
            transitionProperty: 'transform', transitionDuration: '3s', scrollBehavior: 'smooth',
        })
        probe.addEventListener('animationend', () => { probe.dataset.completed = 'yes' }, {once: true})
        document.body.append(probe)
    })
    const probe = page.locator('#reduced-motion-contract')
    const reduced = await probe.evaluate(element => {
        const style = getComputedStyle(element)
        return {animation: style.animationDuration, transition: style.transitionDuration,
            iterations: style.animationIterationCount, scrolling: style.scrollBehavior}
    })
    expect(Number.parseFloat(reduced.animation)).toBeGreaterThan(0)
    expect(Number.parseFloat(reduced.animation)).toBeLessThan(0.001)
    expect(Number.parseFloat(reduced.transition)).toBeGreaterThan(0)
    expect(Number.parseFloat(reduced.transition)).toBeLessThan(0.001)
    expect(reduced.iterations).toBe('1')
    expect(reduced.scrolling).toBe('auto')
    // Disabling animation entirely would silently drop completion callbacks.
    await expect(probe).toHaveAttribute('data-completed', 'yes')
    await page.emulateMedia({reducedMotion: 'no-preference'})
    await expect(probe).toHaveCSS('animation-duration', '2s')
    await expect(probe).toHaveCSS('animation-iteration-count', 'infinite')
    await expect(probe).toHaveCSS('transition-duration', '3s')
    await expect(probe).toHaveCSS('scroll-behavior', 'smooth')
})
