'use strict';

const $ = (id) => document.getElementById(id);
const SEA_LEVEL = 2;
const MAX_WATCHES = 4;
const WATCH_COLORS = ['#ffd34d', '#ff7ab6', '#7cf0a8', '#c9a2ff'];

const PRESETS = {
  quick: { width: 200, length: 200, seed: '', hills: 120, hillWide: 40, ridges: 5, ridgeLen: 20, ridgeWide: 25, stuck: 6, petalShape: 2, petalNum: 3, dropNum: 20000, times: 50, enlarge: 0 },
  legacy: { width: 400, length: 400, seed: 1679292301, hills: 250, hillWide: 80, ridges: 5, ridgeLen: 20, ridgeWide: 50, stuck: 6, petalShape: 2, petalNum: 3, dropNum: 50000, times: 50, enlarge: 1 },
  island: { width: 300, length: 300, seed: '', hills: 300, hillWide: 35, ridges: 3, ridgeLen: 15, ridgeWide: 30, stuck: 10, petalShape: 3, petalNum: 4, dropNum: 40000, times: 60, enlarge: 0 },
};

// 与 engine.DefaultColors 相同的分层设色带
const STOPS = [
  [0.00, 0x2a, 0x5d, 0x8f], [0.01, 0x8c, 0xc4, 0xa0], [0.20, 0x5f, 0x9e, 0x5a], [0.42, 0xa7, 0xb8, 0x66],
  [0.58, 0xd9, 0xc2, 0x7a], [0.74, 0xb9, 0x8a, 0x55], [0.88, 0x8a, 0x6a, 0x52], [1.00, 0xf2, 0xf2, 0xf0],
];
const LUT_N = 512;
const LUT = (() => {
  const lut = new Uint8Array(LUT_N * 3);
  for (let i = 0; i < LUT_N; i++) {
    const v = i / (LUT_N - 1);
    let k = 1;
    while (k < STOPS.length - 1 && v > STOPS[k][0]) k++;
    const a = STOPS[k - 1], b = STOPS[k];
    const t = b[0] > a[0] ? Math.min(1, Math.max(0, (v - a[0]) / (b[0] - a[0]))) : 0;
    for (let c = 0; c < 3; c++) lut[i * 3 + c] = Math.round(a[c + 1] + (b[c + 1] - a[c + 1]) * t);
  }
  return lut;
})();
const SEA = [0x2a, 0x5d, 0x8f];
const RIVER = [0x40, 0x72, 0xcb];
const ERODE = [0xc4, 0x53, 0x2e];
const DEPO = [0x2f, 0x6f, 0xb0];
const FLOW_RAMP = [[18, 26, 34], [40, 110, 200], [120, 220, 240], [255, 255, 255]];

const canvas = $('mapCanvas'); // 叠加层；2D 回退时也画地图
const ctx = canvas.getContext('2d');
const glCanvas = $('glCanvas');
const viewport = $('viewport');

const app = {
  id: null,
  ws: null,
  state: null,
  speed: 5,
  layer: 'terrain',
  flowOverlay: true,
  map: null,
  view: { scale: 1, ox: 0, oy: 0, auto: true },
  hover: null,
  watches: [], // { x, y, color, hist: [{tick, t, c, q}] }
  focus: null, // 观察列表中当前选中的像素
  detail: null,
  stats: null,
  history: [],
  queue: Promise.resolve(),
  drawPending: false,
  gl: null,
  owner: true, // false 表示通过分享链接只读查看
  request: null, // 创建参数，用于生成复现链接
};

/* ---------- 渲染器 ---------- */

const forced2D = new URLSearchParams(location.search).get('renderer') === '2d';
app.gl = forced2D ? null : createGLRenderer(glCanvas, LUT);
$('rendererLabel').textContent = app.gl ? 'WebGL2' : 'Canvas 2D';
glCanvas.hidden = !app.gl;
if (app.gl) app.gl.onRestore = () => { if (app.map) app.gl.upload(app.map); requestDraw(); };

/* ---------- 解码 ---------- */

async function gunzip(bytes) {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
}

async function onBinary(buf) {
  const dv = new DataView(buf);
  const kind = dv.getUint8(0);
  const tick = dv.getUint32(1, true);
  if (kind === 1) {
    const w = dv.getUint16(5, true), h = dv.getUint16(7, true);
    const raw = await gunzip(new Uint8Array(buf, 9));
    const n = w * h;
    const old = app.map;
    const map = {
      w, h, tick,
      q: new Int32Array(raw, 0, n),
      t: new Int16Array(raw, n * 4, n),
      t0: new Int16Array(raw, n * 6, n),
      c: new Int16Array(raw, n * 8, n),
    };
    let scaleMax = 2, maxQ = 1, maxC = 1;
    for (let i = 0; i < n; i++) {
      if (map.t0[i] > scaleMax) scaleMax = map.t0[i];
      if (map.q[i] > maxQ) maxQ = map.q[i];
      if (map.c[i] > maxC) maxC = map.c[i];
    }
    Object.assign(map, { scaleMax, maxQ, maxC });
    updateErosionScale(map);
    if (old && old.w && w !== old.w) remapPixels(w / old.w);
    app.map = map;
    if (app.gl) {
      app.gl.upload(map);
    } else {
      map.canvas = document.createElement('canvas');
      map.canvas.width = w; map.canvas.height = h;
      map.ctx = map.canvas.getContext('2d');
      map.img = map.ctx.createImageData(w, h);
      paintRect(0, 0, w, h);
    }
    $('empty').hidden = true;
    if (!old || old.w !== w || old.h !== h) fitView();
    updateLegend();
    updateControls();
  } else if (kind === 2) {
    const map = app.map;
    if (!map) { send({ op: 'full' }); return; }
    const count = dv.getUint16(5, true);
    const raw = await gunzip(new Uint8Array(buf, 7));
    let off = 0;
    for (let k = 0; k < count; k++) {
      const [x0, y0, tw, th] = new Uint16Array(raw, off, 4);
      const a = tw * th;
      if (x0 + tw > map.w || y0 + th > map.h) { send({ op: 'full' }); return; }
      const q = new Int32Array(raw, off + 8, a);
      const t = new Int16Array(raw, off + 8 + a * 4, a);
      const c = new Int16Array(raw, off + 8 + a * 6, a);
      for (let yy = 0; yy < th; yy++) {
        const dst = (y0 + yy) * map.w + x0, src = yy * tw;
        map.q.set(q.subarray(src, src + tw), dst);
        map.t.set(t.subarray(src, src + tw), dst);
        map.c.set(c.subarray(src, src + tw), dst);
      }
      for (let i = 0; i < a; i++) {
        if (q[i] > map.maxQ) map.maxQ = q[i];
        if (c[i] > map.maxC) map.maxC = c[i];
      }
      if (app.gl) app.gl.updateTile(x0, y0, tw, th, { q, t, c });
      // 坡度阴影依赖左、上邻居，右下各多画一格
      else paintRect(x0, y0, Math.min(tw + 1, map.w - x0), Math.min(th + 1, map.h - y0));
      off += 8 + a * 8;
    }
    map.tick = tick;
  }
  if (app.hover) showHover(app.hover.x, app.hover.y, app.hover.cx, app.hover.cy);
  renderInspector();
  renderWatches();
  requestDraw();
}

