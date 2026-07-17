// SPDX-License-Identifier: Elastic-2.0
// replay-player.js — the corral canvas renderer + the read-only mission
// replay player, shared verbatim between the product UI (internal/ui/web/
// index.html, loaded via <script src="/replay-player.js">) and the
// corralai.dev site (site/public/replay-player.js, a hash-checked copy —
// see scripts/sync-site-assets.sh). Extracted from index.html so a static
// site with no brain running can embed the identical player.
//
// DOM CONTRACT — the embedding page MUST provide:
//   <canvas id="c">                the render surface
//   #replay, #replay-playbtn, #replay-scrub, #replay-label, #replay-title
//                                   the replay control bar (see index.html's
//                                   markup for the exact structure/CSS)
//   a global function setView(v)   called with 'replay' on startReplay() and
//                                   with the previous view on closeReplay()/
//                                   stopReplaySession(); at minimum it must
//                                   toggle #replay's "show" class. index.html
//                                   keeps its full multi-tab setView(); a
//                                   standalone embed can supply a two-line
//                                   version — see docs/superpowers/plans/
//                                   2026-07-03-corralai-dev-site.md Task 4.
//                                   GOTCHA: if the embedding page's script
//                                   uses a templating mechanism that wraps
//                                   the script body in a closure (e.g.
//                                   Astro's `define:vars`), a plain
//                                   `function setView(v){}` declaration is
//                                   scoped to that closure, NOT global — this
//                                   file's own top-level `setView('replay')`
//                                   call will throw ReferenceError. Assign
//                                   `window.setView = function(v){...}`
//                                   explicitly in that case.
//
// Optional, null-guarded (safe to omit entirely): #empty, #stat, #skinsel,
// #skinsub, #tab-swarm, #tab-proposals, #proposals-badge, #tab-completed,
// #themebtn — and the replay-cockpit panels #exec, #tasks, #agents, #findings
// (when present, replay populates them from the tape; the canvas-only hero
// omits them and loses nothing).
//
// To start a live SSE-driven view (the product page only — a static replay
// embed never calls this): `es = connectSSE();`
// connectSSE() additionally requires a global apply(state) function; static
// embeds must never call connectSSE().
// To play a replay (works with no brain at all): `startReplay(streamOrUrl)`
// where streamOrUrl is either a URL string (GETted as {events:[...]}) or an
// already-resolved {events:[...]} object/array. Replay is genuinely
// backend-free end to end: requestChatter() (the in-character speech bubbles)
// short-circuits to its canned-line fallback whenever `inReplay` is true, so
// no /api/chatter call is ever made while replaying — a static embed with no
// backend at all will never see a failed network request or console error.

const cv = document.getElementById('c'), ctx = cv.getContext('2d');
// The canvas now lives in the CENTER grid cell, so it sizes to ITS box (not the
// window). CW/CH are its CSS-pixel dimensions; all physics + drawing use them.
let CW = 800, CH = 600;
function resize(){
  const r = cv.getBoundingClientRect();
  CW = Math.max(1, r.width); CH = Math.max(1, r.height);
  cv.width = CW*devicePixelRatio; cv.height = CH*devicePixelRatio;
  ctx.setTransform(devicePixelRatio,0,0,devicePixelRatio,0,0);
}
addEventListener('resize', resize);
new ResizeObserver(resize).observe(cv);   // re-fit when the cell changes (tab switches, window)
resize();

// ---- view transform: wheel-zoom + drag-pan over the corral ----
// screen = world*viewScale + viewOffset. All node physics/layout stay in
// world coordinates (CW/CH space); draw() applies the transform once per
// frame via ctx.setTransform, and every consumer of a canvas mouse event
// must go through canvasToWorld() for hit-testing (see index.html).
let viewScale = 1, viewOX = 0, viewOY = 0;
const VIEW_MIN = 0.4, VIEW_MAX = 4;
let viewDidPan = false;   // set by a completed drag-pan; eats the click that follows
function screenToWorld(sx, sy){ return { x: (sx - viewOX)/viewScale, y: (sy - viewOY)/viewScale }; }
function canvasToWorld(ev){
  const r = cv.getBoundingClientRect();
  return screenToWorld(ev.clientX - r.left, ev.clientY - r.top);
}
function getViewTransform(){ return { scale: viewScale, x: viewOX, y: viewOY }; }
function resetView(){ viewScale = 1; viewOX = 0; viewOY = 0; }
function zoomAt(sx, sy, factor){
  const ns = Math.min(VIEW_MAX, Math.max(VIEW_MIN, viewScale * factor));
  // keep the world point under the cursor fixed: it maps to the same screen
  // px before and after the scale change, so the offset absorbs the delta.
  viewOX = sx - (sx - viewOX) * (ns / viewScale);
  viewOY = sy - (sy - viewOY) * (ns / viewScale);
  viewScale = ns;
}
cv.addEventListener('wheel', ev => {
  ev.preventDefault();
  dismissViewHint();
  const r = cv.getBoundingClientRect();
  // Trackpad pinch arrives as the SAME 'wheel' event with ctrlKey set (the
  // browser's own convention for "prevent page zoom, handle it yourself").
  // It tends to report much larger per-tick deltaY than a physical wheel
  // notch, so it gets a gentler coefficient to land at a comparable feel.
  const k = ev.ctrlKey ? 0.008 : 0.0015;
  zoomAt(ev.clientX - r.left, ev.clientY - r.top, Math.exp(-ev.deltaY * k));
}, { passive: false });
// drag-to-pan: only engages after a small movement threshold, so plain
// clicks (agent windows, inspector) keep working untouched. A completed pan
// fires a trailing 'click'; the hit-test in index.html ignores it by asking
// viewJustPanned() at the top of its handler (a shared flag, NOT a capture-
// phase propagation trick — that only worked by script load-order accident
// and a reorder/extra listener would silently break it).
let panFrom = null;
cv.addEventListener('mousedown', ev => { panFrom = { x: ev.clientX, y: ev.clientY, ox: viewOX, oy: viewOY }; });
addEventListener('mousemove', ev => {
  if(!panFrom) return;
  const dx = ev.clientX - panFrom.x, dy = ev.clientY - panFrom.y;
  if(!viewDidPan && Math.hypot(dx, dy) < 4) return;   // threshold: not a pan yet
  viewDidPan = true;
  dismissViewHint();
  viewOX = panFrom.ox + dx; viewOY = panFrom.oy + dy;
});
// Clear pan state on the GLOBAL mouseup — it always fires, even when the
// release lands OFF the canvas (routine: mousemove/mouseup are window-level
// so a pan continues past the edge, and the zoom controls sit right at that
// edge). A canvas 'click' only fires when mouseup also lands on cv, so
// clearing viewDidPan there leaked the flag when the release was off-canvas
// and silently ate the NEXT legit click. Deferring the reset to a macrotask
// keeps it true through the pan's OWN trailing click (dispatched right after
// this mouseup, before the timer) while a later unrelated click sees false.
addEventListener('mouseup', () => { panFrom = null; setTimeout(() => { viewDidPan = false; }, 0); });
// The one authority index.html's canvas hit-test consults to skip a pan's
// trailing click — robust to listener/registration order by construction.
function viewJustPanned(){ return viewDidPan; }
// double-click on EMPTY space resets to the 1x fit; a double-click on an
// agent stays the rename shortcut (index.html's own dblclick hit-test) —
// the two are disjoint by the same hit radius the rename handler uses.
// (No keyboard binding: neither file has canvas keyboard idioms to match.)
cv.addEventListener('dblclick', ev => {
  const p = canvasToWorld(ev);
  for(const n of nodes.values()){
    if(n.kind !== 'agent') continue;
    if(Math.hypot(n.x - p.x, n.y - p.y) < 22) return;  // rename territory — leave it alone
  }
  resetView();
});

// ---- SITE-ONLY canvas click-to-inspect (product owns its own in index.html) ----
// On the backend-free site there's no index.html canvas handler, so wire the
// swarm canvas here: click an AGENT node → open its .aw-win inspector; click a
// TASK node (the claimed-task node, id "p:<taskkey>") → open its storytelling
// modal. Guarded to the site (openAgentWindow is product-only) and to replay,
// and it defers to viewJustPanned() so a drag-pan never opens a window. Nodes
// drift under the force layout, but hit-testing is done in WORLD coords at
// click time, so they stay clickable wherever they float.
function replayCanvasHit(ev){
  const p = canvasToWorld(ev);
  let best = null, bd = 1e9;
  for(const n of nodes.values()){
    const r = n.kind === 'agent' ? 16 : 9;
    const d = Math.hypot(n.x - p.x, n.y - p.y);
    if(d < r + 6 && d < bd){ best = n; bd = d; }
  }
  return best;
}
cv.addEventListener('click', ev => {
  if(typeof openAgentWindow === 'function') return; // product owns canvas clicks
  if(!inReplay) return;
  if(viewJustPanned()) return;
  const best = replayCanvasHit(ev);
  if(!best) return;
  if(best.kind === 'agent') replayAgentClick(best.fullname || best.label);
  else if(best.kind === 'path') replayTaskClick(best.id.slice(2)); // "p:<taskkey>" → taskkey
});
// Pointer affordance: show a pointer cursor when hovering a clickable node
// (site + replay only). Cheap hit-test on move; leaves the pan/zoom cursor
// alone otherwise.
cv.addEventListener('mousemove', ev => {
  if(typeof openAgentWindow === 'function' || !inReplay){ return; }
  if(panFrom){ return; } // mid-drag: don't fight the pan
  cv.style.cursor = replayCanvasHit(ev) ? 'pointer' : '';
});

// ---- touch: one-finger drag-pan, two-finger pinch-to-zoom ----
// A LinkedIn click from a phone/tablet has no wheel and no mouse, so touch
// gets its own gesture handling over the SAME transform + click-eater flag
// (a tap that ends a pan still fires a synthetic 'click', which the capture-
// phase listener above already eats via viewDidPan).
let touchMode = null;      // 'pan' | 'pinch' | null
let touchPanFrom = null;
let pinchStartDist = 0, pinchStartScale = 1;
function touchDist(a, b){ return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY); }
function touchMidCanvas(a, b){
  const r = cv.getBoundingClientRect();
  return { x: (a.clientX + b.clientX) / 2 - r.left, y: (a.clientY + b.clientY) / 2 - r.top };
}
cv.addEventListener('touchstart', ev => {
  dismissViewHint();
  if(ev.touches.length === 1){
    touchMode = 'pan';
    const t = ev.touches[0];
    touchPanFrom = { x: t.clientX, y: t.clientY, ox: viewOX, oy: viewOY };
  } else if(ev.touches.length === 2){
    touchMode = 'pinch';
    pinchStartDist = touchDist(ev.touches[0], ev.touches[1]) || 1;
    pinchStartScale = viewScale;
  }
}, { passive: true });
cv.addEventListener('touchmove', ev => {
  if(touchMode === 'pan' && ev.touches.length === 1){
    ev.preventDefault();
    const t = ev.touches[0];
    const dx = t.clientX - touchPanFrom.x, dy = t.clientY - touchPanFrom.y;
    if(!viewDidPan && Math.hypot(dx, dy) < 4) return;
    viewDidPan = true;
    viewOX = touchPanFrom.ox + dx; viewOY = touchPanFrom.oy + dy;
  } else if(touchMode === 'pinch' && ev.touches.length === 2){
    ev.preventDefault();
    const dist = touchDist(ev.touches[0], ev.touches[1]) || 1;
    const mid = touchMidCanvas(ev.touches[0], ev.touches[1]);
    const targetScale = pinchStartScale * (dist / pinchStartDist);
    zoomAt(mid.x, mid.y, targetScale / viewScale);
  }
}, { passive: false });
cv.addEventListener('touchend', ev => {
  if(ev.touches.length === 0){
    touchMode = null; touchPanFrom = null;
    // Same leak guard as the mouse path: a touch-pan sets viewDidPan but
    // preventDefault suppresses the synthetic mouseup that would clear it, so
    // clear it here (deferred, for symmetry) or the NEXT tap-to-open is eaten.
    setTimeout(() => { viewDidPan = false; }, 0);
  } else if(ev.touches.length === 1){
    // dropped from a pinch to a single finger — restart the pan baseline
    // from here rather than jumping using the old pinch anchor.
    touchMode = 'pan';
    const t = ev.touches[0];
    touchPanFrom = { x: t.clientX, y: t.clientY, ox: viewOX, oy: viewOY };
  }
});

// ---- on-screen zoom controls + discoverability hint ----
// Trackpad-less mice and plain touch taps have no wheel/pinch gesture, so a
// visible +/−/reset cluster is the accessible fallback — keyboard-focusable
// real <button>s with aria-labels, driving the IDENTICAL zoomAt/resetView
// transform (+/− anchor on the canvas CENTER, since there's no cursor to
// anchor on). Injected here, not in index.html/Hero.astro, so every embed
// (product/replay/site/observer) gets it for free from the shared renderer.
(function initViewControls(){
  const host = cv.parentElement;
  if(!host) return;
  if(getComputedStyle(host).position === 'static') host.style.position = 'relative';

  const style = document.createElement('style');
  style.textContent = `
/* Top-right, not bottom-right: index.html's #legend already owns the
   bottom-right corner of the canvas (bottom:8px; right:10px), and a
   z-index fight there would either hide the legend or crowd the buttons. */
.view-controls { position:absolute; right:10px; top:10px; z-index:5; display:flex; gap:6px; }
.view-controls button {
  width:28px; height:28px; padding:0; border-radius:6px; border:1px solid rgba(255,255,255,.25);
  background:rgba(20,20,20,.55); color:#eee; font-size:15px; line-height:1; cursor:pointer;
  display:flex; align-items:center; justify-content:center; backdrop-filter:blur(2px);
}
.view-controls button:hover, .view-controls button:focus-visible {
  background:rgba(40,40,40,.75); border-color:rgba(255,255,255,.5); outline:none;
}
/* No html.light override here on purpose — the stage stays dark in BOTH
   chrome themes (see index.html's #center), so these overlay controls keep
   their dark-glass styling regardless of the toggle. */
.view-hint {
  position:absolute; right:10px; top:44px; z-index:5; font-size:11px; color:#eee;
  background:rgba(20,20,20,.55); padding:3px 8px; border-radius:5px; pointer-events:none;
  opacity:1; transition:opacity 1.1s ease; white-space:nowrap;
}
.view-hint.hide { opacity:0; }
`;
  document.head.appendChild(style);

  const controls = document.createElement('div');
  controls.className = 'view-controls';
  controls.innerHTML =
    '<button type="button" aria-label="Zoom out">−</button>' +
    '<button type="button" aria-label="Reset zoom">⤢</button>' +
    '<button type="button" aria-label="Zoom in">+</button>';
  const [outBtn, resetBtn, inBtn] = controls.querySelectorAll('button');
  outBtn.title = 'Zoom out'; resetBtn.title = 'Reset view'; inBtn.title = 'Zoom in';
  outBtn.addEventListener('click', () => { dismissViewHint(); zoomAt(CW/2, CH/2, 1/1.3); });
  inBtn.addEventListener('click', () => { dismissViewHint(); zoomAt(CW/2, CH/2, 1.3); });
  resetBtn.addEventListener('click', () => { dismissViewHint(); resetView(); });
  host.appendChild(controls);

  const hint = document.createElement('div');
  hint.className = 'view-hint';
  hint.textContent = 'scroll or pinch to zoom · drag to pan';
  host.appendChild(hint);
  window.dismissViewHint = () => { hint.classList.add('hide'); };
  setTimeout(() => hint.classList.add('hide'), 4000);
})();
function dismissViewHint(){ if(window.dismissViewHint) window.dismissViewHint(); }

// ---- themed canvas backgrounds: grass (ranch/flock), honeycomb (hive) are
// pre-rendered offscreen; matrix rain animates in draw(). The stage is always
// dark (see index.html's #center — framed dark viewport in BOTH chrome
// themes), so these are tuned once for a dark ground and do NOT fork on the
// chrome's light/dark toggle.
const bgCv = document.createElement('canvas'); const bgCtx = bgCv.getContext('2d');
let rainDrops = [];
function renderBg(){
  bgCv.width = Math.max(1, CW); bgCv.height = Math.max(1, CH);
  bgCtx.clearRect(0,0,CW,CH);
  const kind = skin().bg;
  if(kind==='grass'){
    const rgb = '110,190,110';
    for(let i=0;i<Math.floor(CW*CH/9000);i++){
      const x = Math.random()*CW, y = Math.random()*CH, h = 6+Math.random()*9;
      bgCtx.strokeStyle = 'rgba('+rgb+','+(0.25+Math.random()*0.14)+')'; bgCtx.lineWidth=1.4;
      for(const dx of [-2,0,2]){
        bgCtx.beginPath(); bgCtx.moveTo(x,y); bgCtx.quadraticCurveTo(x+dx, y-h*0.6, x+dx*1.8, y-h); bgCtx.stroke();
      }
    }
  } else if(kind==='comb'){
    const rgb = '244,196,48';
    const r=16, w=r*Math.sqrt(3);
    bgCtx.strokeStyle='rgba('+rgb+',0.18)'; bgCtx.lineWidth=1.2;
    for(let row=0; row*1.5*r<CH+2*r; row++){
      for(let col=0; col*w<CW+w; col++){
        const cx = col*w + (row%2? w/2:0), cy = row*1.5*r;
        bgCtx.beginPath();
        for(let k=0;k<6;k++){ const a=Math.PI/3*k+Math.PI/6; const px=cx+r*Math.cos(a), py=cy+r*Math.sin(a); k?bgCtx.lineTo(px,py):bgCtx.moveTo(px,py); }
        bgCtx.closePath(); bgCtx.stroke();
      }
    }
  } else if(kind==='rain'){
    rainDrops = [];
    for(let i=0;i<Math.floor(CW/26);i++) rainDrops.push({x:i*26+6, y:Math.random()*CH, v:40+Math.random()*90});
  }
}
new ResizeObserver(()=>{ try{ renderBg(); }catch(_){} }).observe(cv);
const RAIN_GLYPHS = '01コラルｱｲｳｴｵｶｷｸ<>{}#$';
function drawRain(dt){
  bgCtx.clearRect(0,0,CW,CH); // rain redraws its own layer each frame
  bgCtx.font='13px ui-monospace,monospace';
  const rgb = '90,240,130';
  for(const d of rainDrops){
    d.y += d.v*dt; if(d.y > CH+80) { d.y = -20; d.v = 40+Math.random()*90; }
    for(let k=0;k<8;k++){
      bgCtx.fillStyle = 'rgba('+rgb+','+(0.45 - k*0.055)+')';
      bgCtx.fillText(RAIN_GLYPHS[(Math.random()*RAIN_GLYPHS.length)|0], d.x, d.y - k*13);
    }
  }
}

