<template>
  <canvas ref="canvasRef" class="particle-bg"></canvas>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'

const canvasRef = ref(null)
let ctx = null
let raf = null
let particles = []
let lines = []
let w = 0
let h = 0
let dpr = 1

const PARTICLE_COUNT = 80
const MAX_DIST = 140

function initParticles() {
  particles = []
  for (let i = 0; i < PARTICLE_COUNT; i++) {
    particles.push({
      x: Math.random() * w,
      y: Math.random() * h,
      vx: (Math.random() - 0.5) * 0.35,
      vy: (Math.random() - 0.5) * 0.35,
      r: Math.random() * 1.6 + 0.4,
      a: Math.random() * 0.5 + 0.2,
    })
  }
}

function resize() {
  dpr = window.devicePixelRatio || 1
  const parent = canvasRef.value.parentElement
  w = parent ? parent.clientWidth : window.innerWidth
  h = parent ? parent.clientHeight : window.innerHeight
  canvasRef.value.width = w * dpr
  canvasRef.value.height = h * dpr
  canvasRef.value.style.width = w + 'px'
  canvasRef.value.style.height = h + 'px'
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  refreshAccent()
  if (particles.length === 0) initParticles()
}

let accentRGB = { r: 56, g: 189, b: 248 }

function refreshAccent() {
  const el = canvasRef.value?.parentElement || document.body
  const color = getComputedStyle(el).getPropertyValue('--accent').trim() || '#38bdf8'
  // hex / rgb / named -> rgb
  const div = document.createElement('div')
  div.style.color = color
  div.style.display = 'none'
  document.body.appendChild(div)
  const rgbStr = getComputedStyle(div).color
  document.body.removeChild(div)
  const m = rgbStr.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/)
  if (m) accentRGB = { r: +m[1], g: +m[2], b: +m[3] }
}

function draw() {
  ctx.clearRect(0, 0, w, h)

  // 粒子运动
  for (const p of particles) {
    p.x += p.vx
    p.y += p.vy
    if (p.x < 0) p.x = w
    if (p.x > w) p.x = 0
    if (p.y < 0) p.y = h
    if (p.y > h) p.y = 0
  }

  // 连线
  lines = []
  for (let i = 0; i < particles.length; i++) {
    for (let j = i + 1; j < particles.length; j++) {
      const dx = particles[i].x - particles[j].x
      const dy = particles[i].y - particles[j].y
      const dist = Math.sqrt(dx * dx + dy * dy)
      if (dist < MAX_DIST) {
        const alpha = (1 - dist / MAX_DIST) * 0.18
        lines.push({ i, j, alpha })
      }
    }
  }

  const { r, g, b } = accentRGB

  // 绘制连线
  for (const l of lines) {
    const p1 = particles[l.i]
    const p2 = particles[l.j]
    ctx.strokeStyle = `rgba(${r},${g},${b},${l.alpha})`
    ctx.lineWidth = 0.6
    ctx.beginPath()
    ctx.moveTo(p1.x, p1.y)
    ctx.lineTo(p2.x, p2.y)
    ctx.stroke()
  }

  // 绘制粒子
  for (const p of particles) {
    ctx.fillStyle = `rgba(${r},${g},${b},${p.a})`
    ctx.beginPath()
    ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2)
    ctx.fill()
  }

  raf = requestAnimationFrame(draw)
}

let ro = null
onMounted(() => {
  ctx = canvasRef.value.getContext('2d')
  resize()
  draw()
  ro = new ResizeObserver(resize)
  ro.observe(canvasRef.value.parentElement)
  window.addEventListener('resize', resize)
})
onUnmounted(() => {
  raf && cancelAnimationFrame(raf)
  ro && ro.disconnect()
  window.removeEventListener('resize', resize)
})
</script>

<style scoped>
.particle-bg {
  position: absolute;
  inset: 0;
  z-index: 1;
  pointer-events: none;
}
</style>