// 地图放大后像素坐标按倍数换算，服务端对观察列表做同样的换算。
// 放大时高度也整体乘以同一倍数，把已有历史换算到新尺度，曲线才连续
function remapPixels(f) {
  for (const w of app.watches) {
    w.x *= f; w.y *= f;
    for (const p of w.hist) p.t *= f;
  }
  if (app.focus) { app.focus.x *= f; app.focus.y *= f; }
  app.detail = null;
  app.hover = null;
  $('tooltip').hidden = true;
}

/* ---------- Canvas 2D 回退着色(与 render-gl.js 的着色器一致) ---------- */

function mix(out, o, a, b, f) {
  out[o] = a[0] + (b[0] - a[0]) * f;
  out[o + 1] = a[1] + (b[1] - a[1]) * f;
  out[o + 2] = a[2] + (b[2] - a[2]) * f;
}

function paintRect(x0, y0, rw, rh) {
  const m = app.map;
  const { w, t, t0, q, c, scaleMax, maxC, erodeCap, depoCap } = m;
  const d = m.img.data;
  const shadeK = 30 / scaleMax;
  const logMaxQ = Math.log(m.maxQ + 1);
  const tmp = [0, 0, 0];
  const layer = app.layer;
  for (let y = y0; y < y0 + rh; y++) {
    for (let x = x0; x < x0 + rw; x++) {
      const i = y * w + x, o = i * 4;
      const h = t[i];
      const left = x > 0 ? t[i - 1] : h, up = y > 0 ? t[i - w] : h;
      const shade = Math.min(1.25, Math.max(0.7, 1 + ((h - left) + (h - up)) * shadeK));
      const sea = h < SEA_LEVEL;
      let r, g, b;
      if (layer === 'terrain' || layer === 'water') {
        if (sea) { [r, g, b] = SEA; }
        else {
          const li = Math.min(LUT_N - 1, Math.floor(h / scaleMax * (LUT_N - 1))) * 3;
          r = LUT[li] * shade; g = LUT[li + 1] * shade; b = LUT[li + 2] * shade;
        }
        if (layer === 'water') {
          r = g = b = (r + g + b) / 3 * 0.55 + 90;
          if (c[i] > 0 && !sea) {
            tmp[0] = r; tmp[1] = g; tmp[2] = b;
            mix(tmp, 0, tmp, [40, 150, 255], Math.min(1, 0.25 + 0.75 * Math.sqrt(c[i] / maxC)));
            [r, g, b] = tmp;
          }
        } else if (app.flowOverlay && q[i] > 0 && !sea) {
          const f = Math.log(q[i] + 1) / logMaxQ;
          tmp[0] = r; tmp[1] = g; tmp[2] = b;
          mix(tmp, 0, tmp, RIVER, Math.min(0.9, f * f * 1.1));
          [r, g, b] = tmp;
        }
      } else if (layer === 'erosion') {
        const grey = sea ? 70 : 215 - 90 * (h / scaleMax);
        const base = [grey * shade, grey * shade, grey * shade];
        const diff = t0[i] - h;
        const f = diff >= 0 ? Math.min(1, diff / erodeCap) : -Math.min(1, -diff / depoCap);
        if (f > 0) mix(tmp, 0, base, ERODE, Math.pow(f, 0.7));
        else if (f < 0) mix(tmp, 0, base, DEPO, Math.pow(-f, 0.7));
        else { tmp[0] = base[0]; tmp[1] = base[1]; tmp[2] = base[2]; }
        [r, g, b] = tmp;
      } else { // flow
        let f = q[i] > 0 ? Math.log(q[i] + 1) / logMaxQ : 0;
        if (f <= 0) {
          const v = sea ? 10 : 22 * shade;
          r = v; g = v + 4; b = v + 8;
        } else {
          f = Math.min(0.999, f) * (FLOW_RAMP.length - 1);
          const k = Math.floor(f);
          mix(tmp, 0, FLOW_RAMP[k], FLOW_RAMP[k + 1], f - k);
          [r, g, b] = tmp;
        }
      }
      d[o] = r; d[o + 1] = g; d[o + 2] = b; d[o + 3] = 255;
    }
  }
  m.ctx.putImageData(m.img, 0, 0, x0, y0, rw, rh);
}

// 图层或色标变化后重画；WebGL 只需要换 uniform
function repaintAll() {
  if (!app.map) return;
  if (!app.gl) paintRect(0, 0, app.map.w, app.map.h);
  updateLegend();
  requestDraw();
}

