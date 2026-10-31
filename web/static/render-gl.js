'use strict';

// WebGL2 地图渲染：t/t0/c/q 作为整数纹理上传，着色全部在片元着色器里完成。
// 与 app.js 中 Canvas 2D 回退路径 paintRect 的着色规则保持一致。
const GL_FRAG = `#version 300 es
precision highp float;
precision highp int;
uniform highp isampler2D uT;
uniform highp isampler2D uT0;
uniform highp isampler2D uC;
uniform highp isampler2D uQ;
uniform sampler2D uLut;
uniform ivec2 uSize;
uniform vec2 uOrigin;
uniform float uScale;
uniform float uDpr;
uniform float uCanvasH;
uniform float uScaleMax;
uniform float uLogMaxQ;
uniform float uMaxC;
uniform float uErodeCap;
uniform float uDepoCap;
uniform int uLayer;
uniform bool uFlowOverlay;
out vec4 outColor;

const float SEA_LEVEL = 2.0;
const vec3 SEA = vec3(42.0, 93.0, 143.0);
const vec3 RIVER = vec3(64.0, 114.0, 203.0);
const vec3 ERODE = vec3(196.0, 83.0, 46.0);
const vec3 DEPO = vec3(47.0, 111.0, 176.0);
const vec3 RAMP[4] = vec3[4](vec3(18.0, 26.0, 34.0), vec3(40.0, 110.0, 200.0), vec3(120.0, 220.0, 240.0), vec3(255.0));

void main() {
  vec2 css = vec2(gl_FragCoord.x, uCanvasH - gl_FragCoord.y) / uDpr;
  vec2 m = (css - uOrigin) / uScale;
  ivec2 p = ivec2(floor(m));
  if (p.x < 0 || p.y < 0 || p.x >= uSize.x || p.y >= uSize.y) discard;

  float h = float(texelFetch(uT, p, 0).r);
  float left = float(texelFetch(uT, ivec2(max(p.x - 1, 0), p.y), 0).r);
  float up = float(texelFetch(uT, ivec2(p.x, max(p.y - 1, 0)), 0).r);
  float shade = clamp(1.0 + ((h - left) + (h - up)) * 30.0 / uScaleMax, 0.7, 1.25);
  bool sea = h < SEA_LEVEL;
  float q = float(texelFetch(uQ, p, 0).r);
  vec3 col;

  if (uLayer == 0 || uLayer == 3) {
    if (sea) {
      col = SEA;
    } else {
      int li = min(511, int(floor(h / uScaleMax * 511.0)));
      col = texelFetch(uLut, ivec2(li, 0), 0).rgb * 255.0 * shade;
    }
    if (uLayer == 3) {
      col = vec3((col.r + col.g + col.b) / 3.0 * 0.55 + 90.0);
      float c = float(texelFetch(uC, p, 0).r);
      if (c > 0.0 && !sea) col = mix(col, vec3(40.0, 150.0, 255.0), min(1.0, 0.25 + 0.75 * sqrt(c / uMaxC)));
    } else if (uFlowOverlay && q > 0.0 && !sea) {
      float f = log(q + 1.0) / uLogMaxQ;
      col = mix(col, RIVER, min(0.9, f * f * 1.1));
    }
  } else if (uLayer == 1) {
    float grey = sea ? 70.0 : 215.0 - 90.0 * (h / uScaleMax);
    vec3 base = vec3(grey * shade);
    float diff = float(texelFetch(uT0, p, 0).r) - h;
    float f = diff >= 0.0 ? min(1.0, diff / uErodeCap) : -min(1.0, -diff / uDepoCap);
    col = f > 0.0 ? mix(base, ERODE, pow(f, 0.7)) : f < 0.0 ? mix(base, DEPO, pow(-f, 0.7)) : base;
  } else {
    float f = q > 0.0 ? log(q + 1.0) / uLogMaxQ : 0.0;
    if (f <= 0.0) {
      float v = sea ? 10.0 : 22.0 * shade;
      col = vec3(v, v + 4.0, v + 8.0);
    } else {
      f = min(0.999, f) * 3.0;
      int k = int(floor(f));
      col = mix(RAMP[k], RAMP[k + 1], f - float(k));
    }
  }
  outColor = vec4(clamp(col, 0.0, 255.0) / 255.0, 1.0);
}`;

const GL_VERT = `#version 300 es
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}`;

const GL_LAYERS = { terrain: 0, erosion: 1, flow: 2, water: 3 };

