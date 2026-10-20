// The background: green rain falling from the top of the page to the bottom.
//
// Kept faint on purpose, since the cards sit on top of it and must stay easy
// to read. Drops come in three depths: far ones are thinner, slower and paler,
// which is what makes a flat canvas read as rain rather than as stripes. One
// drop in thirty is a small ring, the coin from the icon.
//
// It always runs. On this site motion is how a page shows it is live, so the
// OS reduced-motion setting does not stop it (CLAUDE.md, Motion).

interface Drop {
  x: number
  y: number
  len: number
  speed: number // px per second
  alpha: number
  width: number
  coin: boolean
}

const DEPTHS = [
  {speed: [70, 110], len: [30, 60], alpha: [0.06, 0.11], width: 1},
  {speed: [130, 190], len: [50, 90], alpha: [0.1, 0.17], width: 1.2},
  {speed: [220, 300], len: [80, 130], alpha: [0.15, 0.25], width: 1.6},
] as const

const between = (r: readonly [number, number]) => r[0] + Math.random() * (r[1] - r[0])

export function startRain(canvas: HTMLCanvasElement) {
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  let w = 0
  let h = 0
  let drops: Drop[] = []

  const spawn = (anywhere: boolean): Drop => {
    const d = DEPTHS[Math.random() < 0.5 ? 0 : Math.random() < 0.7 ? 1 : 2]
    const len = between(d.len)
    return {
      x: Math.random() * w,
      y: anywhere ? Math.random() * h : -len - Math.random() * h * 0.3,
      len,
      speed: between(d.speed),
      alpha: between(d.alpha),
      width: d.width,
      coin: Math.random() < 1 / 30,
    }
  }

  const resize = () => {
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    w = window.innerWidth
    h = window.innerHeight
    canvas.width = Math.round(w * dpr)
    canvas.height = Math.round(h * dpr)
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    // About one drop per 18 px of width: sparse enough to stay background.
    const want = Math.round(w / 18)
    while (drops.length < want) drops.push(spawn(true))
    drops.length = want
  }
  resize()
  window.addEventListener('resize', resize)

  // The colour follows the theme: the brand green, fainter on a light page.
  const palette = () => {
    const light = document.documentElement.getAttribute('data-theme') === 'light'
    return {rgb: light ? '0, 128, 58' : '0, 213, 87', k: light ? 0.7 : 1}
  }

  // The pointer's glow: nothing is drawn for it, but drops passing within
  // GLOW px of it light up, most at the centre. It fades in when the pointer
  // arrives and out when it leaves the page, rather than switching.
  const GLOW = 170
  let mx = -1e4
  let my = -1e4
  let present = false
  let strength = 0
  window.addEventListener('pointermove', (e) => {
    mx = e.clientX
    my = e.clientY
    present = true
  })
  document.documentElement.addEventListener('pointerleave', () => (present = false))
  window.addEventListener('blur', () => (present = false))

  // lit is how much a drop is lit, 0 to 1: by its nearest point to the
  // pointer, so a long streak brightens as soon as any of it passes close.
  const lit = (x: number, top: number, bottom: number) => {
    if (strength === 0) return 0
    const dx = x - mx
    if (Math.abs(dx) > GLOW) return 0
    const dy = my < top ? top - my : my > bottom ? my - bottom : 0
    const dist = Math.hypot(dx, dy)
    if (dist >= GLOW) return 0
    const f = 1 - dist / GLOW
    return f * f * strength
  }

  let last = performance.now()
  const frame = (now: number) => {
    // A tab in the background gets no frames; cap the step so the rain does
    // not jump a whole screen when it comes back.
    const dt = Math.min((now - last) / 1000, 0.05)
    last = now
    strength += ((present ? 1 : 0) - strength) * Math.min(1, dt * 6)
    if (strength < 0.001) strength = 0
    const {rgb, k} = palette()
    ctx.clearRect(0, 0, w, h)
    ctx.lineCap = 'round'
    for (let i = 0; i < drops.length; i++) {
      const d = drops[i]
      d.y += d.speed * dt
      if (d.y - d.len > h) {
        drops[i] = spawn(false)
        continue
      }
      // Near the pointer a drop brightens up to about four times, and
      // thickens a touch, so the glow reads as light rather than as a shape.
      const l = lit(d.x, d.y - d.len, d.y)
      const a = Math.min(0.9, d.alpha * k * (1 + 3.2 * l))
      if (d.coin) {
        ctx.strokeStyle = `rgba(${rgb}, ${Math.min(0.95, a * 1.6)})`
        ctx.lineWidth = 1.4 + l
        ctx.beginPath()
        ctx.arc(d.x, d.y, 3.2, 0, Math.PI * 2)
        ctx.stroke()
        // A short trail above it, where it just was.
        ctx.strokeStyle = `rgba(${rgb}, ${a * 0.8})`
        ctx.beginPath()
        ctx.moveTo(d.x, d.y - 9)
        ctx.lineTo(d.x, d.y - 9 - d.len * 0.25)
        ctx.stroke()
        continue
      }
      // Faint at the tail, brightest at the head.
      const g = ctx.createLinearGradient(d.x, d.y - d.len, d.x, d.y)
      g.addColorStop(0, `rgba(${rgb}, 0)`)
      g.addColorStop(1, `rgba(${rgb}, ${a})`)
      ctx.strokeStyle = g
      ctx.lineWidth = d.width + 0.8 * l
      ctx.beginPath()
      ctx.moveTo(d.x, d.y - d.len)
      ctx.lineTo(d.x, d.y)
      ctx.stroke()
    }
    requestAnimationFrame(frame)
  }
  requestAnimationFrame(frame)
}