// 抽样取陆地像素侵蚀量与沉积量的 98% 分位作为色标上限，返回是否有明显变化
function updateErosionScale(m) {
  const er = [], dp = [];
  const stride = Math.max(1, Math.floor(m.t.length / 40000));
  for (let i = 0; i < m.t.length; i += stride) {
    if (m.t0[i] < SEA_LEVEL) continue;
    const d = m.t0[i] - m.t[i];
    if (d > 0) er.push(d); else if (d < 0) dp.push(-d);
  }
  const p98 = (a, floor) => {
    if (!a.length) return floor;
    a.sort((x, y) => x - y);
    return Math.max(floor, a[Math.floor((a.length - 1) * 0.98)]);
  };
  const erodeCap = p98(er, 4), depoCap = p98(dp, 4);
  const changed = !m.erodeCap || Math.abs(erodeCap - m.erodeCap) > m.erodeCap * 0.15 || Math.abs(depoCap - m.depoCap) > m.depoCap * 0.15;
  if (changed) Object.assign(m, { erodeCap, depoCap });
  return changed;
}

function updateLegend() {
  const m = app.map, el = $('legend');
  if (!m) { el.hidden = true; return; }
  let bar, lo, hi, title;
  if (app.layer === 'terrain') {
    bar = `linear-gradient(90deg, ${STOPS.map(s => `rgb(${s[1]},${s[2]},${s[3]}) ${s[0] * 100}%`).join(',')})`;
    title = '海拔'; lo = '0'; hi = String(m.scaleMax);
  } else if (app.layer === 'erosion') {
    bar = `linear-gradient(90deg, rgb(${DEPO}), #b4b8b6, rgb(${ERODE}))`;
    title = '侵蚀量 t0−t（98% 分位封顶）'; lo = `沉积 ${m.depoCap}`; hi = `侵蚀 ${m.erodeCap}`;
  } else if (app.layer === 'flow') {
    bar = `linear-gradient(90deg, ${FLOW_RAMP.map(c => `rgb(${c})`).join(',')})`;
    title = '流量 q（对数）'; lo = '1'; hi = String(m.maxQ);
  } else {
    bar = 'linear-gradient(90deg, #8a8a8a, rgb(40,150,255))';
    title = '积水 c'; lo = '0'; hi = String(m.maxC);
  }
  el.innerHTML = `<span>${title}</span><span class="bar" style="background:${bar}"></span><span class="ends"><span>${lo}</span><span>${hi}</span></span>`;
  el.hidden = false;
}

/* ---------- 视图 ---------- */

function fitView() {
  const m = app.map;
  if (!m) return;
  const r = viewport.getBoundingClientRect();
  if (!r.width || !r.height) return;
  const s = Math.min(r.width / m.w, r.height / m.h) * 0.96;
  app.view = { scale: s, ox: (r.width - m.w * s) / 2, oy: (r.height - m.h * s) / 2, auto: true };
  requestDraw();
}

function requestDraw() {
  if (app.drawPending) return;
  app.drawPending = true;
  requestAnimationFrame(draw);
}

function sizeCanvas(c, r, dpr) {
  const w = Math.round(r.width * dpr), h = Math.round(r.height * dpr);
  if (c.width !== w || c.height !== h) { c.width = w; c.height = h; }
}

function draw() {
  app.drawPending = false;
  const dpr = window.devicePixelRatio || 1;
  const r = viewport.getBoundingClientRect();
  sizeCanvas(canvas, r, dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, r.width, r.height);
  const m = app.map;
  if (app.gl) {
    sizeCanvas(glCanvas, r, dpr);
    if (!app.gl.lost) {
      app.gl.draw(app.view, {
        scaleMax: m ? m.scaleMax : 1, maxQ: m ? m.maxQ : 1, maxC: m ? m.maxC : 1,
        erodeCap: m ? m.erodeCap : 1, depoCap: m ? m.depoCap : 1,
        layer: app.layer, flowOverlay: app.flowOverlay,
      }, r.width, r.height, dpr);
    }
  }
  if (!m) return;
  const { scale: s, ox, oy } = app.view;
  if (!app.gl) {
    ctx.imageSmoothingEnabled = false;
    ctx.drawImage(m.canvas, ox, oy, m.w * s, m.h * s);
  }

  const cell = (x, y) => [ox + x * s, oy + y * s];
  const box = (x, y, color, width) => {
    const [px, py] = cell(x, y);
    const size = Math.max(s, 6);
    const pad = (size - s) / 2;
    ctx.lineWidth = width;
    ctx.strokeStyle = '#000';
    ctx.strokeRect(px - pad - 1, py - pad - 1, size + 2, size + 2);
    ctx.strokeStyle = color;
    ctx.strokeRect(px - pad, py - pad, size, size);
  };
  const f = app.focus, d = app.detail;
  if (f && d && d.x === f.x && d.y === f.y) {
    const [cx, cy] = cell(d.x + 0.5, d.y + 0.5);
    const link = (x, y, color) => {
      if (x < 0) return;
      const [tx, ty] = cell(x + 0.5, y + 0.5);
      ctx.strokeStyle = color; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(cx, cy); ctx.lineTo(tx, ty); ctx.stroke();
      ctx.fillStyle = color; ctx.beginPath(); ctx.arc(tx, ty, 3, 0, Math.PI * 2); ctx.fill();
    };
    link(d.preX, d.preY, '#f5d76e');
    link(d.outX, d.outY, '#6ee7f5');
    if (d.xPower || d.yPower) {
      const len = Math.max(18, s * 3);
      const ax = cx + d.xPower * len, ay = cy + d.yPower * len;
      ctx.strokeStyle = '#ffffff'; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(cx, cy); ctx.lineTo(ax, ay); ctx.stroke();
      const ang = Math.atan2(ay - cy, ax - cx);
      ctx.beginPath();
      ctx.moveTo(ax, ay);
      ctx.lineTo(ax - 7 * Math.cos(ang - 0.45), ay - 7 * Math.sin(ang - 0.45));
      ctx.lineTo(ax - 7 * Math.cos(ang + 0.45), ay - 7 * Math.sin(ang + 0.45));
      ctx.closePath(); ctx.fillStyle = '#ffffff'; ctx.fill();
    }
  }
  for (const w of app.watches) box(w.x, w.y, w.color, isFocus(w) ? 3 : 1.5);
  const h = app.hover;
  if (h && !findWatch(h.x, h.y)) box(h.x, h.y, '#ffffff', 1.5);
}