function createGLRenderer(canvas, lut) {
  const gl = canvas.getContext('webgl2', { antialias: false, alpha: true, premultipliedAlpha: false });
  if (!gl) return null;

  let prog, uni, tex, size = null;

  function compile(type, src) {
    const sh = gl.createShader(type);
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(sh));
    return sh;
  }

  function newTex() {
    const t = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return t;
  }

  function setup() {
    prog = gl.createProgram();
    gl.attachShader(prog, compile(gl.VERTEX_SHADER, GL_VERT));
    gl.attachShader(prog, compile(gl.FRAGMENT_SHADER, GL_FRAG));
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(prog));
    uni = {};
    const n = gl.getProgramParameter(prog, gl.ACTIVE_UNIFORMS);
    for (let i = 0; i < n; i++) {
      const name = gl.getActiveUniform(prog, i).name;
      uni[name] = gl.getUniformLocation(prog, name);
    }
    tex = { t: newTex(), t0: newTex(), c: newTex(), q: newTex(), lut: newTex() };
    const rgba = new Uint8Array(lut.length / 3 * 4);
    for (let i = 0, j = 0; i < lut.length; i += 3, j += 4) {
      rgba[j] = lut[i]; rgba[j + 1] = lut[i + 1]; rgba[j + 2] = lut[i + 2]; rgba[j + 3] = 255;
    }
    gl.bindTexture(gl.TEXTURE_2D, tex.lut);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, lut.length / 3, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, rgba);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    size = null;
  }

  const layers = [
    ['t', gl.R16I, gl.SHORT],
    ['t0', gl.R16I, gl.SHORT],
    ['c', gl.R16I, gl.SHORT],
    ['q', gl.R32I, gl.INT],
  ];

  function upload(map) {
    for (const [k, fmt, type] of layers) {
      gl.bindTexture(gl.TEXTURE_2D, tex[k]);
      gl.texImage2D(gl.TEXTURE_2D, 0, fmt, map.w, map.h, 0, gl.RED_INTEGER, type, map[k]);
    }
    size = [map.w, map.h];
  }

  // 单个区块的增量，数组按区块行优先排列
  function updateTile(x0, y0, tw, th, tile) {
    for (const [k, , type] of layers) {
      if (!tile[k]) continue;
      gl.bindTexture(gl.TEXTURE_2D, tex[k]);
      gl.texSubImage2D(gl.TEXTURE_2D, 0, x0, y0, tw, th, gl.RED_INTEGER, type, tile[k]);
    }
  }

  function draw(view, u, cssW, cssH, dpr) {
    gl.viewport(0, 0, canvas.width, canvas.height);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    if (!size) return;
    gl.useProgram(prog);
    const units = [['uT', tex.t], ['uT0', tex.t0], ['uC', tex.c], ['uQ', tex.q], ['uLut', tex.lut]];
    units.forEach(([name, t], i) => {
      gl.activeTexture(gl.TEXTURE0 + i);
      gl.bindTexture(gl.TEXTURE_2D, t);
      gl.uniform1i(uni[name], i);
    });
    gl.uniform2i(uni.uSize, size[0], size[1]);
    gl.uniform2f(uni.uOrigin, view.ox, view.oy);
    gl.uniform1f(uni.uScale, view.scale);
    gl.uniform1f(uni.uDpr, dpr);
    gl.uniform1f(uni.uCanvasH, canvas.height);
    gl.uniform1f(uni.uScaleMax, u.scaleMax);
    gl.uniform1f(uni.uLogMaxQ, Math.log(u.maxQ + 1));
    gl.uniform1f(uni.uMaxC, u.maxC);
    gl.uniform1f(uni.uErodeCap, u.erodeCap);
    gl.uniform1f(uni.uDepoCap, u.depoCap);
    gl.uniform1i(uni.uLayer, GL_LAYERS[u.layer]);
    gl.uniform1i(uni.uFlowOverlay, u.flowOverlay ? 1 : 0);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }

  try {
    setup();
  } catch (err) {
    console.warn('WebGL2 初始化失败，改用 Canvas 2D', err);
    return null;
  }

  const r = { upload, updateTile, draw, lost: false, onRestore: null };
  canvas.addEventListener('webglcontextlost', (e) => { e.preventDefault(); r.lost = true; });
  canvas.addEventListener('webglcontextrestored', () => {
    setup();
    r.lost = false;
    if (r.onRestore) r.onRestore();
  });
  return r;
}