const nodes = new Map();   // id -> {id,kind,label,x,y,vx,vy,last,conflict}
const links = [];          // {a,b,conflict}
const bursts = [];         // execution ring-bursts: {x,y,t0,ok,timed} — fired when a bee's real command lands
const buzzes = [];         // little "buzz.." sound-effect texts that float up from active bees
// ---- skins: pluggable critter themes. The corral metaphor is the default —
// workers are the herd being corralled, the brain wrangles them; claims render
// as lassos. 'hive' keeps the original hand-drawn bees for the nostalgic.
const SKINS = {
  ranch: { label:'🤠 ranch', brain:'🤠', worker:'🐴', roles:{scrum:'🐕', client:'🧑‍💼'}, sub:'🐎', subtitle:'— the corral', bg:'grass', tab:'corral', empty:'no agents in the corral yet', warming:'the herd is saddling up…',
           center:'the corral', lasso:true, favicon:'🤠', proposes:'the herd proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'pony', nouns:'ponies',
           sounds:['neigh~','clop clop','*swish*','yeehaw~'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Can’t outride the brand 🤠','No proof, no pass — re-doing it'],
           brainTag:'the wrangler — MCP coordinator · task queue · verification gate', replay:'🤠 riding the tape back' },
  flock: { label:'🐑 flock', brain:'🐕', worker:'🐑', roles:{scrum:'🐕‍🦺', client:'🧑‍🌾'}, sub:'🐑', subtitle:'— the fold', bg:'grass', tab:'fold', empty:'no agents in the fold yet', warming:'the flock is gathering…',
           center:'the fold', lasso:false, favicon:'🐑', proposes:'the flock proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'sheep', nouns:'sheep',
           sounds:['baa~','baaa..','*nibble*','baa!'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Fleeced! No {cmd} run…','No proof, no pass — re-doing it'],
           brainTag:'the sheepdog — MCP coordinator · task queue · verification gate', replay:'🐑 reliving the fold' },
  matrix:{ label:'🕶 matrix', brain:'👁️', worker:'🕶️', roles:{scrum:'📟', client:'🥄'}, sub:'🐇', subtitle:'— the construct', bg:'rain', tab:'construct', honorific:'Mr. ', empty:'no Agents in the construct yet', warming:'loading the construct…',
           center:'the construct', lasso:false, favicon:'🕶️', proposes:'the construct proposes:', proposalsTab:'anomalies', completedTab:'archive', noun:'Agent', nouns:'Agents',
           sounds:['0101..','⋮⋮⋮','*dodge*','follow the 🐇'],
           caught:['There is no {cmd} on record','Busted! Never ran {cmd}…','The gate sees all 👁️','No proof, no pass — re-doing it'],
           brainTag:'the architect — MCP coordinator · task queue · verification gate', replay:'🕶️ replaying the construct' },
  hive:  { label:'🐝 hive', brain:null, worker:null, roles:{}, sub:null, subtitle:'— the hive', bg:'comb', tab:'hive', empty:'no bees in the hive yet', warming:'the bees are warming up…',
           center:'the corral', lasso:false, favicon:'🐝', proposes:'the hive proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'bee', nouns:'bees',
           sounds:['buzz..','bzz..','bzzz~','buzz~'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Can’t fake the buzz 🐝','No proof, no pass — re-doing it'],
           brainTag:'the queen — MCP coordinator · task queue · verification gate', replay:'🐝 replaying the hive' },
};
// A host page can PIN the skin by setting data-skin-lock on <html> before this
// script loads. The marketing landing hero uses this so it always renders the
// branded 'ranch' look, regardless of a skin the visitor picked on /recordings
// — that pick persists under the shared corral-skin key on the same origin, so
// without the lock it would leak into the hero on the next load. When locked we
// also skip persistence: switching a skin on a locked page previews it but
// never overwrites the choice made on an unlocked page.
const SKIN_LOCK = (function(){ try{ return document.documentElement.getAttribute('data-skin-lock') || ''; }catch(_){ return ''; } })();
let skinName = SKIN_LOCK || localStorage.getItem('corral-skin') || 'ranch';
if(!SKINS[skinName]) skinName = 'ranch';
function skin(){ return SKINS[skinName]; }
function setSkin(n){
  if(!SKINS[n]) return;
  skinName = n; if(!SKIN_LOCK) localStorage.setItem('corral-skin', n);
  // Drive the visual palette: data-skin on <html> lets CSS override the
  // theme-invariant --stage-* tokens per skin (see the site's global.css and
  // the product's own stylesheet), so the canvas + HUD + panels + console +
  // agent windows + file-tree all re-theme in one move (matrix → green
  // phosphor, etc.). readColors() re-samples them for the next canvas paint.
  try{ document.documentElement.setAttribute('data-skin', n); }catch(_){}
  const fav = document.querySelector('link[rel="icon"]');
  if(fav) fav.href = "data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><text y='.9em' font-size='90'>" + skin().favicon + "</text></svg>";
  const sel = document.getElementById('skinsel'); if(sel) sel.value = n;
  document.title = 'CorralAI ' + skin().subtitle;
  const sub = document.getElementById('skinsub'); if(sub) sub.textContent = skin().subtitle;
  const tab = document.getElementById('tab-swarm'); if(tab) tab.textContent = skin().tab;
  const ptab = document.getElementById('tab-proposals');
  if(ptab){ const badge = document.getElementById('proposals-badge');
    ptab.firstChild.textContent = skin().proposalsTab || 'proposals';
    if(badge && !ptab.contains(badge)) ptab.appendChild(badge); }
  const ctab = document.getElementById('tab-completed');
  if(ctab) ctab.textContent = skin().completedTab || 'completed';
  const emp = document.getElementById('empty'); if(emp) emp.textContent = skin().empty;
  const rtitle = document.getElementById('replay-title');
  if(rtitle) rtitle.textContent = skin().replay || SKINS.ranch.replay;
  // guard: the load-time skin IIFE runs before `let C` is initialized (TDZ),
  // so readColors() throws that first time — harmless, applyTheme() below reads
  // colors once C exists; every later user-driven setSkin re-tints for real.
  try{ readColors(); }catch(_){}
  try{ renderBg(); }catch(_){}
  try{ renderExec(); }catch(_){ /* repaint the console's idle line in the new skin's voice */ }
  try{ renderTopology(); }catch(_){ /* first paint may precede state init */ }
  try{ renderProposalsTab(); }catch(_){ /* first paint may precede state init */ }
  // No completed-tab re-render here: its pane content is skin-neutral (only
  // the tab label is flavored, set above), and re-rendering would refetch
  // /api/history for nothing.
}
function critterGlyph(role, sub){
  const s = skin();
  if(!s.worker) return null;                    // hive: vector bees
  return s.roles[role] || (sub ? s.sub : s.worker);
}
function skinSounds(){ return skin().sounds; }

// drawBubble: comic bubble behind a line of text. Speech = rounded rect with a
// pointed tail; thought = rounded rect with two trailing dots. Caught = red rim.
function drawBubble(x, y, w, h, alpha, caught, thought){
  const r = 8;
  ctx.beginPath();
  ctx.moveTo(x+r, y);
  ctx.arcTo(x+w, y,   x+w, y+h, r); ctx.arcTo(x+w, y+h, x,   y+h, r);
  ctx.arcTo(x,   y+h, x,   y,   r); ctx.arcTo(x,   y,   x+w, y,   r);
  ctx.closePath();
  ctx.fillStyle = hexA(C.panel || '#101318', .92*alpha);
  ctx.fill();
  ctx.strokeStyle = hexA(caught ? C.red : C.line, .9*alpha); ctx.lineWidth = caught ? 1.6 : 1; ctx.stroke();
  if(thought){
    ctx.fillStyle = hexA(C.line, .8*alpha);
    ctx.beginPath(); ctx.arc(x+6, y+h+5, 2.5, 0, 7); ctx.fill();
    ctx.beginPath(); ctx.arc(x+1, y+h+10, 1.5, 0, 7); ctx.fill();
  } else {
    ctx.beginPath();
    ctx.moveTo(x+10, y+h); ctx.lineTo(x+4, y+h+8); ctx.lineTo(x+18, y+h);
    ctx.closePath();
    ctx.fillStyle = hexA(C.panel || '#101318', .92*alpha); ctx.fill();
    ctx.strokeStyle = hexA(caught ? C.red : C.line, .9*alpha); ctx.stroke();
  }
}

// requestChatter: in-character, MODEL-GENERATED speech grounded in the agent's
// real latest activity ("you are Tess, a sheep in the flock; you just ran go
// test…"). Server-cached + rate-limited; canned role lines are the fallback
// when the narrator is off, AND the only path taken during replay (see
// `inReplay` below) — replaying a recorded mission must never touch a brain
// endpoint, matching startReplay()'s "EMBED-FRIENDLY BY CONSTRUCTION" promise
// that a static, backend-less embed never calls out to a live server. Client
// throttles to one fetch per agent per 45s outside replay.
const chatterAt = {};   // agent -> last fetch ts
function requestChatter(n){
  const nowS = Date.now()/1000;
  if(inReplay || (chatterAt[n.label] && nowS - chatterAt[n.label] < 45)){
    const lines = roleQuips(n.role);
    if(lines) buzzes.push({x:n.x+6, y:n.y-15, t0:nowS, txt:lines[(Math.random()*lines.length)|0], say:true, role:n.role, life:2.5});
    return;
  }
  chatterAt[n.label] = nowS;
  fetch('/api/chatter?agent='+encodeURIComponent(n.label)+'&skin='+encodeURIComponent(skinName))
    .then(r => r.ok ? r.json() : null)
    .then(j => {
      if(j && j.line) buzzes.push({x:n.x+6, y:n.y-15, t0:Date.now()/1000, txt:j.line, say:true, role:n.role, life:3.5});
      else {
        const lines = roleQuips(n.role);
        if(lines) buzzes.push({x:n.x+6, y:n.y-15, t0:Date.now()/1000, txt:lines[(Math.random()*lines.length)|0], say:true, role:n.role, life:2.5});
      }
    }).catch(()=>{});
}

// populate the skin selector + apply the persisted choice
(function(){
  const sel = document.getElementById('skinsel');
  if(sel){
    for(const k of Object.keys(SKINS)){
      const o = document.createElement('option'); o.value = k; o.textContent = SKINS[k].label; sel.appendChild(o);
    }
  }
  setSkin(skinName);
})();
// role-appropriate chatter, translated into EACH skin's own universe — not
// suppressed, translated (the corral's design intent: a builder still riffs
// on its name/role, just in-persona). 'hive' keeps the original bee puns
// verbatim; ranch/flock/matrix carry the same beats reworded so no skin ever
// leaks another skin's vocabulary. matrix's builder line is a fixed, exact
// beat ("Can we fix it, Mr. Anderson?") — a deliberate, non-random riff on
// Bob-the-Builder translated into the construct's honorific.
const ROLE_LINES = {
  ranch: {
    researcher:['trailing fresh sign','scouting the range','tracking down leads','riding the back forty'],
    designer:['laying out the corral','keeping the fence lines clean',"this brand's solid","what's the layout?"],
    builder:['Can we build it? Yes we can!','breaking this bronco in','one more rail set','nailing down the gate'],
    tester:['clean as a whistle','no burrs in this saddle','all green on the range','herd tests pass'],
    pentester:['found a real varmint here',"snuffing out that snake","that bite'll leave a mark",'watch for rattlers'],
    perf:['fast as a quarter horse','slow as molasses','56 ns/op — quick on the draw','riding hard'],
    integrator:['branding it all together','merging the herd','the corral connects','rebase incoming'],
    writer:['writing up the trail log','needs a README, partner','spelling it out plain','add an example'],
    reviewer:['looks good to ride','needs changes, partner','nit: keep it tidy','did you test this?'],
    lead:['to the corral!','re-routing the herd','ship it, cowpokes',"who's wrangling this?"],
    client:['make it sweeter','mighty fine work!','a bit more polish please','not quite ready to brand'],
  },
  flock: {
    researcher:['following the scent','grazing for answers','found good pasture','to the flock-mind!'],
    designer:['keeping the fold tidy','wool-even architecture','un-baa-lievably neat',"what's the fold shape?"],
    builder:['Can we fix it?','ewe-ilding…','another fence post set','one more sprint'],
    tester:['sweet, all green','no burrs in MY wool','this test is un-baa-coming','flock tests pass'],
    pentester:['found a real wolf here','fleece that vuln','that bite stings','baa careful with input'],
    perf:["the sheep's knees",'slow as wet wool','56 ns/op — shear speed','fast as a spring lamb'],
    integrator:['fold-ing it all together','merging the flock','the fold connects','rebase incoming'],
    writer:['documenting the fold','needs a README, ewe','spelling it baa by baa','add an example'],
    reviewer:['looks good to ewe','needs changes, lamb','nit: baa consistent','did you test this?'],
    lead:['to the fold!','re-routing the flock','ship it, sheep',"who's herding this?"],
    client:['make it sweeter','wool-good!','more baa-utiful please','not quite ripe'],
  },
  matrix: {
    researcher:['jacking into the archives','tracing the signal','found a clean thread','back to the source'],
    designer:['architecting the construct','keeping the code clean','this pattern holds',"what's the structure?"],
    builder:['Can we fix it, Mr. Anderson?','compiling the construct…','another module locked','one more cycle'],
    tester:['clean run, no glitches','no bugs in this simulation','all green in the matrix','construct tests pass'],
    pentester:['found a real exploit here','patching that vulnerability','that exploit stings','watch the code'],
    perf:['faster than a bullet dodge','slow as lagged frames','56 ns/op — bullet time','fast as the construct'],
    integrator:['compiling it all together','merging the construct','the signal connects','rebase incoming'],
    writer:['documenting the construct','needs a README, operator','spelling it out in code','add an example'],
    reviewer:['looks clean, approved','needs changes, operator','nit: keep it consistent','did you test this?'],
    lead:['to the construct!','re-routing the signal','ship it, operators',"who's tracing this?"],
    client:['make it sharper','impressive work!','a bit more refinement please','not quite there'],
  },
  hive: {
    researcher:['bee-lining to the docs','scouting for nectar','found a sweet source','to the hive-mind!'],
    designer:['honeycomb architecture','keep the comb clean','un-bee-lievably tidy',"what's the cell shape?"],
    builder:['Can we fix it?','bee-uilding…','another cell capped','one more sprint'],
    tester:['sweet, all green','no bugs in MY comb','this test is un-bee-coming','swarm tests pass'],
    pentester:['Found a real bug here guys','swat that vuln','that bug stings','bee careful with input'],
    perf:["the bee's knees",'slow as cold honey','56 ns/op — buzz-worthy','fast as wingbeats'],
    integrator:['comb-ining it all','merging the swarm','the hive connects','rebase incoming'],
    writer:['documenting the hive','needs a README, honey','spelling it bee by bee','add an example'],
    reviewer:['looks good to bee','needs changes, drone','nit: bee consistent','did you test this?'],
    lead:['to the hive!','re-routing the swarm','ship it, bees',"who's foraging this?"],
    client:['make it sweeter','honey-good!','more bee-utiful please','not quite ripe'],
  },
};
// roleQuips: the active skin's canned line set for a role, falling back to the
// ranch set (never hive — hive's bee puns must never leak into another skin)
// if the active skin is missing that role.
function roleQuips(role){
  const bySkin = ROLE_LINES[skinName] || ROLE_LINES.ranch;
  return bySkin[role] || ROLE_LINES.ranch[role];
}
let bobSeeded = false; const bobDone = new Set();  // Bob says "Yes we can…" when he finishes a task
// when the verify gate refuses a lazy completion it raises a verify-gate finding;
// the caught bee owns up out loud (honest — driven by the real finding, not random).
const caughtFindings = new Set();
// caught lines live per-skin (skin().caught) — the verify-gate call-out stays in character.
let selected = null, lastState = {active_agents:[],live_claims:[],recent_completed:[]};
// Per-viewer display aliases (localStorage): rename a bee in the GUI WITHOUT
// touching its real identity. The real name keys all coordination, telemetry, and
// recorded history; displayName() is purely cosmetic, applied only at render time.
let beeAlias = {}; try { beeAlias = JSON.parse(localStorage.getItem('beeAlias')||'{}') || {}; } catch(_){ beeAlias = {}; }
function displayName(real){ const n = (real && beeAlias[real]) || real; return n ? (skin().honorific||'')+n : n; }
function renameBee(real){
  if(!real) return;
  const v = prompt('Rename '+real+' — display only, this browser. Leave blank to reset:', beeAlias[real]||'');
  if(v===null) return;
  const t = v.trim();
  if(t && t!==real) beeAlias[real]=t; else delete beeAlias[real];
  try{ localStorage.setItem('beeAlias', JSON.stringify(beeAlias)); }catch(_){}
  renderInspector();
}
let serverNow = 0, parkedGrace = 300, stateClientT = 0;
function liveServerNow(){ return serverNow + (performance.now()/1000 - stateClientT); } // skew-free elapsed

// agents are tinted by their swarm role so builder/tester/pentester/reviewer are
// instantly distinguishable; roleless agents fall back to the default amber.
const ROLE_COLORS = {researcher:'#e6c84f', designer:'#4fc3d9', builder:'#5b9bd5', tester:'#5ec98a', pentester:'#e0913a', perf:'#d96fb0', integrator:'#7b8cde', writer:'#9fb06a', reviewer:'#b07cd6'};
function roleColor(role){ return ROLE_COLORS[role] || C.amber; }

// severity color/rank — shared by the live findings panel (index.html) and
// the replay cockpit's findings renderer below.
function sevColor(sev){ return (sev==='critical'||sev==='high')?C.red:(sev==='medium'?C.amber:'#6b6452'); }
const SEV_RANK = {critical:3, high:2, medium:1, low:0};

// ---- theme (light / dark) ----
let C = {};
function hexA(hex, a){ const h=(hex||'#888').replace('#','').trim(); const n=parseInt(h.length===3?h.replace(/./g,'$&$&'):h,16); return `rgba(${(n>>16)&255},${(n>>8)&255},${n&255},${a})`; }
// C drives EVERY swarm-canvas draw — agent labels, selection ring, conflict/
// quorum halos, claimed-path dots+labels, burst rings, speech/thought bubbles.
// The canvas is a permanently-dark stage in BOTH chrome themes (see #center),
// so C MUST source from the theme-invariant --stage-* palette, NOT the chrome
// --fg/--muted/… tokens. Reading the chrome set painted near-black label text
// and rings on the dark stage in light mode = invisible nodes. --stage-* is
// dark in both themes (never overridden by html.light), so labels/rings stay
// legible whichever chrome theme is active.
function readColors(){ const s=getComputedStyle(document.getElementById('stage-frame')||document.documentElement), g=k=>s.getPropertyValue(k).trim();
  C={fg:g('--stage-fg'),muted:g('--stage-muted'),amber:g('--stage-amber'),red:g('--stage-red'),line:g('--stage-line'),green:g('--stage-green'),panel:g('--stage-panel')}; }
function applyTheme(th){ document.documentElement.classList.toggle('light', th==='light');
  const b=document.getElementById('themebtn');
  if(b){ b.textContent = th==='light'?'☀':'☾'; b.setAttribute('aria-label', th==='light'?'Switch to dark theme':'Switch to light theme'); }
  readColors();
  try{ renderBg(); }catch(_){} }
function toggleTheme(){ const th=document.documentElement.classList.contains('light')?'dark':'light';
  try{ localStorage.setItem('corral-theme', th); }catch(_){} applyTheme(th); }
// Default: an explicit prior choice wins; otherwise follow the OS/browser's
// prefers-color-scheme on first load, falling back to dark if that API is
// unavailable (matches the chrome's own dark-first design).
applyTheme((()=>{ try{ return localStorage.getItem('corral-theme'); }catch(_){ return null; } })()
  || (()=>{ try{ return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'; }catch(_){ return 'dark'; } })());
function ensure(id, kind, label){
  let n = nodes.get(id);
  if(!n){ n = {id,kind,label, x: CW/2 + (Math.random()-.5)*200, y: CH/2 + (Math.random()-.5)*200, vx:0, vy:0, last:0, conflict:false, phase: Math.random()*6.283, dartCd: 0, working:false, trail: []}; nodes.set(id,n); }
  return n;
}

function esc(s){ return (s||'').replace(/[&<>"']/g, m=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[m])); }

// renderFaultDiff / parseMutants: the FAULT HIGHLIGHT — the founder's key
// transparency affordance ("here is the exact fault, and your tests pass
// anyway"). A mutant is a same-signature drop-in of the code under review, so
// a line diff of the original vs a surviving mutant isolates the planted
// fault. No diff library exists on the tape side (esc() above is the only
// primitive), so this is a minimal, dependency-free, O(n) line diff:
// membership-against-the-original-line-set is an order-tolerant "is this
// line new" test — good enough for a small mutation (a line present ANYWHERE
// in the original is NOT a fault). A positional LCS diff is a fair follow-up
// if false highlights ever show up on a real mutant, but it isn't needed for
// the demo's single-region mutations.
function renderFaultDiff(original, mutant){
  const o = (original||'').split('\n'), m = (mutant||'').split('\n');
  const oset = new Set(o.map(l => l.trim()));
  const rows = m.map(line => {
    const changed = line.trim() !== '' && !oset.has(line.trim());
    const cell = esc(line);
    return changed ? '<span class="faultline">' + cell + '</span>' : cell;
  });
  return '<pre class="aw-result faultdiff">' + rows.join('\n') + '</pre>';
}
// parseMutants: split a mutant-generator task_done.result into its
// ===MUTATION_n=== blocks — {id, code}[] in tape order. Tolerant of a result
// with no blocks at all (returns []), which is the signal to fall back to
// the plain result <pre> (Task 2's behavior, unchanged for non-mutant tasks
// or a mutant-gen result the tape didn't shape this way).
//
// IDs must mirror internal/testgen/parse.go's parseMutants EXACTLY: "m"+
// positional-index (1-based) over only the NON-EMPTY blocks — NOT the raw
// marker token (a marker of "===MUTATION_1===" does NOT mean id "1"). The
// tape's pool_dev_adequacy.survivor_ids are the Go-assigned ids ("m1","m2",
// ...), so a mismatched id scheme here silently breaks survivor matching
// (see the honesty-fix commit that introduced this comment).
function parseMutants(result){
  const mark = '===MUTATION_';
  const parts = (result || '').split(mark);
  const out = [];
  for(let i = 1; i < parts.length; i++){ // parts[0] is any preamble before the first marker
    const p = parts[i];
    // p looks like "1===\n<code>...": drop up to and including the marker's closing "==="
    const close = p.indexOf('===');
    if(close < 0) continue;
    let body = p.slice(close + 3);
    // A trailing "..._END===" (or the next marker, already split off) may remain — cut at any residual "===".
    const end = body.indexOf('===');
    if(end >= 0) body = body.slice(0, end);
    const code = body.trim();
    if(code === '') continue;
    out.push({id: 'm' + (out.length + 1), code});
  }
  return out;
}

// simple force layout
function step(){
  const arr = [...nodes.values()];
  const cx = CW/2, cy = CH/2;
  for(const n of arr){
    let fx=(cx-n.x)*0.0015, fy=(cy-n.y)*0.0015;     // gravity
    for(const m of arr){ if(m===n) continue;
      let dx=n.x-m.x, dy=n.y-m.y, d2=dx*dx+dy*dy+0.01, d=Math.sqrt(d2);
      const rep = 2600/d2; fx += dx/d*rep; fy += dy/d*rep; }   // repulsion
    n.vx=(n.vx+fx)*0.82; n.vy=(n.vy+fy)*0.82;
  }
  for(const l of links){
    let dx=l.b.x-l.a.x, dy=l.b.y-l.a.y, d=Math.sqrt(dx*dx+dy*dy)+0.01, f=(d-90)*0.01;
    l.a.vx+=dx/d*f; l.a.vy+=dy/d*f; l.b.vx-=dx/d*f; l.b.vy-=dy/d*f; }
  // busy bees never stand still: a continuous organic wander keeps every bee in
  // motion (layered sines = a wandering flight path), and a WORKING bee is frenetic
  // — bigger amplitude plus occasional forage "darts" to a new spot. Idle bees still
  // hover gently. Motion amplitude IS the honesty signal: the harder a bee moves,
  // the more it's actually doing.
  const tt = Date.now()/1000;
  for(const n of arr){
    if(n.kind==='agent'){
      const e = n.working ? 1 : 0.36, ph = n.phase||0;
      n.vx += (Math.sin(tt*2.3+ph) + 0.6*Math.sin(tt*5.7+ph*1.7)) * 0.17 * e;
      n.vy += (Math.cos(tt*1.9+ph*1.3) + 0.6*Math.sin(tt*4.9+ph*0.7)) * 0.17 * e;
      if(n.working && (n.dartCd=(n.dartCd||0)-1) <= 0){    // forage: zip to a new flower
        const a=Math.random()*6.283, k=1.6+Math.random()*1.9;
        n.vx+=Math.cos(a)*k; n.vy+=Math.sin(a)*k; n.dartCd=32+Math.random()*64;
      }
    }
    n.x+=n.vx; n.y+=n.vy;
  }
}

// drawBee: a little role-colored bee — striped body, flapping wings, antennae.
// The queen (the brain at the heart of the corral) gets a crown.
function drawBee(x, y, r, color, t, queen){
  const wf = 0.5 + 0.5*Math.abs(Math.sin(t*6));          // wing flap — a busy flutter
  ctx.fillStyle = hexA('#dbe9ff', .52); ctx.strokeStyle = hexA('#ffffff', .4); ctx.lineWidth = 0.7;  // wings
  ctx.beginPath(); ctx.ellipse(x - r*0.72, y - r*0.38, r*1.08*wf, r*0.62, -0.7, 0, 7); ctx.fill(); ctx.stroke();
  ctx.beginPath(); ctx.ellipse(x + r*0.72, y - r*0.38, r*1.08*wf, r*0.62,  0.7, 0, 7); ctx.fill(); ctx.stroke();
  ctx.save();                                            // striped body (clip)
  ctx.beginPath(); ctx.ellipse(x, y, r*0.92, r*1.2, 0, 0, 7); ctx.clip();
  ctx.fillStyle = color; ctx.fillRect(x - r*1.3, y - r*1.4, r*2.6, r*2.8);
  ctx.fillStyle = hexA('#1c1208', .82);
  for(const dy of [-0.5, 0, 0.5]) ctx.fillRect(x - r*1.3, y + dy*r*1.2 - r*0.16, r*2.6, r*0.32);
  ctx.restore();
  ctx.beginPath(); ctx.ellipse(x, y, r*0.92, r*1.2, 0, 0, 7);
  ctx.strokeStyle = hexA('#1c1208', .5); ctx.lineWidth = 1; ctx.stroke();
  ctx.beginPath(); ctx.arc(x, y - r*1.25, r*0.42, 0, 7); ctx.fillStyle = '#241a10'; ctx.fill(); // head
  ctx.strokeStyle = '#241a10'; ctx.lineWidth = 1;        // antennae
  ctx.beginPath(); ctx.moveTo(x - r*0.2, y - r*1.55); ctx.lineTo(x - r*0.55, y - r*2.1);
  ctx.moveTo(x + r*0.2, y - r*1.55); ctx.lineTo(x + r*0.55, y - r*2.1); ctx.stroke();
  if(queen){                                             // a little gold crown
    const cw = r*1.8, cy = y - r*2.15;
    ctx.fillStyle = '#f4c430';
    ctx.beginPath(); ctx.moveTo(x - cw/2, cy);
    ctx.lineTo(x - cw/2, cy - r*0.55); ctx.lineTo(x - cw*0.25, cy - r*0.12);
    ctx.lineTo(x, cy - r*0.78); ctx.lineTo(x + cw*0.25, cy - r*0.12);
    ctx.lineTo(x + cw/2, cy - r*0.55); ctx.lineTo(x + cw/2, cy);
    ctx.closePath(); ctx.fill();
  }
}

// drawCritter: the themed node renderer. Emoji skins get a role-colored halo
// ring (role color survives any skin) + a gently bobbing critter; the hive
// skin falls through to the original hand-drawn bee.
function drawCritter(x, y, r, color, t, isBrain, role, sub){
  const g = isBrain ? skin().brain : critterGlyph(role, sub);
  if(!g){ drawBee(x, y, r, color, t, isBrain); return; }
  ctx.beginPath(); ctx.arc(x, y, r*1.35, 0, 7);
  ctx.strokeStyle = hexA(color, .85); ctx.lineWidth = isBrain ? 2 : 1.5; ctx.stroke();
  const bob = Math.sin(t*3 + x)*r*0.12;
  ctx.font = (r*2.1) + 'px serif'; ctx.textAlign='center'; ctx.textBaseline='middle';
  ctx.fillText(g, x, y + bob); ctx.textAlign='left'; ctx.textBaseline='alphabetic';
}

let lastFrameT = Date.now()/1000;
function draw(){
  step();
  // clear in DEVICE space (identity-ish base transform), THEN apply the
  // world transform once for everything that lives in the corral — the
  // background included (natural zoom: the pasture is part of the world;
  // beyond its edge the panel color shows, which reads as "the pasture
  // ends", not as a glitch). Per-node math stays untouched.
  ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
  ctx.clearRect(0, 0, CW, CH);
  const frameT = Date.now()/1000, dt = Math.min(0.1, frameT-lastFrameT); lastFrameT = frameT;
  ctx.setTransform(devicePixelRatio*viewScale, 0, 0, devicePixelRatio*viewScale, devicePixelRatio*viewOX, devicePixelRatio*viewOY);
  if(skin().bg==='rain') drawRain(dt);
  if(bgCv.width>1) ctx.drawImage(bgCv, 0, 0, CW, CH);
  for(const l of links){
    let parkedLabel = null;
    if(l.parked){
      const remaining = parkedGrace - (liveServerNow() - (l.since||0));
      const open = remaining <= 0;
      ctx.strokeStyle = hexA(open ? C.red : C.amber, .85);
      ctx.lineWidth = 2; ctx.setLineDash(open ? [2,4] : [5,4]);
      parkedLabel = open ? 'lease open to peers' : 'parked ' + Math.ceil(remaining) + 's';
    } else if(l.sub){ ctx.strokeStyle=hexA(C.amber,.35); ctx.lineWidth=1; ctx.setLineDash([3,3]); }
    else if(skin().lasso && !l.sub && !l.conflict){
      // ranch skin: a claim is a thrown lasso — rope-colored line, loop at the file end
      ctx.strokeStyle = hexA('#c9a86a', .8); ctx.lineWidth = 1.4; ctx.setLineDash([]);
    }
    else { ctx.strokeStyle = l.conflict ? hexA(C.red,.7) : hexA(C.line,.7); ctx.lineWidth = l.conflict ? 2 : 1; ctx.setLineDash([]); }
    ctx.beginPath(); ctx.moveTo(l.a.x,l.a.y); ctx.lineTo(l.b.x,l.b.y); ctx.stroke();
    if(skin().lasso && !l.sub && !l.conflict && !l.parked){
      ctx.beginPath(); ctx.arc(l.b.x, l.b.y, 8, 0, 7);
      ctx.strokeStyle = hexA('#c9a86a', .75); ctx.lineWidth = 1.4; ctx.stroke();
    }
    if(parkedLabel){
      ctx.setLineDash([]);
      ctx.fillStyle = hexA(C.fg, .85); ctx.font='10px ui-sans-serif';
      ctx.fillText(parkedLabel, (l.a.x+l.b.x)/2 + 4, (l.a.y+l.b.y)/2 - 4);
    }
  }
  ctx.setLineDash([]);
  const now = Date.now()/1000, t = now;
  // the queen — the brain at the heart of the corral, with her crown
  if(nodes.size){
    const qx=CW/2, qy=CH/2;
    const qglow = 34 + 7*Math.sin(t*1.4);
    ctx.beginPath(); ctx.arc(qx,qy, qglow, 0, 7); ctx.fillStyle=hexA(C.amber,.10); ctx.fill();
    drawCritter(qx, qy, 16, C.amber, t*0.7, true);
    ctx.fillStyle=hexA(C.muted,.85); ctx.font='11px ui-sans-serif'; ctx.textAlign='center';
    ctx.fillText(skin().center, qx, qy+38); ctx.textAlign='left';
  }
  for(const n of nodes.values()){
    if(n.kind==='agent'){
      const sub = !!n.parent;                              // subagent = smaller child node
      const fresh = Math.max(0, 1-(now-n.last)/300);      // 0..1 recency
      const working = (n.status === 'working');           // actually doing work right now
      n.working = working;                                 // feed the busy-bee physics (next step)
      const core = sub ? 5 : 8;
      const ac = roleColor(n.role);
      // flight trail: a working bee leaves a fading honey-colored path so the eye
      // tracks its motion — the busiest bees draw the longest streaks.
      if(working){ n.trail.push(n.x, n.y); if(n.trail.length>20) n.trail.splice(0,2); }
      else if(n.trail.length){ n.trail.splice(0,2); }
      if(n.trail.length>=4){
        ctx.beginPath(); ctx.moveTo(n.trail[0], n.trail[1]);
        for(let k=2;k<n.trail.length;k+=2) ctx.lineTo(n.trail[k], n.trail[k+1]);
        ctx.strokeStyle=hexA(ac,0.20); ctx.lineWidth=2; ctx.stroke();
      }
      // glow: bright + pulsing when working, faint when idle — so the eye finds the busy bees
      const pulse = (sub?9:14) + (working ? 3*Math.sin(t*3 + n.x) : 0);
      ctx.beginPath(); ctx.arc(n.x,n.y, pulse + (working?6:1)*fresh, 0, 7);
      ctx.fillStyle = hexA(ac, working ? (0.16+0.22*fresh) : 0.05); ctx.fill();
      // idle critters dim out; only the working ones are full color and lit
      ctx.globalAlpha = working ? 1 : 0.38;
      drawCritter(n.x, n.y, core, ac, t + n.x*0.6, false, n.role, sub);
      ctx.globalAlpha = 1;
      // ONLY working agents talk — the animation tells the truth. Speech is
      // model-generated in-character from the agent's REAL latest activity
      // (see requestChatter); the canned role lines are the offline fallback.
      if(working && Math.random()<0.013){
        if(Math.random()<0.5){ requestChatter(n); }
        else buzzes.push({x:n.x+core*0.7, y:n.y-core*1.7, t0:now, txt:skinSounds()[(Math.random()*skinSounds().length)|0], life:1.5});
      }
      const dn = displayName(n.label); const lbl = n.role ? dn+' ['+n.role+']' : dn;
      const parkedNode = n.status === 'awaiting_approval';
      if(parkedNode){
        ctx.beginPath(); ctx.arc(n.x,n.y, core+4, 0, 7);
        ctx.strokeStyle=hexA(C.amber,.95); ctx.lineWidth=2; ctx.setLineDash([2,2]); ctx.stroke(); ctx.setLineDash([]);
      }
      ctx.fillStyle = working ? C.fg : hexA(C.fg,.42); ctx.font=(sub?'11px':'12px')+' ui-sans-serif';
      ctx.fillText((parkedNode?'⏸ ':'')+lbl, n.x+core+4, n.y+4);
    } else {
      // active-work pulse: a file glows green while the agent holding it is fresh
      const cfresh = Math.max(0, 1-(now-(n.claimLast||0))/180);
      if(n.conflict){
        // contested file: a pulsing red alarm so collisions (the clobber!) are unmistakable
        const cp = 9 + 4*Math.abs(Math.sin(t*4 + n.x));
        ctx.beginPath(); ctx.arc(n.x,n.y, cp+5, 0, 7); ctx.fillStyle = hexA(C.red, 0.18); ctx.fill();
        ctx.beginPath(); ctx.arc(n.x,n.y, cp, 0, 7); ctx.strokeStyle = hexA(C.red,.9); ctx.lineWidth = 2; ctx.stroke();
      } else if(cfresh>0.02){
        ctx.beginPath(); ctx.arc(n.x,n.y, 6 + 5*cfresh + 2.5*Math.sin(t*4+n.x), 0, 7);
        ctx.fillStyle = hexA(C.green, 0.10+0.28*cfresh); ctx.fill();
      }
      ctx.beginPath(); ctx.arc(n.x,n.y, 5, 0, 7);
      ctx.fillStyle = n.conflict ? C.red : (cfresh>0.02 ? C.green : C.muted); ctx.fill();
      ctx.fillStyle = n.conflict ? C.red : C.muted; ctx.font='11px ui-monospace,monospace'; ctx.fillText(n.label, n.x+9, n.y+3);
    }
    if(n===selected){ ctx.beginPath(); ctx.arc(n.x,n.y, n.kind==='agent'?(n.parent?12:17):11, 0, 7); ctx.strokeStyle=C.fg; ctx.lineWidth=1.5; ctx.stroke(); }
  }
  // execution bursts: an expanding ring blooms from a bee when its REAL command
  // lands — green on a passing build/test, red on failure, amber on timeout.
  for(let i=bursts.length-1; i>=0; i--){
    const b=bursts[i], age=now-b.t0;
    if(age>1.2 || age<0){ bursts.splice(i,1); continue; }
    const p=age/1.2, r=9+48*p, col=b.timed?C.amber:(b.ok?C.green:C.red);
    ctx.beginPath(); ctx.arc(b.x,b.y, r, 0, 7);
    ctx.strokeStyle=hexA(col,.6*(1-p)); ctx.lineWidth=3*(1-p)+0.5; ctx.stroke();
  }
  // active critters murmur — and SAY things in character. Spoken lines get a
  // speech bubble; ambient murmurs get a thought bubble (cloud + dot tail).
  for(let i=buzzes.length-1; i>=0; i--){
    const b=buzzes[i], life=b.life||1.5, age=now-b.t0;
    if(age>life || age<0){ buzzes.splice(i,1); continue; }
    const p=age/life, alpha=Math.min(1,(1-p)*1.4);
    if(b.say){
      const txt = '“'+b.txt+'”';
      ctx.font = b.caught ? 'bold 13px ui-sans-serif' : '12px ui-sans-serif';
      const w = ctx.measureText(txt).width;
      const bx = b.x + (b.caught?0:2*Math.sin(age*4)), by = b.y - 26*p;
      drawBubble(bx-8, by-16, w+16, 23, alpha, b.caught, false);
      ctx.fillStyle = hexA(b.caught ? C.red : roleColor(b.role), .96*alpha);
      ctx.font = b.caught ? 'bold 13px ui-sans-serif' : '12px ui-sans-serif';
      ctx.fillText(txt, bx, by);
    } else {
      ctx.font='italic 11px ui-sans-serif';
      const w = ctx.measureText(b.txt).width;
      const bx = b.x + 5*Math.sin(age*7), by = b.y - 20*p;
      drawBubble(bx-7, by-14, w+14, 20, alpha*0.8, false, true);
      ctx.fillStyle=hexA(C.amber, .8*alpha);
      ctx.font='italic 11px ui-sans-serif';
      ctx.fillText(b.txt, bx, by);
    }
  }
  requestAnimationFrame(draw);
}
requestAnimationFrame(draw);

// connectSSE: opens (or re-opens) the live /events stream. Factored out so
// closeReplay() can restore the exact same wiring it tears down — no
// duplicated onmessage/onerror pair to drift out of sync.
function connectSSE(){
  const src = new EventSource('/events');
  src.onmessage = e => { try { apply(JSON.parse(e.data)); } catch(_){} };
  src.onerror = () => { const s = document.getElementById('stat'); if (s) s.textContent = 'reconnecting…'; };
  return src;
}
let es = null; // lazy: the embedding page calls `es = connectSSE()` itself for live SSE
// (see the DOM-contract header above) — a static replay embed never does, so `es` stays
// null and every `if(es && ...)` guard below skips cleanly.

// ---- replay player ----
// Replays a mission's recorded beat stream through the SAME apply-adjacent
// canvas machinery (nodes/links/bursts/buzzes, drawn by the one draw() loop
// above) that the live SSE feed drives — positions are never recorded, they
// recompute via the live force layout exactly like a live agent would.
//
// EMBED-FRIENDLY BY CONSTRUCTION: startReplay(streamOrUrl) is the only entry
// point that touches replay state, and it takes either a URL (fetched with a
// plain GET — what the live corral UI uses: '/api/replay?mission='+id) OR an
// already-resolved {events:[...]} object / bare array. Nothing below this
// point calls a brain/SSE endpoint directly or assumes one exists — a static
// corralai.dev embed with no brain running can hand startReplay() a baked
// JSON file's contents and get the identical player. openReplay(missionId)
// is just the live-corral convenience wrapper around that URL form.
const REPLAY_SPEED_KEY = 'corralai-replay-speed';
const DEFAULT_REPLAY_SPEED = 2;
const REPLAY_SPEEDS = [1, 2, 4, 8, 16];
let replayEvents = [], replayIdx = 0, replayPlaying = false, replaySpeed = DEFAULT_REPLAY_SPEED, replayTimer = null, replaySSEPaused = false;
// cvCurrentView: which cockpit tab is open (cockpitView sets it). The scrub
// choke point (renderReplayScrub) reads it to re-derive whichever position-
// dependent lens is showing — progress/topology/completed reconstruct at the
// playhead just like the swarm canvas and files tree, so one scrubber drives
// the WHOLE cockpit through time, not only the canvas.
let cvCurrentView = 'swarm';
function storedReplaySpeed(){
  try {
    const n = Number(localStorage.getItem(REPLAY_SPEED_KEY));
    return REPLAY_SPEEDS.includes(n) ? n : DEFAULT_REPLAY_SPEED;
  } catch(e) { return DEFAULT_REPLAY_SPEED; }
}
// inReplay: true from startReplay() through stopReplaySession() — the single
// switch that keeps replay genuinely backend-free (see requestChatter above).
// Distinct from replaySSEPaused, which only means anything when a live SSE
// connection existed to pause; a static embed never has one, so inReplay is
// the flag that also works with no brain running at all.
let inReplay = false;

// ---- replay cockpit: the tape drives the console/tasks/findings panels ----
// The live panels render from lastState via apply() (SSE) — which is paused
// during replay, so in the product they used to freeze on stale live content
// while the canvas played the tape. These replay-side renderers accumulate
// state from applyReplayEvent and paint the SAME panel DOM (#exec, #tasks,
// #findings — all optional, null-guarded) from the tape instead. People want
// to see action: the whole viewport replays, not just the canvas.
// Honest limits of the tape: execution beats carry ok/exit_code but not the
// live feed's timed_out flag or multi-line output summary, and there is no
// recent_activity tool-call stream — the cockpit shows what was recorded,
// never invents the rest. The agents roster's "doing now" column is
// reconstructed from claims + executions (the tape has no agent_activity
// tool-call beat), so it shows the last real command or the claimed task,
// not a live tool call.
// replayConsoleLines: the console's single chronological feed — execution
// beats AND thought beats interleaved exactly as the tape emits them (both
// are appended here, in the same order applyReplayEvent processes the
// already-ts-sorted stream, so no separate ts-merge is needed: append order
// IS chronological order, for a full play-through and for a seek's
// rebuild-from-0 alike). Each entry is tagged `kind` so the renderer can
// style reasoning distinctly from action — see renderReplayConsole.
//   exec:    {kind:'exec', agent, role, command, ok, exitCode}
//   thought: {kind:'thought', agent, role, text}
let replayConsoleLines = [];
let replayTasks = new Map(); // key -> {key, title, role, status, claimedBy}
let replayFindings = [];    // {reporter, target, type, severity, model, resolved}
// name -> {name, role, held:Set<taskKey>, lastCmd, completed, lastTaskTitle,
//          lastTs, lastKind, lastDesc}. The trailing five fields (added for
//          the agent INSPECTOR WINDOW reconstruction, product-only — see
//          index.html's renderReplayAgentWindowBody) feed "completed",
//          "working on", and "last activity"; the roster (renderReplayAgents,
//          below) only ever reads held/lastCmd, so this is additive.
let replayAgents = new Map();
let replaySeenBeats = new Set(); // dedupe: findings ride the tape TWICE (queue+telemetry merge), and resolutions repeat across replan cycles
// replayExecFilter: isolate one actor's stream in the console (both #exec in
// the product and the site cockpit) — the tape-side twin of index.html's live
// `execFilter`/`setExecFilter`. NOT reset by resetReplayPanels(): it's a
// display filter over the accumulated beats, not accumulated state itself, so
// it must survive a scrub/seek's rebuild-from-0 (the whole point of the
// addendum — isolate BOB, then scrub around and stay isolated on BOB).
let replayExecFilter = '';
function setReplayExecFilter(n){ replayExecFilter = (replayExecFilter === n ? '' : n); renderReplayConsole(); }
// replayFiles: the file-tree lens's reconstructed state — path -> {path,
// holder, lastActor, touches}. Folded in by the v2 replay merge (internal/
// brain/replay.go pulls global claim_made/claim_released beats into the mission
// stream by time-window inclusion). `holder` is the actor CURRENTLY holding the
// claim ('' once released); `lastActor` is whoever last touched it, so a
// released path stays in the tree (dimmed) — the lens shows "who touched what,
// when" (paths only; the tape never captures file contents). Accumulated in
// applyReplayEvent, so it rebuilds-from-0 on scrub exactly like the panels.
let replayFiles = new Map();
// replayPoolSubject / replayDevAdequacy: the advpool's fault-highlight
// inputs, accumulated the same way as replayFiles above — captured in
// applyReplayEvent when their events land, rebuilt-from-0 on scrub.
// replayPoolSubject.code is the ORIGINAL code under review (pool_subject.
// detail.code); replayDevAdequacy.survivor_ids names which planted mutant(s)
// the dev's own tests failed to kill — the fault worth showing (see
// renderFaultDiff / the mutant-generator task-story "result" section below).
let replayPoolSubject = null;
let replayDevAdequacy = null;
function resetReplayPanels(){
  replayConsoleLines = []; replayTasks = new Map(); replayFindings = [];
  replayAgents = new Map(); replaySeenBeats = new Set();
  replayFiles = new Map();
  replayPoolSubject = null; replayDevAdequacy = null;
}
function clearReplayPanelDOM(){
  // 'agents' is the canonical roster id, shared by the product UI and the
  // site cockpit (internal/ui/web/cockpit-shell.html) — cleared here too so
  // the SSE snapshot repaints it fresh on replay exit (no stale-tape flash),
  // same as the other panels.
  for(const id of ['exec','tasks','findings','agents']){
    const el = document.getElementById(id);
    if(el) el.innerHTML = '';
  }
}
function replayAgentEnsure(name, role){
  let a = replayAgents.get(name);
  if(!a){ a = {name, role: role || '', held: new Set(), lastCmd: '', completed: 0, lastTaskTitle: '', lastTs: 0, lastKind: '', lastDesc: ''}; replayAgents.set(name, a); }
  if(role) a.role = role;
  return a;
}
function replayAgentHold(name, role, taskKey, held){
  const a = replayAgentEnsure(name, role);
  if(held) a.held.add(taskKey); else a.held.delete(taskKey);
}
// replayAgentTouch: records the LATEST beat (by ts) for an actor, across
// every kind that counts as "activity" (claim/done/execution/thought) — feeds
// the inspector window's "last activity" line. Ties (equal ts) keep the
// newest-applied beat, which is what a rebuild-from-0 walk always wants.
function replayAgentTouch(name, role, ts, kind, desc){
  const a = replayAgentEnsure(name, role);
  if((ts||0) >= (a.lastTs||0)){ a.lastTs = ts||0; a.lastKind = kind; a.lastDesc = desc || ''; }
  return a;
}
// renderReplayAgents: the roster of workers the tape shows, reconstructed from
// task claims + executions — mirrors the product's live agents list (#agents):
// role-colored dot when working (holds a claim), name + role + the "doing now"
// column (last real command, else the claimed task). System actors that only
// FILE findings (verify-gate, reflex-replanner, lead, client) never claim or
// run a command, so they never enter the roster — same as live active_agents.
function renderReplayAgents(){
  // #agents is the canonical roster id, shared by the product UI and the site
  // cockpit (see internal/ui/web/cockpit-shell.html) — in the product this
  // also fixes the roster going stale during replay (apply() is paused),
  // exactly like the console/tasks/findings panels.
  const ap = document.getElementById('agents');
  if(!ap) return;
  ensureSiteReplayStyles();   // site-only (no-op in the product): roster hover/arowsel + .aw-win chrome
  const ags = Array.from(replayAgents.values());

  // STABLE, IN-PLACE reconciliation — do NOT blow the roster away with a fresh
  // innerHTML every tape tick. The panel repaints on every playback frame and
  // on every scrub; rebuilding innerHTML detaches and re-creates each .arow,
  // so a row can be replaced out from under a user mid-click (the click then
  // lands on an orphaned node and the inspector window never opens — the
  // launch-blocker bug). Instead we reuse a persistent node per agent, keyed by
  // name, patch only the bits that change (dot / name color / role / "doing" /
  // selected-state), and keep a stable alphabetical order so existing rows
  // never reposition frame-to-frame. Node identity (and its click listener)
  // survives every tick, so clicking an agent reliably opens the .aw-win.

  // Header — reused in place so it never churns.
  let hdr = ap.firstElementChild;
  if(!hdr || !hdr.classList.contains('feedhdr')){
    ap.textContent = '';
    hdr = document.createElement('div');
    hdr.className = 'feedhdr';
    ap.appendChild(hdr);
  }

  if(!ags.length){
    hdr.textContent = 'agents · 0';
    while(hdr.nextSibling) hdr.nextSibling.remove();
    const ph = document.createElement('div');
    ph.className = 'row'; ph.style.opacity = '.6'; ph.textContent = 'no agents yet…';
    ap.appendChild(ph);
    return;
  }
  hdr.textContent = 'agents · ' + ags.length;

  // Stable order: alphabetical by display name, INDEPENDENT of working state,
  // so a row never jumps when its agent starts/stops holding a task.
  ags.sort((a,b)=>{ const an = displayName(a.name), bn = displayName(b.name); return an < bn ? -1 : an > bn ? 1 : 0; });

  // Index the rows already in the DOM by their agent key.
  const existing = new Map();
  ap.querySelectorAll(':scope > .arow').forEach(el => existing.set(el.dataset.agent, el));
  // Clear any non-.arow leftovers after the header (e.g. the "no agents…" row).
  let n = hdr.nextSibling;
  while(n){ const next = n.nextSibling; if(n.nodeType !== 1 || !n.classList.contains('arow')) n.remove(); n = next; }

  const wanted = new Set(ags.map(a => a.name));
  existing.forEach((el, name) => { if(!wanted.has(name)){ el.remove(); existing.delete(name); } });

  // Reconcile in order: reuse or create, patch, and place after the previous
  // node only if it isn't already there (a stable set never triggers a move).
  let prev = hdr;
  for(const a of ags){
    let el = existing.get(a.name);
    if(!el){ el = buildReplayAgentRow(a.name); existing.set(a.name, el); }
    patchReplayAgentRow(el, a);
    if(prev.nextSibling !== el) ap.insertBefore(el, prev.nextSibling);
    prev = el;
  }
}
// buildReplayAgentRow: create ONE persistent roster row for an agent. The click
// listener is bound to the stable node (not a re-parsed inline onclick), so it
// survives every repaint — replayAgentClick routes to the product's native
// floating window where it exists, and to the ported site window
// (openReplayAgentWindow) on the backend-free site.
function buildReplayAgentRow(name){
  const el = document.createElement('div');
  el.className = 'arow';
  el.dataset.agent = name;
  el.style.cursor = 'pointer';
  el.title = 'open ' + displayName(name) + ' detail';
  const dot = document.createElement('span'); dot.className = 'adot';
  const b = document.createElement('b');
  const meta = document.createElement('span'); meta.className = 'ameta';
  const doing = document.createElement('span'); doing.className = 'adoing';
  el.append(dot, b, document.createTextNode(' '), meta, doing);
  el.addEventListener('click', () => replayAgentClick(name));
  return el;
}
// patchReplayAgentRow: update ONLY the changed bits of an existing roster row,
// caching each value on the node's dataset so a steady frame writes nothing to
// the DOM at all (no churn, no reflow) — the row node stays identical.
function patchReplayAgentRow(el, a){
  const work = a.held.size > 0;
  const dotColor = work ? roleColor(a.role) : '#5b5750';
  const nameColor = roleColor(a.role);
  const dn = displayName(a.name);
  const role = a.role || '';
  const doing = work
    ? (a.lastCmd ? '❯ ' + a.lastCmd.slice(0,22) : 'on ' + (Array.from(a.held)[0] || ''))
    : 'idle';
  const cls = 'arow' + ((typeof replayWindows !== 'undefined' && replayWindows.has(a.name)) ? ' arowsel' : '');
  const d = el.dataset;
  if(d._cls !== cls){ el.className = cls; d._cls = cls; }
  if(d._dot !== dotColor){ el.querySelector('.adot').style.background = dotColor; d._dot = dotColor; }
  const b = el.querySelector('b');
  if(d._nc !== nameColor){ b.style.color = nameColor; d._nc = nameColor; }
  if(d._dn !== dn){ b.textContent = dn; d._dn = dn; }
  if(d._role !== role){ el.querySelector('.ameta').textContent = role; d._role = role; }
  if(d._doing !== doing){ el.querySelector('.adoing').textContent = doing; d._doing = doing; }
}
// renderReplayLine: one console row, action or reasoning. Actions render
// exactly as before (❯ prompt, agent, command, ✓/✗ exit badge). Thoughts
// render VISIBLY DIFFERENT — a 💭 affix, no prompt/badge chrome, italic
// muted text — so a viewer instantly tells "the herd is reasoning" from
// "the herd just ran something" at a glance, never confusing the two.
// The thought text is rendered VERBATIM from the tape (only esc()'d for
// HTML safety) — never truncated, rewritten, or summarized here; that
// invariant is the whole point of the story engine (see internal/brain/
// thought.go's header comment).
function renderReplayLine(e){
  if(e.kind === 'thought'){
    return '<div class="xblk xthought"><div class="xcmdline xthoughtline">' +
      '<span class="xthoughtico" title="reasoning, not an action">💭</span> ' +
      '<b style="color:' + roleColor(e.role) + '">' + esc(displayName(e.agent)) + '</b> ' +
      '<span class="xthoughttext">·thinking· ' + esc(e.text || '') + '</span></div></div>';
  }
  // pool: the advpool's ordered reasoning trace (subject → dev-adequacy →
  // verdict), one beat each, always readable text pre-composed by the switch
  // above and esc()'d here — never raw HTML from the tape. The verdict beat
  // carries a status modifier (xpool-ok / xpool-review) so certified vs
  // needs-review reads at a glance.
  if(e.kind === 'pool'){
    const cls = 'xpool' + (e.sub === 'verdict' ? ' xpool-' + (e.status === 'certified' ? 'ok' : 'review') : '');
    return '<div class="xblk"><div class="xcmdline xpoolline ' + cls + '"><span class="xpoolico" title="reasoning trace">⟐</span> <span class="xpooltext">' + esc(e.text || '') + '</span></div></div>';
  }
  // poolfinding: the critic's actual argument (finding_reported.detail.
  // evidence) — distinct from the ".xpool" trio above (a run can report many
  // findings), but rendered in the same chronological feed right where it
  // happened.
  if(e.kind === 'poolfinding'){
    return '<div class="xblk"><div class="xcmdline xpoolfindingline"><span class="xpoolico" title="critic evidence">⚑</span> <span class="xpoolfindingtext">' + esc(e.text || '') + '</span></div></div>';
  }
  const badge = e.ok
    ? '<span class="xbadge" style="color:var(--green)" title="exit 0">✓</span>'
    : '<span class="xbadge" style="color:var(--red)" title="exit ' + esc(String(e.exitCode)) + '">✗' + esc(String(e.exitCode)) + '</span>';
  return '<div class="xblk"><div class="xcmdline"><span class="xprompt">❯</span> <b style="color:' + roleColor(e.role) + '">' + esc(displayName(e.agent)) + '</b> <code class="xcmd">' + esc(e.command || '') + '</code> ' + badge + '</div></div>';
}
// replay filter chips: "all" + one per actor seen SO FAR in the accumulated
// console feed (execution AND thought beats alike, both tagged `agent`) —
// the tape-side twin of index.html's live chip() in renderExec(). Isolating
// one actor is how a busy multi-agent tape actually reads as a story: BOB's
// chip shows BOB's 💭 reasoning interleaved with BOB's ❯ commands, nothing
// from TESS.
function replayConsoleChip(name, role, on){
  return '<span class="xchip" onclick="setReplayExecFilter(\'' + name.replace(/'/g,"\\'") + '\')" style="cursor:pointer;padding:1px 6px;margin-left:5px;border-radius:8px;font-size:10.5px;' +
    (on ? 'background:' + (role?roleColor(role):'#6b6452') + ';color:#15110a;font-weight:700'
        : 'color:' + (role?roleColor(role):'var(--muted)') + ';border:1px solid ' + hexA(role?roleColor(role):'#6b6452', .5)) +
    '">' + esc(displayName(name)) + '</span>';
}
function renderReplayConsole(){
  const ep = document.getElementById('exec');
  if(!ep) return;
  const seen = {}; const actors = [];
  replayConsoleLines.forEach(e => { if(e.agent && !seen[e.agent]){ seen[e.agent] = e.role || ''; actors.push({name: e.agent, role: e.role || ''}); } });
  if(replayExecFilter && !seen.hasOwnProperty(replayExecFilter)) replayExecFilter = ''; // the isolated actor hasn't spoken yet at this scrub position
  const allChip = '<span class="xchip" onclick="setReplayExecFilter(\'\')" style="cursor:pointer;padding:1px 6px;margin-left:8px;border-radius:8px;font-size:10.5px;' +
    (replayExecFilter ? 'color:var(--muted);border:1px solid ' + hexA('#6b6452', .5) : 'background:#6b6452;color:#15110a;font-weight:700') + '">all</span>';
  const chips = allChip + actors.map(a => replayConsoleChip(a.name, a.role, replayExecFilter === a.name)).join('');
  const filtered = replayExecFilter ? replayConsoleLines.filter(e => e.agent === replayExecFilter) : replayConsoleLines;
  const tail = filtered.slice(-24);
  ep.innerHTML = '<div class="feedhdr">console · replaying the tape · ' + (replayExecFilter ? esc(displayName(replayExecFilter)) + ' · ' + filtered.length : replayConsoleLines.length) + ' ' + chips + '</div>' +
    (tail.length ? '' : '<div class="xempty">▌ no commands on the tape yet' + (replayExecFilter ? ' from ' + esc(displayName(replayExecFilter)) : '') + '…</div>') +
    tail.map(renderReplayLine).join('') + '<div class="xcursor">▌</div>';
  ep.scrollTop = ep.scrollHeight; // tail like the live console
}
function renderReplayTasks(){
  const tp = document.getElementById('tasks');
  if(!tp) return;
  ensureReplayTaskStyles();   // task-row hover/dim + modal chrome
  const tasks = Array.from(replayTasks.values());
  if(!tasks.length){ tp.innerHTML = ''; return; }
  const order = {claimed:0, queued:1, done:2, superseded:3, cancelled:4};
  const counts = tasks.reduce((m,t)=>{ m[t.status]=(m[t.status]||0)+1; return m; }, {});
  const hdr = ['claimed','queued','done','superseded','cancelled'].filter(s=>counts[s]).map(s=>counts[s]+' '+s).join(' · ');
  // Every task row is clickable → opens its storytelling modal. The list is
  // rebuilt via innerHTML on every playback tick / scrub, so an inline onclick
  // (re-parsed onto each fresh node) is the robust binding here — a listener
  // bound to a node would be detached out from under a mid-tick click.
  tp.innerHTML = '<div class="feedhdr">tasks · ' + tasks.length + (hdr ? ' &nbsp; ' + hdr : '') + '</div>' +
    tasks.slice().sort((a,b)=>(order[a.status]??9)-(order[b.status]??9)).slice(0,50).map(t => {
      const gone = (t.status==='cancelled' || t.status==='superseded');
      const done = (t.status==='done');
      // DIM completed AND superseded/cancelled tasks as they finish — live on
      // both play and scrub (renderReplayTasks runs on every step/seek). They
      // are NOT removed: the scrubber is the temporal source of truth, so a
      // finished task stays in the list, just dimmed.
      const dim = done || gone;
      const dot = done ? C.green : (t.status==='claimed' ? '#5b9bd5' : '#6b6452');
      const who = t.claimedBy && !gone ? ' <span style="color:' + roleColor(t.role) + '">← ' + esc(displayName(t.claimedBy)) + '</span>' : '';
      const titleStyle = gone ? 'color:var(--muted);text-decoration:line-through' : 'color:var(--fg)';
      const key = (t.key || '').replace(/'/g,"\\'");
      return '<div class="trow' + (dim ? ' tdim' : '') + '" role="button" tabindex="0" title="open this task\u2019s story" onclick="replayTaskClick(\'' + key + '\')" style="cursor:pointer"><span class="tdot" style="background:' + dot + '"></span><b style="' + titleStyle + '">' + esc(t.title || t.key) + '</b> <span style="color:var(--muted)">' + esc(t.role || '') + '</span>' + who + '</div>';
    }).join('');
}
function renderReplayFindings(){
  const fp = document.getElementById('findings');
  if(!fp) return;
  if(!replayFindings.length){ fp.innerHTML = ''; return; }
  const open = replayFindings.filter(f => !f.resolved);
  const crit = open.filter(f => f.severity==='critical' || f.severity==='high').length;
  const hdr = 'findings · ' + open.length + ' open' + (crit ? ' &nbsp; <b style="color:var(--red)">⚠ ' + crit + ' high</b>' : '');
  fp.innerHTML = '<div class="feedhdr">' + hdr + '</div>' +
    open.slice().sort((a,b)=>(SEV_RANK[b.severity]??0)-(SEV_RANK[a.severity]??0)).slice(0,30).map(f => {
      const hi = (f.severity==='critical' || f.severity==='high') ? ' hi' : '';
      const tgt = f.target ? ' <span style="color:var(--fg)">' + esc(f.target) + '</span>' : '';
      const mdl = f.model ? ' <span style="color:#5a7a8a;font-size:10px;font-family:ui-monospace,monospace">[' + esc(f.model) + ']</span>' : '';
      return '<div class="frow' + hi + '"><span class="fsev" style="color:' + sevColor(f.severity) + '">' + esc(f.severity) + '</span> <span style="color:var(--muted)">' + esc(f.type) + '</span>' + tgt + ' <span style="color:#6b6452">· ' + esc(displayName(f.reporter)) + '</span>' + mdl + '</div>';
    }).join('');
}
// ===========================================================================
// File-tree replay lens (the "files" cockpit tab). Reconstructs the directory
// tree the herd touched from the tape's claim_made/claim_released beats (folded
// into the mission stream by the v2 merge — internal/brain/replay.go), coloring
// each file by its claiming agent (the same roleColor the roster/console use).
// Scrub-driven: rebuilt from replayFiles at the CURRENT playhead on every step/
// seek (renderReplayPanels calls this), so the tree fills in and lights up as
// the tape plays, and dims a path when its claim is released. HONESTY: this is
// "who touched which path, when" — never a diff or file contents (the tape
// doesn't capture them), so the header/note say exactly that.
// ===========================================================================
// buildReplayFileTree: fold the flat path set into a nested dir tree; each leaf
// carries its {path,holder,lastActor,touches} record for the renderer.
function buildReplayFileTree(files){
  const root = {name: '', dirs: new Map(), files: []};
  for(const f of files){
    const parts = (f.path || '').split('/').filter(Boolean);
    if(!parts.length) continue;
    let node = root;
    for(let i = 0; i < parts.length - 1; i++){
      const seg = parts[i];
      if(!node.dirs.has(seg)) node.dirs.set(seg, {name: seg, dirs: new Map(), files: []});
      node = node.dirs.get(seg);
    }
    node.files.push({name: parts[parts.length - 1], rec: f});
  }
  return root;
}
// replayFileHeldCount: currently-held leaves anywhere under a node — the dir's
// "N held" summary, so a collapsed branch still shows it's active.
function replayFileHeldCount(node){
  let n = node.files.reduce((s, f) => s + (f.rec.holder ? 1 : 0), 0);
  node.dirs.forEach(d => { n += replayFileHeldCount(d); });
  return n;
}
// replayFileAgentColor: the claiming agent's color — resolved from the roster's
// reconstructed role (replayAgents), so a file matches its owner's dot/name
// everywhere else in the cockpit. Roleless actors fall back to amber, exactly
// like roleColor() does throughout.
function replayFileAgentColor(actor){
  const a = actor && replayAgents.get(actor);
  return roleColor(a && a.role);
}
function renderReplayFileNode(node, depth){
  let html = '';
  const dirs = Array.from(node.dirs.values()).sort((a, b) => a.name.localeCompare(b.name));
  for(const d of dirs){
    const held = replayFileHeldCount(d);
    const pad = 6 + depth * 14;
    html += '<div class="ft-row ft-dir" style="padding-left:' + pad + 'px">' +
      '<span class="ft-caret">▸</span><span class="ft-dname">' + esc(d.name) + '/</span>' +
      (held ? '<span class="ft-badge">' + held + ' held</span>' : '') + '</div>';
    html += renderReplayFileNode(d, depth + 1);
  }
  const files = node.files.slice().sort((a, b) => a.name.localeCompare(b.name));
  for(const f of files){
    const rec = f.rec;
    const held = !!rec.holder;
    const who = rec.holder || rec.lastActor || '';
    const col = replayFileAgentColor(who);
    const role = (who && replayAgents.get(who) && replayAgents.get(who).role) || '';
    const pad = 6 + depth * 14;
    html += '<div class="ft-row ft-file' + (held ? '' : ' ft-released') + '" style="padding-left:' + pad + 'px" ' +
      'title="' + esc(rec.path) + (held ? ' — held by ' : ' — last touched by ') + esc(displayName(who)) + '">' +
      '<span class="ft-dot" style="background:' + (held ? col : 'transparent') + ';border:1.5px solid ' + col + '"></span>' +
      '<span class="ft-fname" style="color:' + (held ? col : 'var(--stage-fg,#e6e1d8)') + '">' + esc(f.name) + '</span>' +
      (who ? '<span class="ft-who">' + esc(displayName(who)) + (role ? ' · ' + esc(role) : '') + (held ? '' : ' · released') + '</span>' : '') +
      '</div>';
  }
  return html;
}
function renderReplayFiles(){
  const el = document.getElementById('files');
  if(!el) return;
  const files = Array.from(replayFiles.values());
  const touched = files.length;
  const held = files.filter(f => f.holder).length;
  if(!touched){
    el.innerHTML = '<div class="cv-pane"><div class="feedhdr">files · 0 touched</div>' +
      '<div class="ft-empty">▌ no file claims on the tape yet — as agents claim paths, the tree fills in and lights up in each agent\u2019s color\u2026</div></div>';
    return;
  }
  const tree = buildReplayFileTree(files);
  el.innerHTML = '<div class="cv-pane">' +
    '<div class="feedhdr">files · ' + touched + ' touched' + (held ? ' · ' + held + ' held now' : '') + '</div>' +
    '<div class="ft-note">who touched which path, when — reconstructed from the herd\u2019s file claims. Paths only; the tape doesn\u2019t capture file contents.</div>' +
    '<div class="ft-tree">' + renderReplayFileNode(tree, 0) + '</div></div>';
}
// refreshReplayAgentWindows: re-paints any OPEN agent inspector windows from
// the reconstructed tape state at the current scrub position — the product-
// only floating windows (index.html's agentWindows/renderAgentWindowBody)
// live outside this shared file's DOM contract, so this is guarded to a
// no-op wherever they don't exist (the site cockpit, the canvas-only hero).
// renderAgentWindowBody itself checks `inReplay` and reads replayAgents/
// replayTasks (this file's state) rather than the live lastState snapshot —
// see index.html's renderReplayAgentWindowBody.
function refreshReplayAgentWindows(){
  if(typeof agentWindows === 'undefined' || typeof renderAgentWindowBody !== 'function') return;
  agentWindows.forEach((_, name) => renderAgentWindowBody(name));
}

// ===========================================================================
// Site replay agent-inspector WINDOW — a faithful port of the product's
// floating .aw-win (internal/ui/web/index.html). The live product opens a
// draggable inspector window when you click a roster row (index.html's
// selectAgent → openAgentWindow → renderAgentWindowBody); a static site embed
// has NONE of that machinery — agentWindows / openAgentWindow /
// renderAgentWindowBody all live in index.html and are treated as product-only
// throughout this file (see the typeof-guards above). This is the site's
// equivalent: the SAME .aw-win chrome (draggable header, role, close button,
// an ask box) reproduced verbatim in class names + layout so it looks
// identical, with its body reconstructed PURELY from the recorded tape
// (replayAgents / replayTasks) — never a /api/* call, honoring this page's
// backend-free-by-contract promise (recordings.spec.ts fails on any /api/*).
//
// Deliberately NON-colliding with the product: distinct identifiers
// (replayWindows vs agentWindows, openReplayAgentWindow vs openAgentWindow) so
// loading this shared file into index.html — which declares its own — never
// double-declares. replayAgentClick() dispatches to the product's native
// window when present and only falls back to this port on the site, so the
// product's behavior is completely untouched.
// ===========================================================================
const replayWindows = new Map();   // name -> {el, bodyEl}
let rwZ = 100;                      // z-index counter, raised on any interaction
let rwDrag = null;                  // {el, ox, oy} — set on title-bar mousedown

// Shared drag move/end (one pair of listeners for all windows). Inert in the
// product: rwDrag is only ever set by the site's openReplayAgentWindow.
addEventListener('mousemove', ev => {
  if(!rwDrag) return;
  let nx = ev.clientX - rwDrag.ox, ny = ev.clientY - rwDrag.oy;
  nx = Math.max(0, Math.min(window.innerWidth - rwDrag.el.offsetWidth, nx));
  ny = Math.max(0, Math.min(window.innerHeight - 60, ny));
  rwDrag.el.style.left = nx + 'px';
  rwDrag.el.style.top = ny + 'px';
});
addEventListener('mouseup', () => { rwDrag = null; });

// replayAgentClick: the roster-row / canvas click entry point. Product first
// (its native floating window, unchanged), site fallback (this port).
function replayAgentClick(name){
  if(!name) return;
  if(typeof openAgentWindow === 'function'){ openAgentWindow(name); return; }
  openReplayAgentWindow(name);
}

// ensureSiteReplayStyles: inject the roster affordance + .aw-win chrome CSS
// ONCE, and ONLY on a site embed. The product ships its own copies in
// index.html's <style>, so re-injecting there would duplicate/override them —
// hence the openAgentWindow guard. Styled off the theme-invariant --stage-*
// palette (dark in both site themes) with hard dark fallbacks, so the window
// reads as the same dark instrument as the product's wherever it floats.
function ensureSiteReplayStyles(){
  if(typeof openAgentWindow === 'function') return;   // product owns these already
  if(document.getElementById('replay-aw-style')) return;
  const s = document.createElement('style');
  s.id = 'replay-aw-style';
  s.textContent = `
#replay-windows { position:fixed; inset:0; z-index:1200; pointer-events:none; }
.arow.arowsel { background:var(--stage-line,#33405a); outline:1px solid var(--stage-line,#33405a); border-radius:5px; }
.arow:hover { background:var(--stage-panel,#161b22); border-radius:5px; }
.aw-win { position:absolute; width:370px; background:var(--stage-panel,#161b22); color:var(--stage-fg,#e6e1d8);
  border:1px solid var(--stage-line,#33405a); border-radius:10px; box-shadow:0 10px 34px rgba(0,0,0,.55);
  display:flex; flex-direction:column; pointer-events:auto; max-height:80vh;
  font:13px/1.5 ui-sans-serif,system-ui,sans-serif; }
.aw-title { display:flex; align-items:center; gap:7px; padding:9px 12px; background:var(--stage-bg,#0e1116);
  border-bottom:1px solid var(--stage-line,#33405a); border-radius:10px 10px 0 0; cursor:move; user-select:none;
  flex:none; min-width:0; }
.aw-title b { font-size:14px; color:var(--stage-amber,#e8a838); min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.aw-title .aw-role { color:var(--stage-muted,#8a8170); font-size:11.5px; flex:none; white-space:nowrap; }
.aw-badge { background:rgba(79,195,217,.15); border:1px solid rgba(79,195,217,.4); color:#4fc3d9;
  font-size:9.5px; padding:1px 6px; border-radius:4px; font-family:ui-monospace,monospace; letter-spacing:.3px; flex:none; white-space:nowrap; }
.aw-close { margin-left:auto; cursor:pointer; color:var(--stage-muted,#8a8170); font-size:14px; line-height:1; padding:1px 3px; border-radius:3px; flex:none; }
.aw-close:hover { color:var(--stage-fg,#e6e1d8); background:rgba(232,80,58,.18); }
.aw-body { flex:1 1 auto; overflow-y:auto; padding:10px 14px; font-size:13px; min-height:60px; }
.aw-body .isec { color:var(--stage-muted,#8a8170); font-size:11px; text-transform:uppercase; letter-spacing:.5px; margin:11px 0 4px; }
.aw-body .irow { padding:4px 0; border-bottom:1px solid var(--stage-line,#33405a); line-height:1.4; }
.aw-body .ir { color:var(--stage-muted,#8a8170); font-size:12px; }
.aw-body .itask { color:var(--stage-fg,#e6e1d8); line-height:1.45; padding:3px 0; }
.aw-body .igreen { color:var(--stage-green,#8fdcab); }
.aw-body .iconf { color:var(--stage-red,#e8503a); }
.aw-footer { flex:none; border-top:1px solid var(--stage-line,#33405a); padding:8px 10px; display:flex; flex-direction:column; gap:4px; }
.aw-footer .aw-askbox { display:flex; gap:6px; }
.aw-footer .aw-askbox input { flex:1; background:var(--stage-bg,#0e1116); border:1px solid var(--stage-line,#33405a); color:var(--stage-fg,#e6e1d8); border-radius:6px; padding:5px 8px; font-size:12px; outline:none; min-width:0; }
.aw-footer .aw-askbox input:focus { border-color:var(--stage-amber,#e8a838); }
.aw-footer .aw-askbox button { background:var(--stage-amber,#e8a838); color:var(--stage-bg,#0e1116); border:none; border-radius:6px; padding:0 11px; cursor:pointer; font-size:12px; font-weight:600; white-space:nowrap; }
.aw-footer .aw-askbox button:disabled { opacity:.5; cursor:default; }
.aw-footer .aw-ans { color:var(--stage-fg,#e6e1d8); font-size:12px; line-height:1.5; font-style:italic; border-left:2px solid var(--stage-amber,#e8a838); padding-left:8px; white-space:pre-wrap; max-height:72px; overflow-y:auto; }
.aw-footer .aw-q2 { color:var(--stage-muted,#8a8170); font-size:11px; }
/* pool reasoning-trace beats — the site-embed twin of index.html's #exec
   .xpool* rules (product ships those directly in its <style>; this JS
   injector is the site's only path to the same CSS, hence the mirror). */
#exec .xpoolline, .cockpit-exec .xpoolline { white-space:normal; overflow-wrap:anywhere; border-left:2px solid var(--stage-amber,#e8a838); padding-left:8px; }
#exec .xpoolico, .cockpit-exec .xpoolico { flex:none; opacity:.85; }
#exec .xpooltext, .cockpit-exec .xpooltext { color:var(--stage-muted,#8a8170); font-size:11.5px; line-height:1.5; }
#exec .xpool-ok, .cockpit-exec .xpool-ok { border-left-color:var(--stage-green,#8fdcab); }
#exec .xpool-ok .xpooltext, .cockpit-exec .xpool-ok .xpooltext { color:var(--stage-green,#8fdcab); font-weight:600; }
#exec .xpool-review, .cockpit-exec .xpool-review { border-left-color:var(--stage-red,#e8503a); }
#exec .xpool-review .xpooltext, .cockpit-exec .xpool-review .xpooltext { color:var(--stage-red,#e8503a); font-weight:600; }
#exec .xpoolfindingline, .cockpit-exec .xpoolfindingline { white-space:normal; overflow-wrap:anywhere; border-left:2px solid var(--stage-line,#33405a); padding-left:8px; }
#exec .xpoolfindingtext, .cockpit-exec .xpoolfindingtext { color:var(--stage-fg,#e6e1d8); font-size:11.5px; font-style:italic; line-height:1.5; }
/* the per-agent WORK timeline inside the inspector — the console beat
   vocabulary (renderReplayLine) re-scoped into .aw-body, so a drill-down reads
   with the same colored ❯ commands and 💭 thoughts as the console feed (those
   rules are otherwise scoped to #exec/.cockpit-exec and never reach here). */
.aw-body .itl { margin:2px 0 6px; }
.aw-body .itl .xblk { padding:2px 0; border:none; }
.aw-body .itl .xcmdline { display:flex; align-items:baseline; gap:6px; white-space:normal; overflow-wrap:anywhere; line-height:1.5; }
.aw-body .itl .xprompt { color:var(--stage-amber,#e8a838); flex:none; }
.aw-body .itl .xcmd { font-family:ui-monospace,monospace; color:var(--stage-fg,#e6e1d8); }
.aw-body .itl .xbadge { font-weight:700; flex:none; }
.aw-body .itl .xthoughtline { white-space:normal; overflow-wrap:anywhere; }
.aw-body .itl .xthoughtico { flex:none; opacity:.85; }
.aw-body .itl .xthoughttext { color:var(--stage-muted,#8a8170); font-style:italic; font-size:11.5px; line-height:1.5; }
.aw-body .itlprod { color:var(--stage-green,#8fdcab); cursor:pointer; }
.aw-body .itlprod:hover { color:var(--stage-amber,#e8a838); }
`;
  document.head.appendChild(s);
}

// openReplayAgentWindow: the site's openAgentWindow — same cascade offset,
// z-raise, draggable title bar, close button and ask footer as the product's,
// but its body is renderReplayWindowBody (tape state) and its ask never
// touches the network.
function openReplayAgentWindow(name){
  ensureSiteReplayStyles();
  if(replayWindows.has(name)){
    const w = replayWindows.get(name); rwZ++; w.el.style.zIndex = rwZ;
    const inp = w.el.querySelector('.aw-ask-input'); if(inp) inp.focus();
    return;
  }
  let layer = document.getElementById('replay-windows');
  if(!layer){ layer = document.createElement('div'); layer.id = 'replay-windows'; document.body.appendChild(layer); }
  const numWin = replayWindows.size;
  const offset = (numWin % 8) * 30;
  const left = Math.min(90 + offset, window.innerWidth - 390);
  const top = Math.min(90 + offset, window.innerHeight - 340);

  const el = document.createElement('div');
  el.className = 'aw-win';
  el.style.left = left + 'px'; el.style.top = top + 'px';
  rwZ++; el.style.zIndex = rwZ;

  const ra = replayAgents.get(name) || {};
  const role = ra.role || '';
  const leaf = name.split('/').pop() || name;
  el.innerHTML =
    '<div class="aw-title">' +
      '<b>' + esc(displayName(leaf)) + '</b>' +
      (role ? '<span class="aw-role">' + esc(role) + '</span>' : '') +
      '<span class="aw-close" title="close" aria-label="close">✕</span>' +
    '</div>' +
    '<div class="aw-body"><div class="ir" style="font-style:italic;font-size:12px">loading…</div></div>' +
    '<div class="aw-footer">' +
      '<div class="aw-askbox">' +
        '<input class="aw-ask-input" placeholder="what have you done? what\'s blocking you?" autocomplete="off">' +
        '<button class="aw-ask-btn">ask</button>' +
      '</div>' +
      '<div class="aw-answer-slot"></div>' +
    '</div>';
  layer.appendChild(el);

  const bodyEl = el.querySelector('.aw-body');
  const win = { el, bodyEl };
  replayWindows.set(name, win);

  el.addEventListener('mousedown', () => { rwZ++; win.el.style.zIndex = rwZ; }, true);
  el.querySelector('.aw-close').addEventListener('click', ev => {
    ev.stopPropagation();
    replayWindows.delete(name);
    el.remove();
    try{ renderReplayAgents(); }catch(_){}   // drop the .arowsel highlight
  });

  const titleBar = el.querySelector('.aw-title');
  titleBar.addEventListener('mousedown', ev => {
    if(ev.target.classList.contains('aw-close')) return;
    rwDrag = { el, ox: ev.clientX - el.offsetLeft, oy: ev.clientY - el.offsetTop };
    ev.preventDefault();
  });

  // The ask box is part of the product window's chrome, so it's here for
  // visual parity — but the herd only talks on a LIVE brain, and this page is
  // backend-free by contract (no /api/* ever). So it answers with an honest
  // static line rather than calling /api/ask.
  const askInput = el.querySelector('.aw-ask-input');
  const askBtn = el.querySelector('.aw-ask-btn');
  const doAsk = () => {
    const q = askInput ? askInput.value.trim() : '';
    if(!q) return;
    const slot = el.querySelector('.aw-answer-slot');
    if(slot) slot.innerHTML = '<div class="aw-q2">▸ ' + esc(q) + '</div><div class="aw-ans">This is a recording — ' + esc(displayName(leaf)) + ' only answers on a live brain. Run the corral to ask it yourself.</div>';
    if(askInput){ askInput.value = ''; askInput.focus(); }
  };
  askBtn.addEventListener('click', doAsk);
  askInput.addEventListener('keydown', ev => { if(ev.key === 'Enter'){ ev.preventDefault(); doAsk(); } });

  renderReplayWindowBody(name);
  try{ renderReplayAgents(); }catch(_){}   // paint the .arowsel highlight
  askInput.focus();
}

// replayWindowActivityLabel: one-line description of the agent's most recent
// beat (twin of index.html's replayActivityLabel) — verbatim from the tape.
function replayWindowActivityLabel(ra){
  const kind = ra.lastKind || '', desc = ra.lastDesc || '';
  if(kind === 'exec') return '❯ ' + desc;
  if(kind === 'thought') return '💭 ' + desc;
  if(kind === 'claimed') return 'claimed ' + desc;
  if(kind === 'task_done') return 'finished ' + desc;
  if(kind === 'task_cancelled') return 'cancelled ' + desc;
  if(kind === 'task_superseded') return 'superseded ' + desc;
  return desc;
}

// renderReplayWindowBody: the site window's body, reconstructed from the tape —
// mirrors index.html's renderReplayAgentWindowBody (holding / working-on /
// completed / last-command / last-activity), and is HONEST about what the tape
// never recorded (per-agent memory / mcp / skills — live-only /api/agent
// fields), labeling that gap instead of fabricating it.
function renderReplayWindowBody(name){
  const win = replayWindows.get(name);
  if(!win) return;
  const ra = replayAgents.get(name);
  const heldKeys = ra ? Array.from(ra.held) : [];
  const holdingTasks = heldKeys.map(k => replayTasks.get(k) || { key: k, title: k });

  let h = '<div class="isec">stats <span class="ir">· reconstructed from the tape</span></div>';
  h += '<div class="irow">holding <b>' + holdingTasks.length + '</b> · completed <b>' + ((ra && ra.completed) || 0) + '</b></div>';
  // "working on" only when the agent is ACTUALLY holding a task at this scrub
  // position — same rule the roster uses for idle-vs-working (held.size > 0).
  // Otherwise a finished agent (holding 0) kept claiming "working on <lastTask>"
  // at end-of-tape, contradicting its own idle state.
  if(ra && ra.held.size > 0 && ra.lastTaskTitle){ h += '<div class="isec">working on</div><div class="itask">' + esc(ra.lastTaskTitle) + '</div>'; }
  if(holdingTasks.length){
    h += '<div class="isec">holding</div>';
    holdingTasks.forEach(t => { h += '<div class="irow"><span class="igreen">' + esc(t.title || t.key) + '</span></div>'; });
  }
  // The per-agent WORK timeline — every thought + command this agent authored,
  // in tape order, in the SAME console vocabulary (renderReplayLine) but scoped
  // to one agent: "see exactly what this model did." The console feed holds only
  // exec + thought beats (both tagged .agent), already chronological, so filter
  // it directly and reuse the renderer (DRY with the console + the per-agent
  // filter chip). Supersedes the old one-line "last command" summary.
  const beats = replayConsoleLines.filter(e => e.agent === name);
  if(beats.length){
    h += '<div class="isec">work <span class="ir">· thoughts + commands, in order</span></div>';
    h += '<div class="itl">' + beats.map(renderReplayLine).join('') + '</div>';
  }
  // The artifacts this agent PRODUCED — each task it completed up to this scrub
  // position, clickable into the task-story modal (its mutants / authored test /
  // critique + fault-highlight). Same inline-onclick as renderReplayTasks: the
  // body is rebuilt every tick so a bound node would detach mid-click, and task
  // keys are internal ids (that function's same trust boundary).
  const produced = [], seenProduced = new Set();
  const uptoIdx = (typeof replayIdx === 'number') ? replayIdx : replayEvents.length;
  for(let i=0; i<uptoIdx && i<replayEvents.length; i++){
    const ev = replayEvents[i];
    if(ev.kind === 'task_done' && ev.actor === name && ev.subject && !seenProduced.has(ev.subject)){
      seenProduced.add(ev.subject); produced.push(ev.subject);
    }
  }
  if(produced.length){
    h += '<div class="isec">produced <span class="ir">· click to inspect the artifact</span></div>';
    produced.forEach(k => {
      const t = replayTasks.get(k) || { key: k, title: k };
      const key = String(k).replace(/'/g, "\\'");   // escape like renderReplayTasks — a quoted key must not break out of the inline handler
      h += '<div class="irow itlprod" role="button" tabindex="0" title="open this task’s story" onclick="replayTaskClick(\'' + key + '\')" style="cursor:pointer">✓ ' + esc(t.title || k) + '</div>';
    });
  }
  if(ra && ra.lastTs){ h += '<div class="isec">last activity</div><div class="irow ir">' + esc(replayWindowActivityLabel(ra)) + '</div>'; }
  // The tape doesn't capture each agent's per-agent memory / MCP / skills state
  // — but the LIVE view does. Show the real section shape (the same three the
  // running app renders), each flagged live-only, so a viewer sees exactly what
  // WOULD populate here in a live mission instead of hitting one flat dead end.
  h += '<div class="isec">memory it can recall <span class="ir">· live view only</span></div>';
  h += '<div class="irow ir">the shared pool of knowledge every agent reads — and writes back to</div>';
  h += '<div class="isec">mcp endpoints it can use <span class="ir">· live view only</span></div>';
  h += '<div class="irow ir">the tools this agent is wired into</div>';
  h += '<div class="isec">skills it has <span class="ir">· fleet skills, synced to every member — live view only</span></div>';
  h += '<div class="irow ir">the playbooks the herd has learned and shared</div>';
  win.bodyEl.innerHTML = h;
}

// refreshReplayWindows: repaint every open site window from the reconstructed
// tape state at the current scrub position (twin of refreshReplayAgentWindows
// for the product). Inert in the product — replayWindows is only ever
// populated by the site's openReplayAgentWindow.
function refreshReplayWindows(){
  replayWindows.forEach((_, name) => renderReplayWindowBody(name));
}

// ===========================================================================
// Task storytelling modal — the centerpiece of the replay screen. Every task
// (a row in the left list, a node in the swarm canvas) is clickable and opens
// a floating .aw-win (the SAME chrome as the agent inspector, so the two read
// as one visual language) that reconstructs the task's whole STORY — purely
// from the recorded tape:
//   WHAT was done   — the title/instruction + the commands the worker ran
//                     while holding it (❯ cmd ✓/✗) + findings it produced.
//   WHO did it      — the assigned worker(s), clickable to their inspector.
//   WHAT TRIGGERED  — the upstream cause: the task's depends_on parents and,
//                     when the herd re-planned, the task it superseded.
//   WHAT CAME NEXT  — the downstream tasks it unblocked (reverse depends_on),
//                     clickable to walk the causal chain.
//   TIMING/STATUS   — created / claimed / finished, relative to mission start.
// HONESTY: the tape links findings/commands to a WORKER + TIME, not directly
// to a task, so those are matched by "who held this task, when" and labeled as
// such; and a field the tape never captured (no instruction, no dependency) is
// stated plainly, never invented — this repo has a verbatim-honesty gate.
// ===========================================================================

// buildReplayTaskStories: fold the WHOLE recorded tape (replayEvents) into a
// per-task story map — independent of the scrub position, so a modal always
// tells the complete arc of a task, not a partial frame. Returns {tasks, base}
// where base is the mission's first timestamp (for relative timing).
function buildReplayTaskStories(){
  const evs = (typeof replayEvents !== 'undefined' && replayEvents) || [];
  const tasks = new Map();
  let base = 0;
  const ensureT = (key) => {
    let t = tasks.get(key);
    if(!t){ t = {key, title:'', role:'', instruction:'', deps:[], supersedes:0, status:'queued', claimedBy:'', actors:new Set(), createdTs:0, claimedTs:0, doneTs:0, doneKind:'', commands:[], findings:[], next:[], result:''}; tasks.set(key, t); }
    return t;
  };
  for(const ev of evs){
    if(ev.ts && (base === 0 || ev.ts < base)) base = ev.ts;
    const d = ev.detail || {};
    switch(ev.kind){
      case 'task_created': {
        if(!ev.subject) break;
        const t = ensureT(ev.subject);
        if(d.title) t.title = d.title;
        if(d.role) t.role = d.role;
        if(d.instruction) t.instruction = d.instruction;
        if(Array.isArray(d.depends_on)) t.deps = d.depends_on.slice();
        if(d.supersedes) t.supersedes = d.supersedes;
        if(ev.ts) t.createdTs = ev.ts;
        break;
      }
      case 'task_claimed': {
        if(!ev.subject) break;
        const t = ensureT(ev.subject);
        t.status = 'claimed';
        if(ev.actor){ t.claimedBy = ev.actor; t.actors.add(ev.actor); }
        if(d.role) t.role = d.role;
        if(d.title && !t.title) t.title = d.title;
        if(ev.ts) t.claimedTs = ev.ts;
        break;
      }
      case 'task_done': case 'task_cancelled': case 'task_superseded': {
        if(!ev.subject) break;
        const t = ensureT(ev.subject);
        t.status = ev.kind.slice(5); // done | cancelled | superseded
        t.doneKind = ev.kind;
        if(ev.actor) t.actors.add(ev.actor);
        if(ev.ts) t.doneTs = ev.ts;
        if(d.result) t.result = d.result;
        break;
      }
    }
  }
  // downstream = reverse of depends_on (who was unblocked by this task).
  tasks.forEach(t => t.deps.forEach(dep => { const p = tasks.get(dep); if(p) p.next.push(t.key); }));
  // commands + findings attach to the task their WORKER held at that instant —
  // the tape links them to actor+time, never to a task key, so this is the
  // honest reconstruction (labeled as such in the UI).
  for(const ev of evs){
    const d = ev.detail || {};
    if(ev.kind === 'execution' && ev.actor){
      const t = ownerTaskAt(tasks, ev.actor, ev.ts);
      if(t) t.commands.push({command: ev.subject || '', ok: !!d.ok, exitCode: d.exit_code == null ? '' : d.exit_code, ts: ev.ts});
    } else if(ev.kind === 'finding_reported' && ev.actor){
      const t = ownerTaskAt(tasks, ev.actor, ev.ts);
      if(t) t.findings.push({type: d.type || '', severity: d.severity || '', target: ev.subject || '', ts: ev.ts});
    }
  }
  return {tasks, base};
}
// ownerTaskAt: which task was `actor` holding at time `ts` — the claimed task
// whose [claimedTs, doneTs] window contains ts (latest claim wins on overlap).
// Returns null when the beat carries no usable ts (ts=0) or no window matches,
// so an unattributable command/finding is simply left off, never guessed onto
// the wrong task.
function ownerTaskAt(tasks, actor, ts){
  if(!ts) return null;
  let best = null;
  tasks.forEach(t => {
    if(!(t.claimedBy === actor || t.actors.has(actor))) return;
    if(!t.claimedTs || t.claimedTs > ts) return;
    if(t.doneTs && ts > t.doneTs) return;
    if(!best || t.claimedTs > best.claimedTs) best = t;
  });
  return best;
}
function fmtReplayRelTs(ts, base){
  if(!ts || !base) return '—';
  const s = Math.max(0, Math.round(ts - base));
  return s < 60 ? s + 's' : Math.floor(s/60) + 'm ' + (s%60) + 's';
}
function replayTaskStatusPill(status){
  const map = {done:['done','#1f6f2e','#d6ffdf'], claimed:['in progress','var(--stage-amber,#e8a838)','var(--stage-bg,#0e1116)'],
    queued:['queued','var(--stage-line,#33405a)','var(--stage-fg,#e6e1d8)'], superseded:['superseded','#6b4b8a','#ecdcff'], cancelled:['cancelled','#7a3a3a','#ffdcd6']};
  const [label, bg, fg] = map[status] || [status, 'var(--stage-line,#33405a)', 'var(--stage-fg,#e6e1d8)'];
  return '<span class="aw-pill" style="background:' + bg + ';color:' + fg + '">' + esc(label) + '</span>';
}

const replayTaskWindows = new Map(); // key -> {el, bodyEl}

// replayTaskClick: the task-row / task-node entry point (exported to window so
// the innerHTML-rebuilt task list's inline onclick always resolves it).
function replayTaskClick(key){ if(!key) return; openReplayTaskWindow(key); }

// ensureReplayTaskStyles: task-modal-specific CSS, injected once on BOTH the
// site and the product (unguarded — unlike ensureSiteReplayStyles, which the
// product owns). The base .aw-win chrome already exists in both (index.html's
// <style> in the product, ensureSiteReplayStyles on the site); this only adds
// the task-story extras (status pill, chain links, command/finding rows).
function ensureReplayTaskStyles(){
  if(document.getElementById('replay-task-style')) return;
  const s = document.createElement('style');
  s.id = 'replay-task-style';
  s.textContent = `
.aw-win.aw-task { width: 430px; }
.aw-win.aw-task .aw-title b { color: var(--stage-fg,#e6e1d8); }
.aw-pill { font-size:9.5px; text-transform:uppercase; letter-spacing:.4px; padding:1px 7px; border-radius:999px; flex:none; white-space:nowrap; }
.aw-body .aw-instr { max-height:132px; overflow-y:auto; white-space:pre-wrap; overflow-wrap:anywhere; color: var(--stage-fg,#e6e1d8); font-size:11.5px; line-height:1.5; background: var(--stage-bg,#0e1116); border: 1px solid var(--stage-line,#33405a); border-radius:6px; padding:8px; margin:2px 0 4px; }
.aw-body .aw-honest { color: var(--stage-muted,#8a8170); font-style:italic; font-size:11.5px; line-height:1.45; padding:2px 0; }
.aw-body .aw-cmdrow { font-family: ui-monospace,SFMono-Regular,Menlo,monospace; font-size:11.5px; line-height:1.55; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.aw-body .aw-cmdrow .aw-prompt { color: var(--stage-amber,#e8a838); }
.aw-body .aw-cmd { color: var(--stage-fg,#e6e1d8); }
.aw-body .aw-ok { color: var(--stage-green,#8fdcab); font-weight:700; }
.aw-body .aw-bad { color: var(--stage-red,#e8503a); font-weight:700; }
.aw-body .aw-frow { line-height:1.5; padding:1px 0; }
.aw-body .aw-fsev { font-weight:700; }
.aw-body .aw-chain { color: var(--stage-amber,#e8a838); cursor:pointer; text-decoration:underline dotted; }
.aw-body .aw-chain:hover { color: var(--stage-fg,#e6e1d8); }
.aw-body .aw-timing { color: var(--stage-muted,#8a8170); font-size:11.5px; line-height:1.6; }
.aw-body .aw-result { max-height:220px; overflow:auto; font-family: ui-monospace,SFMono-Regular,Menlo,monospace; font-size:11px; line-height:1.5; color: var(--stage-fg,#e6e1d8); background: var(--stage-bg,#0e1116); border: 1px solid var(--stage-line,#33405a); border-radius:6px; padding:8px; white-space:pre-wrap; word-break:break-word; margin:2px 0 4px; }
.aw-body .faultline { background: rgba(232,80,58,.32); color: var(--stage-red,#ff7a63); font-weight:700; border-radius:3px; padding:0 2px; margin:0 -2px; box-decoration-break:clone; -webkit-box-decoration-break:clone; }
`;
  document.head.appendChild(s);
}

// openReplayTaskWindow: the task-story modal — a floating .aw-win (same chrome
// as the agent inspector), body reconstructed from the tape. Draggable, z-raise
// on interaction, close button. Reuses the shared #replay-windows layer and the
// rwZ/rwDrag drag machinery (openReplayAgentWindow's twin).
function openReplayTaskWindow(key){
  ensureSiteReplayStyles();   // base .aw-win chrome (site); no-op in the product
  ensureReplayTaskStyles();
  if(replayTaskWindows.has(key)){
    const w = replayTaskWindows.get(key); rwZ++; w.el.style.zIndex = rwZ;
    renderReplayTaskWindowBody(key);
    return;
  }
  let layer = document.getElementById('replay-windows');
  if(!layer){ layer = document.createElement('div'); layer.id = 'replay-windows'; document.body.appendChild(layer); }
  const num = replayWindows.size + replayTaskWindows.size;
  const offset = (num % 8) * 30;
  const left = Math.min(120 + offset, window.innerWidth - 450);
  const top = Math.min(80 + offset, window.innerHeight - 360);

  const el = document.createElement('div');
  el.className = 'aw-win aw-task';
  el.style.left = left + 'px'; el.style.top = top + 'px';
  rwZ++; el.style.zIndex = rwZ;
  el.innerHTML =
    '<div class="aw-title">' +
      '<b class="aw-tasktitle">task</b>' +
      '<span class="aw-role aw-taskstatus"></span>' +
      '<span class="aw-close" title="close" aria-label="close">✕</span>' +
    '</div>' +
    '<div class="aw-body"><div class="aw-honest">reconstructing the story…</div></div>';
  layer.appendChild(el);

  const bodyEl = el.querySelector('.aw-body');
  replayTaskWindows.set(key, { el, bodyEl });

  el.addEventListener('mousedown', () => { rwZ++; el.style.zIndex = rwZ; }, true);
  el.querySelector('.aw-close').addEventListener('click', ev => {
    ev.stopPropagation();
    replayTaskWindows.delete(key);
    el.remove();
  });
  const titleBar = el.querySelector('.aw-title');
  titleBar.addEventListener('mousedown', ev => {
    if(ev.target.classList.contains('aw-close')) return;
    rwDrag = { el, ox: ev.clientX - el.offsetLeft, oy: ev.clientY - el.offsetTop };
    ev.preventDefault();
  });

  renderReplayTaskWindowBody(key);
}

// renderReplayTaskWindowBody: paint the task's story into an open modal from
// the full-tape reconstruction. Chain links (trigger/next) and the assigned
// worker(s) are clickable — the whole point is to WALK the causal chain.
function renderReplayTaskWindowBody(key){
  const win = replayTaskWindows.get(key);
  if(!win) return;
  const {tasks, base} = buildReplayTaskStories();
  const t = tasks.get(key);
  // title bar
  const titleEl = win.el.querySelector('.aw-tasktitle');
  const statusEl = win.el.querySelector('.aw-taskstatus');
  if(!t){
    if(titleEl) titleEl.textContent = key;
    if(statusEl) statusEl.innerHTML = '';
    win.bodyEl.innerHTML = '<div class="aw-honest">This task isn\u2019t on the recorded tape.</div>';
    return;
  }
  if(titleEl) titleEl.textContent = t.title || t.key;
  if(statusEl) statusEl.innerHTML = replayTaskStatusPill(t.status);

  const chainTask = (k) => { const o = tasks.get(k); const lbl = (o && (o.title || o.key)) || k; return '<span class="aw-chain" onclick="replayTaskClick(\'' + String(k).replace(/'/g,"\\'") + '\')">' + esc(lbl) + '</span>'; };
  const chainAgent = (name) => '<span class="aw-chain" onclick="replayAgentClick(\'' + String(name).replace(/'/g,"\\'") + '\')">' + esc(displayName(name)) + '</span>';

  let h = '';
  h += '<div class="irow ir">' + esc(t.key) + (t.role ? ' · ' + esc(t.role) : '') + '</div>';

  // WHAT was done
  h += '<div class="isec">what was done</div>';
  if(t.instruction) h += '<div class="aw-instr">' + esc(t.instruction) + '</div>';
  else h += '<div class="aw-honest">The tape recorded this task\u2019s title but not a full instruction.</div>';
  if(t.commands.length){
    h += '<div class="isec">commands <span class="ir">· run by ' + (t.claimedBy ? esc(displayName(t.claimedBy)) : 'its worker') + ' while holding this task</span></div>';
    t.commands.slice(0, 12).forEach(c => {
      const badge = c.ok ? '<span class="aw-ok" title="exit 0">✓</span>' : '<span class="aw-bad" title="exit ' + esc(String(c.exitCode)) + '">✗' + esc(String(c.exitCode)) + '</span>';
      h += '<div class="aw-cmdrow"><span class="aw-prompt">❯</span> <span class="aw-cmd">' + esc(c.command) + '</span> ' + badge + '</div>';
    });
    if(t.commands.length > 12) h += '<div class="ir">…and ' + (t.commands.length - 12) + ' more</div>';
  } else {
    h += '<div class="aw-honest">No commands on the tape are attributable to this task (the tape links commands to a worker + time, not to a task key).</div>';
  }
  if(t.findings.length){
    h += '<div class="isec">findings <span class="ir">· reported while holding this task</span></div>';
    t.findings.slice(0, 8).forEach(f => {
      h += '<div class="aw-frow"><span class="aw-fsev" style="color:' + sevColor(f.severity) + '">' + esc(f.severity || 'finding') + '</span> <span class="ir">' + esc(f.type) + '</span>' + (f.target ? ' <span style="color:var(--stage-fg,#e6e1d8)">' + esc(f.target) + '</span>' : '') + '</div>';
    });
  }

  if(t.result){
    // Fault highlight: for a mutant-generator task, when the original code
    // under review is on the tape (replayPoolSubject, captured from
    // pool_subject.detail.code), diff the surviving mutant against it and
    // highlight the planted-fault lines instead of dumping the raw result.
    // Graceful/additive: no pool_subject, no mutant blocks, or a non-mutant
    // task all fall straight through to the plain result <pre> (unchanged
    // behavior from Task 2).
    let resultHtml = '';
    if(t.role === 'mutant-generator' && replayPoolSubject && replayPoolSubject.code){
      const mutants = parseMutants(t.result);
      if(mutants.length){
        const survivorIds = (replayDevAdequacy && replayDevAdequacy.survivor_ids) || [];
        const matched = mutants.find(mu => survivorIds.includes(mu.id));
        const survivor = matched || mutants[0];
        // Only claim "the surviving fault" when the tape's survivor_ids
        // actually resolved to one of the parsed mutants — if the id didn't
        // match (unrecognized scheme, empty list, stale tape), the fallback
        // to mutants[0] is a GUESS, not a verified survivor, so the label
        // must not over-claim.
        const label = matched
          ? 'the surviving fault, highlighted against the original'
          : 'a planted fault (highlighted against the original)';
        resultHtml = '<div class="isec">result <span class="ir">· ' + esc(label) + '</span></div>'
          + renderFaultDiff(replayPoolSubject.code, survivor.code);
      }
    }
    if(!resultHtml) resultHtml = '<div class="isec">result</div><pre class="aw-result">' + esc(t.result) + '</pre>';
    h += resultHtml;
  }

  // WHO did it
  h += '<div class="isec">who did it</div>';
  const actors = Array.from(t.actors);
  if(actors.length) h += '<div class="irow">' + actors.map(chainAgent).join(', ') + '</div>';
  else h += '<div class="aw-honest">No worker ever claimed this task on the tape' + (t.status === 'queued' ? ' — it stayed queued.' : '.') + '</div>';

  // WHAT TRIGGERED it
  h += '<div class="isec">what triggered it</div>';
  const triggers = [];
  if(t.deps.length) triggers.push('depended on ' + t.deps.map(chainTask).join(', ') + ' finishing first');
  if(t.supersedes) triggers.push('re-planned — it replaced an earlier task (the tape records the superseded task\u2019s id, not its key)');
  if(triggers.length) h += '<div class="irow">' + triggers.join('<br>') + '</div>';
  else h += '<div class="aw-honest">No upstream dependency or re-plan link is recorded on the tape for this task — it was part of the seed plan.</div>';

  // WHAT CAME NEXT
  h += '<div class="isec">what came next</div>';
  if(t.next.length) h += '<div class="irow">unblocked ' + t.next.map(chainTask).join(', ') + '</div>';
  else h += '<div class="aw-honest">Nothing downstream depended on this task on the tape.</div>';

  // TIMING / STATUS
  h += '<div class="isec">timing</div><div class="aw-timing">';
  h += 'created ' + fmtReplayRelTs(t.createdTs, base);
  if(t.claimedTs) h += ' · claimed ' + fmtReplayRelTs(t.claimedTs, base);
  if(t.doneTs) h += ' · ' + esc(t.status) + ' ' + fmtReplayRelTs(t.doneTs, base);
  h += ' <span class="ir">(from mission start)</span></div>';

  win.bodyEl.innerHTML = h;
}
function refreshReplayTaskWindows(){
  replayTaskWindows.forEach((_, key) => renderReplayTaskWindowBody(key));
}

function renderReplayPanels(){
  if(!inReplay) return; // the live page's panels belong to apply()/SSE
  renderReplayConsole();
  renderReplayTasks();
  renderReplayAgents();
  renderReplayFindings();
  renderReplayFiles();   // file-tree lens — scrub-driven, fills in / lights up as the tape plays
  // The reduced full-screen lenses are playhead-driven too (cvReduce bounds to
  // replayIdx). Re-derive only the OPEN one so scrubbing moves it in lockstep
  // with the canvas and the files tree — rendering all three every tick would
  // re-fold the prefix three times per event for no visible gain.
  if(cvCurrentView === 'progress') renderReplayProgress();
  else if(cvCurrentView === 'topology') renderReplayTopology();
  else if(cvCurrentView === 'completed') renderReplayCompleted();
  refreshReplayAgentWindows();
  refreshReplayWindows();
}

function startReplay(streamOrUrl){
  const load = (typeof streamOrUrl === 'string')
    ? fetch(streamOrUrl).then(r => r.json()).then(d => d.events || [])
    : Promise.resolve((streamOrUrl && streamOrUrl.events) || streamOrUrl || []);
  return load.then(events => {
    replayEvents = events;
    replayIdx = 0;
    replayPlaying = false;
    inReplay = true;
    // The playback-speed control lives in the top HUD ("replaying [ N× ]"), shown
    // only while a tape is loaded (hidden during live coordination).
    { const hs = document.getElementById('hud-speed'); if(hs) hs.style.display = ''; }
    if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    resetReplayPanels();
    if(es && !replaySSEPaused){ es.close(); replaySSEPaused = true; } // live mode paused while replaying
    setView('replay');
    renderReplayScrub();
  });
}
// openReplay: the live-corral entry point — wraps startReplay with the
// concrete /api/replay?mission=N URL. Called from the Completed tab's
// ▶ replay button.
function openReplay(missionId){
  startReplay('/api/replay?mission=' + missionId).catch(()=>{});
}
// stopReplaySession: the idempotent replay teardown — stop the step timer,
// reset the play button, resume live SSE if this session paused it. Called
// by setView() on ANY navigation to a non-replay view, so switching tabs
// mid-replay can never orphan the session (timer running, SSE closed, no
// visible control). closeReplay() is just the exit button's path into it.
function stopReplaySession(){
  replayPlaying = false;
  inReplay = false;
  { const hs = document.getElementById('hud-speed'); if(hs) hs.style.display = 'none'; }
  if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
  const btn = document.getElementById('replay-playbtn');
  if(btn) btn.textContent = '▶ play';
  // Relinquish the panels: clear replay content so the live snapshot (pushed
  // immediately on SSE reconnect) repaints from a blank panel, never leaving
  // a flash of stale TAPE content posing as live state. inReplay is already
  // false at this point, so no replay render can race the repaint.
  resetReplayPanels();
  clearReplayPanelDOM();
  // Any agent window opened DURING replay was rendered from the tape (never
  // fetched /api/agent — see index.html's openAgentWindow) and its
  // detailData is still null. Leaving replay without this would leave that
  // window stuck showing "fetching…" forever, since nothing else ever
  // triggers the live fetch. Kick it once per open window, symmetric with
  // fetchWindowDetail's own retry loop for a transient failure.
  if(typeof agentWindows !== 'undefined' && typeof fetchWindowDetail === 'function'){
    agentWindows.forEach((win, name) => { win.detailData = null; fetchWindowDetail(name); });
  }
  if(replaySSEPaused){ es = connectSSE(); replaySSEPaused = false; } // resume live: the events handler pushes a fresh snapshot immediately on connect
}
function closeReplay(){
  setView('swarm'); // setView runs stopReplaySession() for every non-replay view
}
function toggleReplayPlay(){
  // Pressing play while the playhead is parked at the very end restarts from
  // the top (replay again) instead of sitting dead on the last frame — same
  // rebuild-from-0 the scrub uses, so canvas + panels both reset.
  if(!replayPlaying && replayIdx >= replayEvents.length){
    replayIdx = 0;
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    resetReplayPanels();
    renderReplayScrub();
  }
  replayPlaying = !replayPlaying;
  const btn = document.getElementById('replay-playbtn');
  if(btn) btn.textContent = replayPlaying ? '⏸ pause' : '▶ play';
  if(replayPlaying) replayStep();
}
function setReplaySpeed(x){
  const n = Number(x);
  replaySpeed = REPLAY_SPEEDS.includes(n) ? n : DEFAULT_REPLAY_SPEED;
  document.querySelectorAll('[data-replay-speed]').forEach(function(sel){
    sel.value = String(replaySpeed);
  });
  try { localStorage.setItem(REPLAY_SPEED_KEY, String(replaySpeed)); } catch(e) {}
  const stat = document.getElementById('stat');
  if(stat && /replaying/.test(stat.textContent)) stat.textContent = 'replaying'; // speed now shows in the #hud-speed dropdown beside it
}
function replayStep(){
  if(replayIdx >= replayEvents.length){
    replayPlaying = false;
    const btn = document.getElementById('replay-playbtn'); if(btn) btn.textContent = '▶ play';
    return;
  }
  applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
// seekReplay: deterministic scrub — rebuild all canvas state from the start
// of the stream up to the target index. No incremental/differential seek;
// re-walking the whole prefix is what keeps a seek back and a seek forward
// produce byte-identical state to just having played there.
function seekReplay(target){
  if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
  replayIdx = 0;
  nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
  resetReplayPanels();
  while(replayIdx < target && replayIdx < replayEvents.length) applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
// applyReplayEvent: the read-only translation from one ReplayEvent (brain.go's
// merged, sorted beat stream) into the live canvas's node/link/burst/buzz
// vocabulary.
function applyReplayEvent(ev){
  // ---- cockpit accumulation (panels) — before the canvas switch below ----
  const d = ev.detail || {};
  switch(ev.kind){
    case 'task_created':
      if(ev.subject) replayTasks.set(ev.subject, {key: ev.subject, title: d.title || '', role: d.role || '', status: 'queued', claimedBy: ''});
      break;
    case 'task_claimed': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject) || {key: ev.subject, title: d.title || '', role: d.role || ''};
        t.status = 'claimed'; t.claimedBy = ev.actor || ''; t.role = d.role || t.role;
        replayTasks.set(ev.subject, t);
        if(ev.actor){
          replayAgentHold(ev.actor, d.role, ev.subject, true);
          const title = d.title || ev.subject;
          const a = replayAgentEnsure(ev.actor, d.role); a.lastTaskTitle = title;
          replayAgentTouch(ev.actor, d.role, ev.ts, 'claimed', title);
        }
      }
      break;
    }
    case 'task_done': case 'task_cancelled': case 'task_superseded': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject);
        if(t) t.status = ev.kind.slice(5); // done | cancelled | superseded
        if(ev.actor){
          replayAgentHold(ev.actor, d.role, ev.subject, false);
          const title = (t && t.title) || ev.subject;
          if(ev.kind === 'task_done'){ const a = replayAgentEnsure(ev.actor, d.role); a.completed = (a.completed||0) + 1; }
          replayAgentTouch(ev.actor, d.role, ev.ts, ev.kind, title);
        }
      }
      break;
    }
    case 'execution': {
      // dedupe on (actor, command, second-rounded ts) — cheap insurance in
      // case a stream ever carries a beat from both merge sources.
      const k = 'x|' + (ev.actor||'') + '|' + (ev.subject||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayConsoleLines.push({kind: 'exec', agent: ev.actor || '', role: d.role || '', command: ev.subject || '', ok: !!d.ok, exitCode: d.exit_code == null ? '' : d.exit_code});
        if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      }
      // roster: an execution proves the actor is a real worker + records the
      // last command (its "doing now"), even if the claim beat was missed.
      if(ev.actor){
        const a = replayAgentEnsure(ev.actor, d.role); a.lastCmd = ev.subject || a.lastCmd;
        replayAgentTouch(ev.actor, d.role, ev.ts, 'exec', (ev.subject||'') + (d.ok ? ' ✓' : ' ✗'));
      }
      break;
    }
    // thought: internal/brain/replay.go merges these straight from telemetry
    // (kind="thought", actor=agent, detail={role,text}, subject unused) — a
    // single source, never doubled like findings, so no dedupe key is
    // needed. Appended to the SAME console feed as execution beats, so the
    // rebuilt-at-scrub-T order interleaves reasoning with action exactly as
    // the herd produced it.
    case 'thought': {
      replayConsoleLines.push({kind: 'thought', agent: ev.actor || '', role: d.role || '', text: d.text || ''});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      if(ev.actor) replayAgentTouch(ev.actor, d.role, ev.ts, 'thought', d.text || '');
      break;
    }
    case 'finding_reported': {
      // Findings ride the tape TWICE (queue + telemetry merge) — dedupe the
      // ~2ms double on (subject, severity, type, second-rounded ts). Each
      // DISTINCT report is appended (the reflex re-planner legitimately
      // re-raises the same subject seconds later — that reopens a finding).
      const k = 'f|' + (ev.subject||'') + '|' + (d.severity||'') + '|' + (d.type||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayFindings.push({reporter: ev.actor || '', target: ev.subject || '', type: d.type || '', severity: d.severity || '', model: ev.model || '', resolved: false});
        // Surface the critic's ACTUAL ARGUMENT (d.evidence) in the chronological
        // console feed alongside the pool_* beats below — "show the work" means
        // the reader sees not just "a finding was reported" but WHY, in the
        // critic's own words. Only pool runs carry evidence; a bare
        // finding_reported without it produces no console beat (unchanged
        // behavior for non-pool streams).
        if(d.evidence){
          // kind:'poolfinding' — deliberately NOT 'pool': the pool_subject/
          // pool_dev_adequacy/pool_verdict trio is the countable ".xpool" trace
          // (one beat each), while a finding's evidence is its own distinct
          // beat (a finding_reported can fire many times per run).
          replayConsoleLines.push({kind:'poolfinding', target: ev.subject || '', severity: d.severity || '', type: d.type || '', text: d.evidence});
          if(replayConsoleLines.length > 200) replayConsoleLines.shift();
        }
      }
      break;
    }
    // pool_subject / pool_dev_adequacy / pool_verdict: the advpool's
    // reasoning trace (internal/brain: contain·certify·query) rendered as
    // readable beats in the SAME console feed the thoughts/execs use — the
    // ordered "why" behind a certify/needs-review verdict. Synthetic beats,
    // same shape as reflex_cap_exhausted above.
    case 'pool_subject': {
      replayConsoleLines.push({kind:'pool', sub:'subject', text:'grading ' + (d.code_path||'the change') + ' against its own tests' + (d.dev_test_path ? ' (' + d.dev_test_path + ')' : '')});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      // Capture the original code under review for the fault-highlight diff
      // (renderFaultDiff, wired into the mutant-generator task story below).
      // Additive: if this event never carries code, replayPoolSubject stays
      // null and the task story falls back to the plain result <pre>.
      if(d.code) replayPoolSubject = {code: d.code, code_path: d.code_path || ''};
      break;
    }
    case 'pool_dev_adequacy': {
      const total = d.mutants_total||0, surv = d.survivors||0;
      replayConsoleLines.push({kind:'pool', sub:'adequacy', text:"the dev's tests killed " + (total-surv) + '/' + total + ' planted faults — ' + surv + ' survived (the gap)'});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      // Which mutant(s) survived — preferred over "just the first mutant" so
      // the fault-highlight diffs the ACTUAL surviving fault, not any planted
      // mutant that the dev's tests already killed.
      replayDevAdequacy = {survivors: surv, survivor_ids: Array.isArray(d.survivor_ids) ? d.survivor_ids.map(String) : []};
      break;
    }
    case 'pool_verdict': {
      const models = Object.keys(d.models_by_role||{}).sort().map(r => r + '=' + d.models_by_role[r]).join(' ');
      replayConsoleLines.push({kind:'pool', sub:'verdict', status:(d.status||''), text:(d.status||'').toUpperCase() + ': kill-rate ' + (d.dev_kill_rate) + ', ' + (d.survivors||0) + ' survivors, ' + (d.proven_missed||0) + ' proven-missed' + (models ? ' · models ' + models : '') + ' · signed record ' + (d.record_id||'?')});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      break;
    }
    case 'reflex_cap_exhausted': {
      replayConsoleLines.push({kind: 'thought', agent: ev.actor || 'reflex-replanner', role: 'coordinator', text: `⚠️ Reflex task cap of ${d.cap || 50} reached on finding #${d.finding_id} (${d.type || ''}). Pausing/parking mission to prevent infinite loop. Human intervention required.`});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      break;
    }
    // claim_made / claim_released: the file-tree lens. These are GLOBAL
    // ambience beats (mission_id=0) folded into the stream by the v2 merge
    // (internal/brain/replay.go) via time-window inclusion. The path travels
    // in BOTH detail.path and subject (see internal/brain/coordination_
    // activity.go); prefer detail.path, fall back to subject. HONESTY: this is
    // "who touched which path, when" — never a diff or file contents.
    case 'claim_made': {
      const path = (d.path || ev.subject || '').trim();
      if(path && path !== '*'){
        const f = replayFiles.get(path) || {path, holder: '', lastActor: '', touches: 0};
        f.holder = ev.actor || '';
        if(ev.actor) f.lastActor = ev.actor;
        f.touches++;
        replayFiles.set(path, f);
      }
      break;
    }
    case 'claim_released': {
      const path = (d.path || ev.subject || '').trim();
      const actor = ev.actor || '';
      if(!path || path === '*'){
        // empty/wildcard release: clear every hold this actor still has.
        replayFiles.forEach(f => { if(f.holder === actor) f.holder = ''; });
      } else {
        const f = replayFiles.get(path);
        if(f){
          // only the current holder can release it (guards against a stale
          // release from another actor clearing someone else's hold).
          if(!actor || f.holder === actor) f.holder = '';
        } else if(actor){
          // released a path whose claim_made predates the window: still record
          // that this actor touched it, so the node appears (dimmed).
          replayFiles.set(path, {path, holder: '', lastActor: actor, touches: 0});
        }
      }
      break;
    }
    case 'finding_resolved': {
      // Resolutions ALSO ride twice AND repeat across replan cycles — dedupe
      // the double on (subject, second-rounded ts), then resolve the OLDEST
      // still-open finding for that subject (FIFO create→resolve pairing).
      // This mirrors the product's live renderFindings, which shows only
      // status==='open' rows: a finding stays visible until its resolution
      // beat fires, and a re-raise brings it back.
      const rk = 'r|' + (ev.subject||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(rk)){
        replaySeenBeats.add(rk);
        for(let i = 0; i < replayFindings.length; i++){
          if(replayFindings[i].target === ev.subject && !replayFindings[i].resolved){ replayFindings[i].resolved = true; break; }
        }
      }
      break;
    }
  }
  // ---- canvas translation (unchanged below this line) ----
  const now = Date.now()/1000; // same clock draw()'s bursts/buzzes compare against
  const agentId = ev.actor ? 'a:'+ev.actor : null;
  const pathId = ev.subject ? 'p:'+ev.subject : null;
  const an = agentId ? nodes.get(agentId) : null;
  switch(ev.kind){
    case 'task_claimed': {
      if(!agentId) break;
      const a = ensure(agentId, 'agent', ev.actor);
      a.role = (ev.detail && ev.detail.role) || a.role || '';
      a.status = 'working'; a.last = now; a.working = true;
      if(pathId){
        const short = (ev.subject||'').split('/').filter(Boolean).pop() || ev.subject;
        const p = ensure(pathId, 'path', short);
        p.claimLast = now;
        links.push({a: a, b: p});
      }
      break;
    }
    case 'task_done': case 'task_cancelled': case 'task_superseded': {
      if(an){ an.status = 'idle'; an.working = false; }
      for(let i=links.length-1; i>=0; i--){ if(links[i].b && links[i].b.id === pathId) links.splice(i,1); }
      break;
    }
    case 'execution': {
      bursts.push({x: an ? an.x : CW/2, y: an ? an.y : CH/2, t0: now, ok: !!(ev.detail && ev.detail.ok)});
      break;
    }
    case 'finding_reported': {
      const sev = (ev.detail && ev.detail.severity) || '';
      const x = (an ? an.x : CW/2) + 8, y = (an ? an.y : CH/2) - 14;
      buzzes.push({x, y, t0: now, txt: (sev ? sev+': ' : '') + (ev.subject||''), life: 3.2});
      break;
    }
    // Graceful no-ops, deliberately: task_created has no visual because the
    // live view doesn't render unclaimed queue entries as nodes either;
    // finding_resolved and the telemetry-only kinds (proposal/review/
    // mission_completed/agent_activity) are ambience the canvas has no
    // vocabulary for yet. thought is likewise a no-op HERE — its payoff is
    // the console feed (renderReplayConsole), handled above; the canvas has
    // no glyph for "an agent is thinking" (yet). Unknown future kinds land
    // here too — graceful degradation, never a thrown error.
    case 'task_created':
    case 'finding_resolved':
    case 'thought':
    default:
      break;
  }
}
function renderReplayScrub(){
  renderReplayPanels();
  // apply() (the SSE path) normally owns #empty's "no agents yet" caption —
  // but apply() never runs during replay (SSE paused) or in a static embed
  // (no SSE at all), so the replay pipeline refreshes it here, at the single
  // choke point every replay state change funnels through (startReplay,
  // replayStep, seekReplay). Null-guarded: an embed may not render #empty.
  const emp = document.getElementById('empty');
  if(emp) emp.style.display = nodes.size ? 'none' : 'flex';
  const scrub = document.getElementById('replay-scrub');
  if(!scrub) return;
  scrub.max = replayEvents.length;
  scrub.value = replayIdx;
  const label = document.getElementById('replay-label');
  if(label) label.textContent = replayIdx + ' / ' + replayEvents.length;
}
// ===========================================================================
// Cockpit view tabs for the replay / demo surface (the marketing-site cockpit).
// The LIVE product renders these views from lastState (see index.html's
// render*), driven by /api/state + /api/*. The demo has no brain, so here we
// reduce the FULL recorded tape (replayEvents, set by startReplay) into the
// progress / topology / completed panels — so the site's tabs switch to REAL
// recorded data, exactly like the app. memory / skills / proposals were never
// recorded (they come from live /api/* on the product), so they render a
// clearly-labeled sample. Only the site assigns setView = cockpitView; the
// product's own setView is untouched, so these functions are inert there.
// ===========================================================================
function cvStatusColor(s){
  if(s === 'done') return '#3fb950';
  if(s === 'claimed') return 'var(--stage-amber, #d9a441)';
  return 'var(--stage-muted, #8b8378)';
}
// cvReduce: fold the whole recorded tape into the shape the panels need. Pure
// over replayEvents — independent of the scrub position, so a tab always shows
// the complete recorded mission, not a partial frame.
function cvReduce(){
  const all = (typeof replayEvents !== 'undefined' && replayEvents) || [];
  // Reduce only the tape UP TO the playhead, so the progress/topology/completed
  // lenses reconstruct the mission AS IT STOOD at the current scrub position —
  // the same playhead the swarm canvas and files tree already honor. After a
  // full play-through replayIdx === all.length, so this is the whole tape (the
  // final state); at rest before playing it is 0 (nothing has happened yet).
  const upto = (typeof replayIdx === 'number') ? Math.min(replayIdx, all.length) : all.length;
  const evs = all.slice(0, upto);
  const tasks = new Map();   // key -> {key,title,role,status,claimedBy,order}
  const agents = new Map();  // name -> {name,role,done,claims}
  const fseen = new Set();
  let findings = 0, critHigh = 0, order = 0;
  let directive = '', missionDone = false, firstTs = 0, lastTs = 0;
  const ensureAgent = (name, role) => {
    let a = agents.get(name);
    if(!a){ a = {name, role: role || '', done: 0, claims: 0}; agents.set(name, a); }
    if(role) a.role = role;
    return a;
  };
  for(const ev of evs){
    const d = ev.detail || {};
    if(ev.ts){ if(firstTs === 0 || ev.ts < firstTs) firstTs = ev.ts; if(ev.ts > lastTs) lastTs = ev.ts; }
    switch(ev.kind){
      case 'mission_created': directive = d.directive || d.title || d.text || directive; break;
      case 'mission_completed': missionDone = true; break;
      case 'task_created':
        if(ev.subject && !tasks.has(ev.subject))
          tasks.set(ev.subject, {key: ev.subject, title: d.title || '', role: d.role || '', status: 'queued', claimedBy: '', order: order++});
        break;
      case 'task_claimed':
        if(ev.subject){
          const t = tasks.get(ev.subject) || {key: ev.subject, title: d.title || '', role: d.role || '', status: 'queued', claimedBy: '', order: order++};
          t.status = 'claimed'; t.claimedBy = ev.actor || t.claimedBy; t.role = d.role || t.role;
          tasks.set(ev.subject, t);
          if(ev.actor) ensureAgent(ev.actor, d.role).claims++;
        }
        break;
      case 'task_done': case 'task_superseded': case 'task_cancelled':
        if(ev.subject){
          const t = tasks.get(ev.subject); if(t) t.status = ev.kind.slice(5);
          if(ev.kind === 'task_done' && ev.actor) ensureAgent(ev.actor, d.role).done++;
        }
        break;
      case 'finding_reported': {
        const k = (ev.subject||'') + '|' + (d.severity||'') + '|' + (d.type||'') + '|' + Math.round(ev.ts||0);
        if(!fseen.has(k)){ fseen.add(k); findings++; if(d.severity === 'critical' || d.severity === 'high') critHigh++; }
        break;
      }
    }
  }
  return { tasks, agents, findings, critHigh, directive, missionDone, durationSec: Math.max(0, lastTs - firstTs) };
}
function cvFmtDuration(sec){ sec = Math.max(0, Math.round(sec)); return Math.floor(sec/60) + 'm ' + (sec%60) + 's'; }
function cvSampleTag(){ return '<span class="cv-sample" title="representative data — this view is populated live from the brain in the product; the recording did not capture it">sample</span>'; }

function renderReplayProgress(){
  const el = document.getElementById('progress'); if(!el) return;
  const r = cvReduce();
  const tasks = Array.from(r.tasks.values());
  const done = tasks.filter(t => t.status === 'done').length;
  const sup = tasks.filter(t => t.status === 'superseded').length;
  const order = {claimed:0, queued:1, ready:1, pending:1, done:3, superseded:4, cancelled:5};
  tasks.sort((a,b) => (order[a.status]??9) - (order[b.status]??9) || a.order - b.order);
  const dir = r.directive || 'the recorded mission';
  const pill = r.missionDone ? '<span class="cv-pill done">done</span>' : '<span class="cv-pill run">running</span>';
  const summary = `${done}/${tasks.length} done` + (sup ? ` · ${sup} superseded` : '') + (r.findings ? ` · ${r.findings} finding${r.findings>1?'s':''}` : '');
  const steps = tasks.map(t => {
    const gone = (t.status === 'cancelled' || t.status === 'superseded');
    const who = (t.claimedBy && !gone) ? `<span style="color:${roleColor(t.role)}">← ${esc(displayName(t.claimedBy))}</span>` : '';
    const ttl = gone ? 'text-decoration:line-through;opacity:.55' : '';
    return `<div class="cv-step"><span class="cv-dot" style="background:${cvStatusColor(t.status)}"></span>`
      + `<span class="cv-status">${esc(t.status)}</span><b style="${ttl}">${esc(t.title || t.key)}</b> `
      + `<span class="cv-role">${esc(t.role||'')}</span> ${who}</div>`;
  }).join('');
  el.innerHTML = `<div class="cv-pane"><div class="cv-mission"><div class="cv-mhdr">${pill}<span class="cv-dir">${esc(dir)}</span></div>`
    + `<div class="cv-sum">${esc(summary)}</div><div class="cv-steps">${steps}</div></div></div>`;
}
function renderReplayTopology(){
  const el = document.getElementById('topology'); if(!el) return;
  const r = cvReduce();
  const ags = Array.from(r.agents.values()).sort((a,b) => (a.role||'').localeCompare(b.role||'') || a.name.localeCompare(b.name));
  const brainName = (typeof skin === 'function' && skin().tab) ? skin().tab : 'corral';
  let html = `<div class="cv-pane"><div class="cv-tsum">${ags.length} agent${ags.length===1?'':'s'} in the herd · one shared brain</div>`;
  html += `<div class="cv-hub"><b>👑 ${esc(brainName)} brain</b> <span class="cv-meta">deterministic coordinator — plans, gates, coordinates</span></div>`;
  html += ags.map(a =>
    `<div class="cv-agent"><span class="cv-dot" style="background:${roleColor(a.role)}"></span>`
    + `<b style="color:${roleColor(a.role)}">${esc(displayName(a.name))}</b> <span class="cv-role">${esc(a.role||'')}</span>`
    + ` <span class="cv-meta">${a.done} done · ${a.claims} claimed</span></div>`).join('');
  html += `<div class="cv-note">${cvSampleTag()} host / model / jail detail is reported live by each worker — the product's topology shows it; this recording captured the herd's roles and throughput.</div>`;
  el.innerHTML = html + '</div>';
}
function renderReplayCompleted(){
  const el = document.getElementById('completed'); if(!el) return;
  const r = cvReduce();
  const tasks = Array.from(r.tasks.values());
  const done = tasks.filter(t => t.status === 'done').length;
  const pill = r.missionDone ? '<span class="cv-pill done">done</span>' : '<span class="cv-pill run">running</span>';
  const dir = r.directive || 'the recorded mission';
  el.innerHTML = `<div class="cv-pane"><div class="cv-mission"><div class="cv-mhdr">${pill}<span class="cv-dir">${esc(dir)}</span></div>`
    + `<div class="cv-sum">${cvFmtDuration(r.durationSec)} · ${done}/${tasks.length} tasks · ${r.findings} finding${r.findings===1?'':'s'}${r.critHigh?` (${r.critHigh} high)`:''}</div>`
    + `<div class="cv-note">This is the mission that plays in the cockpit — the full recorded run, replayable end to end.</div></div></div>`;
}
function renderSampleMemory(){
  const el = document.getElementById('memory'); if(!el) return;
  const rows = [
    ['decision', 'retry backoff must be jittered', 'to avoid thundering-herd on a shared dependency'],
    ['lesson', 'context cancellation beats a fixed deadline', 'so a caller can abort a slow retry loop'],
    ['reference', 'table-driven tests for max-retries', 'cover 0, 1, N and the exhausted case'],
  ];
  el.innerHTML = `<div class="cv-pane"><div class="cv-explain">${cvSampleTag()} The shared corpus every agent reads from and writes back to — it grows as the herd learns. Populated live from the brain in the product.</div>`
    + rows.map(([k,t,w]) => `<div class="cv-memrow"><span class="cv-kind">${esc(k)}</span><b>${esc(t)}</b><div class="cv-meta">${esc(w)}</div></div>`).join('') + '</div>';
}
function renderSampleSkills(){
  const el = document.getElementById('skills'); if(!el) return;
  const skills = [
    ['go-retry-backoff', 'skills/go/retry-backoff', 'exponential backoff with jitter + context cancellation'],
    ['table-tests', 'skills/go/table-tests', 'idiomatic table-driven test scaffolding'],
    ['egress-scan', 'skills/security/egress-scan', 'vet output for secrets before it ships'],
  ];
  el.innerHTML = `<div class="cv-pane"><div class="cv-explain">${cvSampleTag()} Skills the fleet shares — synced by every member, versioned in the brain so one change propagates to all.</div>`
    + skills.map(([n,p,d]) => `<div class="cv-skrow"><b>${esc(n)}</b> <span class="cv-meta">${esc(p)}</span><div class="cv-meta">${esc(d)}</div></div>`).join('') + '</div>';
}
function renderSampleProposals(){
  const el = document.getElementById('proposals'); if(!el) return;
  el.innerHTML = `<div class="cv-pane"><div class="cv-explain">${cvSampleTag()} The learning loop's human gate — the herd clusters findings into proposals; you approve what becomes standing memory or a shared skill.</div>`
    + `<div class="cv-prop"><div class="cv-mhdr"><span class="cv-sig">retry without jitter → thundering herd</span><span class="cv-count">3×</span></div>`
    + `<div class="cv-meta">Add jitter to backoff; the pentester flagged it across two missions.</div>`
    + `<div class="cv-pactions"><button class="cv-acc" disabled>approve</button><button class="cv-rej" disabled>dismiss</button></div></div></div>`;
}
// cockpitView: the site's tab switcher. Normalizes 'replay' → swarm (the stage
// is the swarm surface here), toggles the active tab + the shown panel + the
// replay bar, and renders the selected view from the recorded tape.
function cockpitView(v){
  if(v === 'replay') v = 'swarm';
  cvCurrentView = v;
  const tabs = ['swarm','progress','topology','memory','skills','proposals','files','completed'];
  tabs.forEach(t => { const el = document.getElementById('tab-' + t); if(el) el.classList.toggle('active', t === v); });
  const panels = ['progress','topology','memory','skills','proposals','files','completed'];
  panels.forEach(p => { const el = document.getElementById(p); if(el) el.classList.toggle('show', p === v); });
  // The scrub bar is the WHOLE cockpit's transport: it shows for every lens
  // that reconstructs at the playhead — the swarm canvas, the files tree, and
  // the reduced progress/topology/completed views (all now bounded to replayIdx
  // via cvReduce). Only the sample panels (memory/skills/proposals), which show
  // a static labeled sample the tape never recorded, are position-independent
  // and hide the transport.
  const timeline = (v === 'swarm' || v === 'files' || v === 'progress' || v === 'topology' || v === 'completed');
  const bar = document.getElementById('replay'); if(bar) bar.classList.toggle('show', timeline);
  if(v === 'progress') renderReplayProgress();
  else if(v === 'topology') renderReplayTopology();
  else if(v === 'completed') renderReplayCompleted();
  else if(v === 'memory') renderSampleMemory();
  else if(v === 'skills') renderSampleSkills();
  else if(v === 'proposals') renderSampleProposals();
  else if(v === 'files') renderReplayFiles();   // reconstructed at the current playhead
}

// Export inline handlers to window so they work reliably when the player
// is loaded as a classic script by an Astro component.
window.setSkin = setSkin;
window.toggleTheme = toggleTheme;
// refreshStageColors: re-sample the --stage-* palette and repaint the canvas.
// The demo window's own light/dark toggle (site-controls.js, data-cockpit-theme)
// changes those tokens live; the HTML chrome re-themes via CSS on its own, but
// the canvas caches its colors in C, so it must be told to re-read and repaint.
window.refreshStageColors = function(){ try{ readColors(); }catch(_){} try{ renderBg(); }catch(_){} };
window.setReplayExecFilter = setReplayExecFilter;
window.toggleReplayPlay = toggleReplayPlay;
window.seekReplay = seekReplay;
window.setReplaySpeed = setReplaySpeed;
window.closeReplay = closeReplay;
window.cockpitView = cockpitView;
window.replayAgentClick = replayAgentClick;
window.openReplayAgentWindow = openReplayAgentWindow;
window.replayTaskClick = replayTaskClick;
window.openReplayTaskWindow = openReplayTaskWindow;
// The swarm-canvas node map, exposed so the site canvas hit-test (and e2e) can
// resolve a node's world position. A top-level `const` in a classic script is
// NOT a window property by default; publish the reference explicitly.
window.replayNodes = nodes;
setReplaySpeed(storedReplaySpeed());