function toMap(clientX, clientY) {
  const r = viewport.getBoundingClientRect();
  const cx = clientX - r.left, cy = clientY - r.top;
  const { scale: s, ox, oy } = app.view;
  return { x: Math.floor((cx - ox) / s), y: Math.floor((cy - oy) / s), cx, cy };
}

function inMap(p) {
  return app.map && p.x >= 0 && p.y >= 0 && p.x < app.map.w && p.y < app.map.h;
}

let drag = null;
viewport.addEventListener('pointerdown', (e) => {
  if (!app.map) return;
  viewport.setPointerCapture(e.pointerId);
  drag = { x: e.clientX, y: e.clientY, ox: app.view.ox, oy: app.view.oy, moved: false };
});
viewport.addEventListener('pointermove', (e) => {
  if (!app.map) return;
  if (drag) {
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) > 4) { drag.moved = true; viewport.classList.add('dragging'); }
    if (drag.moved) {
      app.view.ox = drag.ox + dx; app.view.oy = drag.oy + dy; app.view.auto = false;
      requestDraw();
      return;
    }
  }
  const p = toMap(e.clientX, e.clientY);
  if (inMap(p)) showHover(p.x, p.y, p.cx, p.cy); else clearHover();
});
viewport.addEventListener('pointerup', (e) => {
  if (!drag) return;
  const wasDrag = drag.moved;
  drag = null;
  viewport.classList.remove('dragging');
  if (wasDrag) return;
  const p = toMap(e.clientX, e.clientY);
  if (!inMap(p)) return;
  const w = findWatch(p.x, p.y);
  if (w && isFocus(w)) removeWatch(w);
  else if (w) focusWatch(w);
  else addWatch(p.x, p.y);
});
viewport.addEventListener('pointerleave', () => { if (!drag) clearHover(); });
viewport.addEventListener('dblclick', fitView);
viewport.addEventListener('wheel', (e) => {
  if (!app.map) return;
  e.preventDefault();
  const r = viewport.getBoundingClientRect();
  const cx = e.clientX - r.left, cy = e.clientY - r.top;
  const v = app.view;
  const fit = Math.min(r.width / app.map.w, r.height / app.map.h);
  const ns = Math.min(64, Math.max(fit * 0.5, v.scale * Math.pow(1.15, -e.deltaY / 100)));
  v.ox = cx - (cx - v.ox) * ns / v.scale;
  v.oy = cy - (cy - v.oy) * ns / v.scale;
  v.scale = ns;
  v.auto = false;
  requestDraw();
}, { passive: false });
new ResizeObserver(() => (app.view.auto ? fitView() : requestDraw())).observe(viewport);

/* ---------- 观察列表 ---------- */

function findWatch(x, y) { return app.watches.find((w) => w.x === x && w.y === y); }
function isFocus(w) { return app.focus && app.focus.x === w.x && app.focus.y === w.y; }

function addWatch(x, y) {
  if (app.watches.length >= MAX_WATCHES) {
    // 与服务端一致：超出上限时移除最早加入的
    app.watches.shift();
  }
  const used = new Set(app.watches.map((w) => w.color));
  const color = WATCH_COLORS.find((c) => !used.has(c));
  const w = { x, y, color, hist: [] };
  app.watches.push(w);
  send({ op: 'watch', x, y });
  focusWatch(w);
}

function removeWatch(w) {
  app.watches = app.watches.filter((x) => x !== w);
  send({ op: 'unwatch', x: w.x, y: w.y });
  if (isFocus(w)) {
    const last = app.watches[app.watches.length - 1];
    app.focus = last ? { x: last.x, y: last.y } : null;
    app.detail = null;
    if (last) send({ op: 'pixel', x: last.x, y: last.y });
  }
  renderInspector();
  renderWatches();
  requestDraw();
}

function focusWatch(w) {
  app.focus = { x: w.x, y: w.y };
  app.detail = null;
  send({ op: 'pixel', x: w.x, y: w.y });
  renderInspector();
  renderWatches();
  requestDraw();
}

function onWatchSamples(samples) {
  for (const [x, y, tick, t, c, q] of samples) {
    const w = findWatch(x, y);
    if (!w) continue;
    const last = w.hist[w.hist.length - 1];
    if (last && last.tick === tick) continue;
    w.hist.push({ tick, t, c, q });
  }
  renderWatches();
}

function renderWatches() {
  $('watchCount').textContent = `${app.watches.length} / ${MAX_WATCHES}`;
  $('watchHint').hidden = app.watches.length > 0;
  const list = $('watchList');
  const want = app.watches.map((w) => `${w.x},${w.y}`).join('|');
  if (list.dataset.keys !== want) {
    list.dataset.keys = want;
    list.innerHTML = '';
    for (const w of app.watches) {
      const li = document.createElement('li');
      li.innerHTML = `<span class="dot" style="background:${w.color}"></span>
        <span class="meta"><b></b><span></span></span>
        <canvas width="84" height="24" aria-hidden="true"></canvas>
        <button type="button" aria-label="从观察列表移除">×</button>`;
      li.addEventListener('click', () => focusWatch(w));
      li.querySelector('button').addEventListener('click', (e) => { e.stopPropagation(); removeWatch(w); });
      w.el = li;
      list.appendChild(li);
    }
  }
  for (const w of app.watches) {
    const li = w.el;
    if (!li) continue;
    li.classList.toggle('on', isFocus(w));
    li.querySelector('b').textContent = `(${w.x}, ${w.y})`;
    const p = app.map && w.x < app.map.w && w.y < app.map.h ? localPixel(w.x, w.y) : null;
    const first = w.hist[0];
    li.querySelector('.meta span').textContent = p
      ? `t ${p.t}${first ? ` · Δ${p.t - first.t >= 0 ? '+' : ''}${p.t - first.t}` : ''}`
      : '—';
    drawSeries(li.querySelector('canvas'), w.hist, [{ key: 't', color: w.color }], { tiny: true });
  }
  const fw = app.focus && findWatch(app.focus.x, app.focus.y);
  $('historyBox').hidden = !fw;
  if (fw) {
    const css = getComputedStyle(document.documentElement);
    drawSeries($('history'), fw.hist, [
      { key: 'level', color: css.getPropertyValue('--river').trim() },
      { key: 't', color: css.getPropertyValue('--ink').trim() },
    ], { axes: true });
  }
}

// 画一条或多条随 tick 变化的折线；level = t + c
function drawSeries(c, hist, series, opt) {
  const dpr = window.devicePixelRatio || 1;
  const w = c.clientWidth || c.width, h = c.clientHeight || c.height;
  if (c.width !== Math.round(w * dpr)) { c.width = Math.round(w * dpr); c.height = Math.round(h * dpr); }
  const g = c.getContext('2d');
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const val = (p, key) => (key === 'level' ? p.t + p.c : p[key]);
  const css = getComputedStyle(document.documentElement);
  const muted = css.getPropertyValue('--ink-soft').trim();
  const rule = css.getPropertyValue('--rule').trim();
  if (hist.length === 0) return;
  let lo = Infinity, hi = -Infinity;
  for (const p of hist) for (const s of series) { lo = Math.min(lo, val(p, s.key)); hi = Math.max(hi, val(p, s.key)); }
  if (hi - lo < 4) { hi += 2; lo -= 2; }
  const padL = opt.axes ? 34 : 1, padR = opt.axes ? 6 : 1, padT = opt.axes ? 6 : 2, padB = opt.axes ? 16 : 2;
  const t0 = hist[0].tick, t1 = Math.max(hist[hist.length - 1].tick, t0 + 1);
  const X = (p) => padL + (p.tick - t0) / (t1 - t0) * (w - padL - padR);
  const Y = (v) => padT + (1 - (v - lo) / (hi - lo)) * (h - padT - padB);
  if (opt.axes) {
    g.font = '10px ' + css.getPropertyValue('--f-mono');
    g.fillStyle = muted; g.strokeStyle = rule; g.lineWidth = 1;
    for (const v of [hi, (hi + lo) / 2, lo]) {
      const y = Math.round(Y(v)) + 0.5;
      g.beginPath(); g.moveTo(padL, y); g.lineTo(w - padR, y); g.stroke();
      g.textAlign = 'right'; g.textBaseline = 'middle';
      g.fillText(String(Math.round(v)), padL - 4, y);
    }
    g.textBaseline = 'top';
    g.textAlign = 'left'; g.fillText(`tick ${fmt(t0)}`, padL, h - 12);
    g.textAlign = 'right'; g.fillText(fmt(t1), w - padR, h - 12);
  }
  for (const s of series) {
    g.strokeStyle = s.color; g.lineWidth = opt.tiny ? 1.25 : 1.5;
    g.beginPath();
    hist.forEach((p, i) => (i ? g.lineTo(X(p), Y(val(p, s.key))) : g.moveTo(X(p), Y(val(p, s.key)))));
    g.stroke();
    const e = hist[hist.length - 1];
    g.fillStyle = s.color;
    g.beginPath(); g.arc(X(e), Y(val(e, s.key)), opt.tiny ? 1.75 : 2.5, 0, Math.PI * 2); g.fill();
  }
}

/* ---------- 像素检视 ---------- */

function localPixel(x, y) {
  const m = app.map, i = y * m.w + x;
  return { x, y, t: m.t[i], t0: m.t0[i], c: m.c[i], q: m.q[i] };
}

function showHover(x, y, cx, cy) {
  if (!app.map || x >= app.map.w || y >= app.map.h) return;
  app.hover = { x, y, cx, cy };
  const p = localPixel(x, y);
  const er = p.t0 - p.t;
  const tip = $('tooltip');
  tip.innerHTML = `(${x}, ${y})<br>t ${p.t} · c ${p.c} · q ${p.q}<br>${er >= 0 ? '侵蚀' : '沉积'} ${Math.abs(er)}`;
  tip.hidden = false;
  const r = viewport.getBoundingClientRect();
  const tw = tip.offsetWidth, th = tip.offsetHeight;
  tip.style.left = `${cx + 16 + tw > r.width ? cx - tw - 12 : cx + 16}px`;
  tip.style.top = `${cy + 14 + th > r.height ? cy - th - 10 : cy + 14}px`;
  if (!app.focus) renderInspector();
  requestDraw();
}

function clearHover() {
  app.hover = null;
  $('tooltip').hidden = true;
  if (!app.focus) renderInspector();
  requestDraw();
}

function kvRows(rows) {
  return rows.map(([k, v, cls]) => `<dt>${k}</dt><dd${cls ? ` class="${cls}"` : ''}>${v}</dd>`).join('');
}

function fmt(n) { return Number(n).toLocaleString('zh-CN'); }

function renderInspector() {
  const target = app.focus || app.hover;
  const basic = $('pixelBasic');
  if (!app.map || !target || target.x >= app.map.w || target.y >= app.map.h) {
    $('pixelCoord').textContent = '—';
    basic.innerHTML = '';
    $('pixelHint').hidden = false;
    $('pixelDetail').hidden = true;
    return;
  }
  $('pixelHint').hidden = true;
  const p = localPixel(target.x, target.y);
  const er = p.t0 - p.t;
  $('pixelCoord').textContent = `(${p.x}, ${p.y})${app.focus ? ' · 观察中' : ''}`;
  basic.innerHTML = kvRows([
    ['高度 t', p.t],
    ['初始高度 t0', p.t0],
    ['侵蚀/沉积', er > 0 ? `侵蚀 ${er}` : er < 0 ? `沉积 ${-er}` : '无变化', er > 0 ? 'erode' : er < 0 ? 'depo' : ''],
    ['积水 c', p.c],
    ['水面 t+c', p.t + p.c],
    ['流量 q', fmt(p.q)],
    ['海洋', p.t < SEA_LEVEL ? '是' : '否'],
  ]);

  const d = app.detail;
  const showDetail = app.focus && d && d.x === app.focus.x && d.y === app.focus.y;
  $('pixelDetail').hidden = !showDetail;
  if (!showDetail) return;
  $('pinNote').textContent = `tick ${fmt(d.tick)}`;
  const coord = (x, y) => (x < 0 ? '无' : `(${x}, ${y})`);
  $('pixelExtra').innerHTML = kvRows([
    ['场向量', `(${d.xPower.toFixed(2)}, ${d.yPower.toFixed(2)})`],
    ['上游 <span style="color:#d9b84a">●</span>', coord(d.preX, d.preY)],
    ['下游 <span style="color:#3fc6d6">●</span>', coord(d.outX, d.outY)],
    ['累计侵蚀次数', fmt(d.erodeCnt), 'erode'],
    ['累计沉积次数', fmt(d.sediCnt), 'depo'],
    ['停留水滴', d.drops],
    ['水滴携带沙量', d.sand],
  ]);
  const scaleMax = app.map.scaleMax;
  $('neighborhood').innerHTML = d.neighborhood.map((v, i) => {
    if (v < 0) return '<span style="background:transparent;color:inherit">·</span>';
    let bg;
    if (v < SEA_LEVEL) bg = `rgb(${SEA})`;
    else {
      const li = Math.min(LUT_N - 1, Math.floor(v / scaleMax * (LUT_N - 1))) * 3;
      bg = `rgb(${LUT[li]},${LUT[li + 1]},${LUT[li + 2]})`;
    }
    const fg = v < SEA_LEVEL ? '#fff' : '#111';
    return `<span class="${i === 12 ? 'center' : ''}" style="background:${bg};color:${fg}">${v}</span>`;
  }).join('');
}

/* ---------- 统计 ---------- */

const STATE_TEXT = { generating: '生成中', paused: '已暂停', running: '运行中', finished: '已完成', failed: '失败' };

function renderStats(st) {
  app.stats = st;
  $('stats').innerHTML = kvRows([
    ['阶段', `${st.stage + 1} / ${st.stages}`],
    ['地图', `${st.width} × ${st.length}`],
    ['水滴批次', `${Math.min(st.page + 1, st.pages)} / ${st.pages}`],
    ['水滴数', fmt(st.drops)],
    ['侵蚀次数', fmt(st.erosions), 'erode'],
    ['沉积次数', fmt(st.sediments), 'depo'],
    ['积水交换', fmt(st.exchanges)],
    ['总流量', fmt(st.qCount)],
    ['海拔范围', `${st.minLv} – ${st.maxLv}`],
    ['最大流量', fmt(st.maxQ)],
  ]);
  const pct = st.totalTicks ? st.tick / st.totalTicks * 100 : 100;
  $('progressBar').style.width = `${pct}%`;
  $('progressText').textContent = `${fmt(st.tick)} / ${fmt(st.totalTicks)}`;
  const last = app.history[app.history.length - 1];
  if (!last || last.tick !== st.tick) app.history.push({ tick: st.tick, v: st.erosions });
  if (app.history.length > 400) app.history.splice(0, app.history.length - 400);
  drawSpark();
}

function drawSpark() {
  const c = $('spark'), g = c.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const w = c.clientWidth, h = c.clientHeight;
  c.width = w * dpr; c.height = h * dpr;
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const pts = app.history;
  const css = getComputedStyle(document.documentElement);
  g.strokeStyle = css.getPropertyValue('--rule');
  g.beginPath(); g.moveTo(0, h - 0.5); g.lineTo(w, h - 0.5); g.stroke();
  if (pts.length < 2) return;
  const t0 = pts[0].tick, t1 = Math.max(pts[pts.length - 1].tick, t0 + 1);
  const vmax = Math.max(pts[pts.length - 1].v, 1);
  const X = (p) => (p.tick - t0) / (t1 - t0) * (w - 4) + 2;
  const Y = (p) => h - 4 - p.v / vmax * (h - 12);
  const color = css.getPropertyValue('--erode');
  g.beginPath();
  pts.forEach((p, i) => (i ? g.lineTo(X(p), Y(p)) : g.moveTo(X(p), Y(p))));
  g.lineTo(X(pts[pts.length - 1]), h); g.lineTo(X(pts[0]), h); g.closePath();
  g.globalAlpha = 0.15; g.fillStyle = color; g.fill(); g.globalAlpha = 1;
  g.beginPath();
  pts.forEach((p, i) => (i ? g.lineTo(X(p), Y(p)) : g.moveTo(X(p), Y(p))));
  g.strokeStyle = color; g.lineWidth = 1.5; g.stroke();
  const e = pts[pts.length - 1];
  g.fillStyle = color; g.beginPath(); g.arc(X(e), Y(e), 2.5, 0, Math.PI * 2); g.fill();
}

/* ---------- 控制 ---------- */

function setState(state, speed, error) {
  app.state = state;
  if (speed) app.speed = speed;
  const chip = $('stateChip');
  chip.hidden = !state;
  chip.className = `chip ${state}`;
  chip.textContent = STATE_TEXT[state] || state;
  if (state === 'failed') {
    $('empty').hidden = false;
    $('empty').classList.remove('loading');
    $('empty').querySelector('.empty-title').textContent = '生成失败';
    $('empty').querySelector('p:last-child').textContent = error || '请调整参数后重试。';
  }
  updateControls();
}

function updateControls() {
  const ready = !!app.map;
  const run = $('runBtn');
  run.disabled = !ready || !app.owner || !['paused', 'running'].includes(app.state);
  run.textContent = app.state === 'running' ? '暂停' : app.state === 'finished' ? '已完成' : (app.stats && app.stats.tick > 0 ? '继续' : '开始');
  $('stepBtn').disabled = !ready || !app.owner || app.state !== 'paused';
  document.querySelectorAll('.speed button').forEach((b) => {
    b.classList.toggle('on', Number(b.dataset.speed) === app.speed);
    b.disabled = !app.owner;
  });
  $('readOnlyChip').hidden = app.owner || !app.id;
  $('exportBtn').disabled = !ready;
  $('shareBtn').disabled = !app.id;
  document.querySelectorAll('#exportMenu a').forEach((a) => {
    a.href = app.id ? `/api/sims/${app.id}/export?fmt=${a.dataset.fmt}&zoom=2` : '#';
    a.setAttribute('download', '');
  });
}

function toggleRun() {
  if (app.state === 'running') send({ op: 'pause' });
  else if (app.state === 'paused') send({ op: 'run' });
}

$('runBtn').addEventListener('click', toggleRun);
$('stepBtn').addEventListener('click', () => send({ op: 'step', n: 10 }));
document.querySelectorAll('.speed button').forEach((b) => b.addEventListener('click', () => {
  app.speed = Number(b.dataset.speed);
  send({ op: 'speed', level: app.speed });
  updateControls();
}));
document.querySelectorAll('.layer').forEach((b) => b.addEventListener('click', () => {
  app.layer = b.dataset.layer;
  document.querySelectorAll('.layer').forEach((x) => {
    x.classList.toggle('on', x === b);
    x.setAttribute('aria-checked', String(x === b));
  });
  repaintAll();
}));
$('flowOverlay').addEventListener('change', (e) => { app.flowOverlay = e.target.checked; repaintAll(); });
document.addEventListener('keydown', (e) => {
  if (e.code !== 'Space' || ['INPUT', 'SELECT', 'TEXTAREA', 'BUTTON'].includes(e.target.tagName)) return;
  e.preventDefault();
  toggleRun();
});

/* ---------- 连接 ---------- */

function send(msg) {
  if (app.ws && app.ws.readyState === WebSocket.OPEN) app.ws.send(JSON.stringify(msg));
}

function onText(msg) {
  switch (msg.type) {
    case 'hello':
      app.owner = msg.owner;
      updateControls();
      break;
    case 'state':
      setState(msg.state, msg.speed, msg.error);
      break;
    case 'stats':
      renderStats(msg);
      setState(msg.state);
      if (app.map && updateErosionScale(app.map) && app.layer === 'erosion') repaintAll();
      if (app.focus) send({ op: 'pixel', x: app.focus.x, y: app.focus.y });
      break;
    case 'pixel':
      app.detail = msg;
      renderInspector();
      requestDraw();
      break;
    case 'watch':
      onWatchSamples(msg.s);
      break;
    case 'error':
      toast(msg.message);
      break;
  }
}

// keep 为 true 时是断线重连：保留地图、观察列表和历史，连上后重新订阅
function connect(id, keep = false) {
  if (app.ws) { const old = app.ws; app.ws = null; old.close(); }
  if (!keep) {
    Object.assign(app, { id, map: null, focus: null, detail: null, hover: null, stats: null, watches: [], history: [], state: 'generating' });
    $('sessionLabel').textContent = `模拟 ${id}`;
    $('empty').hidden = false;
    $('empty').classList.add('loading');
    $('empty').querySelector('.empty-title').textContent = '正在生成地形';
    $('empty').querySelector('p:last-child').textContent = '大地图的地形生成需要几秒钟。';
    $('stats').innerHTML = '';
    $('legend').hidden = true;
    setState('generating');
    renderInspector();
    renderWatches();
    requestDraw();
  }
  app.queue = Promise.resolve();
  fetch(`/api/sims/${id}`).then((r) => (r.ok ? r.json() : null)).then((info) => {
    if (!info || app.id !== id) return;
    app.owner = info.owner;
    app.request = info.request || null;
    updateControls();
  }).catch(() => {});
  markMine();

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws/sims/${id}`);
  ws.binaryType = 'arraybuffer';
  app.ws = ws;
  ws.onopen = () => {
    for (const w of app.watches) send({ op: 'watch', x: w.x, y: w.y });
  };
  // 二进制帧需要异步解压；文本消息也排进同一个队列，保证与地图帧的先后顺序
  // (例如放大后坐标已翻倍的观察采样必须在新尺寸的全量帧之后处理)
  ws.onmessage = (ev) => {
    const data = ev.data;
    const job = typeof data === 'string' ? () => onText(JSON.parse(data)) : () => onBinary(data);
    app.queue = app.queue.then(() => { if (app.ws === ws) return job(); }).catch((err) => console.error(err));
  };
  ws.onclose = () => {
    if (app.ws !== ws) return;
    setTimeout(async () => {
      if (app.ws !== ws) return;
      const res = await fetch(`/api/sims/${id}`).catch(() => null);
      if (res && res.ok) connect(id, true);
      else {
        setState(null);
        $('sessionLabel').textContent = '模拟已过期，请重新生成';
        history.replaceState(null, '', location.pathname);
      }
    }, 1500);
  };
}

/* ---------- 参数表单 ---------- */

const FIELDS = ['width', 'length', 'seed', 'hills', 'hillWide', 'ridges', 'ridgeLen', 'ridgeWide', 'stuck', 'petalShape', 'petalNum', 'dropNum', 'times', 'enlarge'];

function loadPreset(name) {
  const p = PRESETS[name];
  for (const f of FIELDS) $(f).value = p[f];
}

$('preset').addEventListener('change', (e) => loadPreset(e.target.value));

$('paramsForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const body = { petalSharp: 0.5 };
  for (const f of FIELDS) body[f] = $(f).value === '' ? 0 : Number($(f).value);
  for (const f of ['fixField', 'fixMiddle', 'autoRun']) body[f] = $(f).checked;
  const err = $('formError');
  err.hidden = true;
  const btn = $('createBtn');
  btn.disabled = true;
  try {
    const res = await fetch('/api/sims', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || `请求失败 (${res.status})`);
    if ($('seed').value === '') $('seed').placeholder = `上次 ${data.seed}`;
    history.replaceState(null, '', `${location.pathname}#${data.id}`);
    $('sharedNote').hidden = true;
    connect(data.id);
    refreshMine();
  } catch (ex) {
    err.textContent = ex.message;
    err.hidden = false;
  } finally {
    btn.disabled = false;
  }
});

/* ---------- 分享、导出、我的模拟 ---------- */

let toastTimer = 0;
function toast(text) {
  const el = $('toast');
  el.textContent = text;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2600);
}

async function copyText(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`${label}已复制`);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    toast(ok ? `${label}已复制` : `复制失败，请手动复制：${text}`);
  }
}

const b64url = (str) => btoa(str).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
const unb64url = (str) => atob(str.replace(/-/g, '+').replace(/_/g, '/'));

function setupMenu(btnId, menuId) {
  const btn = $(btnId), menu = $(menuId);
  const close = () => { menu.hidden = true; btn.setAttribute('aria-expanded', 'false'); };
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    const open = menu.hidden;
    document.querySelectorAll('.menu').forEach((m) => { m.hidden = true; });
    menu.hidden = !open;
    btn.setAttribute('aria-expanded', String(open));
  });
  menu.addEventListener('click', () => close());
  document.addEventListener('click', (e) => { if (!menu.contains(e.target)) close(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); });
}
setupMenu('shareBtn', 'shareMenu');
setupMenu('exportBtn', 'exportMenu');

$('shareMenu').addEventListener('click', (e) => {
  const kind = e.target.closest('[data-share]')?.dataset.share;
  if (!kind || !app.id) return;
  const base = `${location.origin}${location.pathname}`;
  if (kind === 'view') copyText(`${base}#${app.id}`, '查看链接');
  else if (app.request) copyText(`${base}?p=${b64url(JSON.stringify(app.request))}`, '参数链接');
  else toast('还没有拿到这个模拟的参数，请稍后再试');
});

// 从 ?p= 载入别人分享的参数
function loadSharedParams() {
  const raw = new URLSearchParams(location.search).get('p');
  if (!raw) return;
  try {
    const req = JSON.parse(unb64url(raw));
    for (const f of FIELDS) if (req[f] !== undefined) $(f).value = req[f];
    for (const f of ['fixField', 'fixMiddle']) $(f).checked = !!req[f];
    $('sharedNote').hidden = false;
  } catch {
    toast('分享链接中的参数无法识别');
  }
}

const STATE_SHORT = { generating: '生成中', paused: '已暂停', running: '运行中', finished: '已完成', failed: '失败' };

function relTime(iso) {
  const sec = (Date.now() - new Date(iso).getTime()) / 1000;
  if (sec < 60) return '刚刚';
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  return `${Math.floor(sec / 86400)} 天前`;
}

async function refreshMine() {
  const res = await fetch('/api/sims').catch(() => null);
  if (!res || !res.ok) return;
  const list = await res.json();
  const ul = $('mineList');
  ul.innerHTML = '';
  $('mineEmpty').hidden = list.length > 0;
  for (const it of list) {
    const li = document.createElement('li');
    li.dataset.id = it.id;
    const pct = it.totalTicks ? Math.floor(it.tick / it.totalTicks * 100) : 0;
    const size = it.width ? `${it.width}×${it.length}` : '';
    li.innerHTML = '<span class="meta"><b></b><span></span></span><button type="button" aria-label="删除这个模拟">×</button>';
    li.querySelector('b').textContent = it.id;
    li.querySelector('.meta span').textContent = [size, STATE_SHORT[it.state] || it.state, it.state === 'finished' ? '' : `${pct}%`, relTime(it.created)].filter(Boolean).join(' · ');
    li.addEventListener('click', () => {
      if (it.id === app.id) return;
      history.replaceState(null, '', `${location.pathname}#${it.id}`);
      connect(it.id);
    });
    li.querySelector('button').addEventListener('click', async (e) => {
      e.stopPropagation();
      if (!confirm(`删除模拟 ${it.id}？删除后无法恢复。`)) return;
      const r = await fetch(`/api/sims/${it.id}`, { method: 'DELETE' });
      if (!r.ok) { toast('删除失败'); return; }
      if (it.id === app.id) resetEmpty();
      refreshMine();
    });
    ul.appendChild(li);
  }
  markMine();
}

function markMine() {
  document.querySelectorAll('#mineList li').forEach((li) => li.classList.toggle('on', li.dataset.id === app.id));
}

function resetEmpty() {
  if (app.ws) { const old = app.ws; app.ws = null; old.close(); }
  Object.assign(app, { id: null, map: null, focus: null, detail: null, hover: null, stats: null, watches: [], history: [], state: null, owner: true, request: null });
  history.replaceState(null, '', location.pathname);
  $('sessionLabel').textContent = '未创建模拟';
  $('empty').hidden = false;
  $('empty').classList.remove('loading');
  $('empty').querySelector('.empty-title').textContent = '还没有地图';
  $('empty').querySelector('p:last-child').textContent = '在左侧选择预设或调整参数，点击“生成地形”。';
  $('stats').innerHTML = '';
  $('legend').hidden = true;
  setState(null);
  renderInspector();
  renderWatches();
  requestDraw();
}

$('refreshMine').addEventListener('click', refreshMine);
setInterval(() => { if (document.visibilityState === 'visible') refreshMine(); }, 15000);

loadPreset('quick');
loadSharedParams();
updateControls();
renderWatches();
refreshMine();
(async () => {
  const id = location.hash.slice(1);
  if (!/^[0-9a-f]{12}$/.test(id)) return;
  const res = await fetch(`/api/sims/${id}`).catch(() => null);
  if (res && res.ok) connect(id);
  else {
    history.replaceState(null, '', location.pathname + location.search);
    toast('这个模拟不存在或已被删除');
  }
})();
