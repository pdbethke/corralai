// SPDX-License-Identifier: Elastic-2.0
// The results page's charts: a line chart (one shared y-axis, one or more
// series over an ordinal x) and a horizontal bar chart, with no dependency.
//
// They draw values they are handed and compute none: every number comes from
// Derived.summary (ledger.ts), and these functions only place and format it.
// Each chart is a <figure> with a hover/focus tooltip and a table twin behind
// a toggle, so no value is reachable only by hovering (dataviz: tooltips
// enhance, never gate). Colours are CSS custom properties (--viz-1, --viz-2)
// that the page defines per theme and that were run through the dataviz
// palette validator; text never wears a series colour.
//
// Every label is set with textContent: repo names, paths and model names come
// from a ledger anyone can publish.

const SVG = 'http://www.w3.org/2000/svg';

export type Series = {
  name: string;
  /** a CSS colour, normally var(--viz-N) */
  color: string;
  /** one per x position; null is a value that was not measured and draws a gap */
  values: (number | null)[];
};

export type LineChartSpec = {
  title: string;
  /** a sentence under the title saying what is plotted and what is left out */
  note?: string;
  /** short tick label per x position */
  xLabels: string[];
  /** the tooltip heading per x position */
  xDetail: string[];
  /** the table's first-column heading */
  xHeading: string;
  series: Series[];
  /** formats a value for ticks, tooltip and table */
  format: (v: number) => string;
  /** what a null reads as in the tooltip and table */
  nullText: string;
  /** fixed y ticks (0..1 for a share); otherwise round steps above the max */
  yTicks?: number[];
  /** the values are counts: ticks are whole numbers */
  integer?: boolean;
};

export type BarRow = { label: string; sub?: string; value: number; detail: string[] };

export type BarChartSpec = {
  title: string;
  note?: string;
  color: string;
  rows: BarRow[];
  format: (v: number) => string;
  /** the table's columns after the label: heading + one cell per row */
  table: { heading: string; cells: string[] }[];
  labelHeading: string;
};

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function svg<K extends keyof SVGElementTagNameMap>(tag: K, attrs: Record<string, string | number>): SVGElementTagNameMap[K] {
  const e = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}

/** niceStep is the 1/2/5×10^k step at or above x. */
function niceStep(x: number): number {
  const p = 10 ** Math.floor(Math.log10(x));
  for (const m of [1, 2, 5, 10]) if (m * p >= x) return m * p;
  return 10 * p;
}

/**
 * niceTicks returns round tick values from 0 to a top at or above max, about
 * four steps, never a fractional step for counts (12.5 tests is not a count).
 */
export function niceTicks(max: number, integer: boolean): number[] {
  let step = max > 0 ? niceStep(max / 4) : 1;
  if (integer) step = Math.max(1, Math.round(step));
  const top = Math.max(step, Math.ceil(max / step) * step);
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v);
  return ticks;
}

// The frame shared by both chart kinds: a title, an optional note, a toggle
// between the chart and its table, and the body the caller fills.
function frame(title: string, note: string | undefined) {
  const fig = el('figure', 'viz');
  const head = el('div', 'viz-head');
  const cap = el('figcaption');
  cap.appendChild(el('span', 'viz-title', title));
  if (note) cap.appendChild(el('span', 'viz-note', note));
  head.appendChild(cap);
  const toggle = el('button', 'viz-toggle', 'Show table');
  toggle.type = 'button';
  toggle.setAttribute('aria-pressed', 'false');
  head.appendChild(toggle);
  fig.appendChild(head);
  const chart = el('div', 'viz-chart');
  const table = el('div', 'viz-table');
  table.hidden = true;
  fig.append(chart, table);
  toggle.addEventListener('click', () => {
    const showTable = table.hidden;
    table.hidden = !showTable;
    chart.hidden = showTable;
    toggle.textContent = showTable ? 'Show chart' : 'Show table';
    toggle.setAttribute('aria-pressed', String(showTable));
  });
  return { fig, chart, table };
}

function dataTable(headings: string[], rows: string[][], numericFrom: number): HTMLElement {
  const wrap = el('div', 'viz-tablewrap');
  const t = el('table');
  const tr = el('tr');
  headings.forEach((h, i) => {
    const th = el('th', i >= numericFrom ? 'num' : undefined, h);
    th.scope = 'col';
    tr.appendChild(th);
  });
  t.appendChild(el('thead')).appendChild(tr);
  const body = el('tbody');
  for (const r of rows) {
    const row = el('tr');
    r.forEach((c, i) => row.appendChild(el('td', i >= numericFrom ? 'num' : undefined, c)));
    body.appendChild(row);
  }
  t.appendChild(body);
  wrap.appendChild(t);
  return wrap;
}

function tooltip(): HTMLDivElement {
  const tip = el('div', 'viz-tip');
  tip.setAttribute('role', 'status');
  tip.hidden = true;
  return tip;
}

function fillTip(tip: HTMLElement, heading: string, lines: { color: string; value: string; name: string }[]) {
  tip.replaceChildren(el('div', 'viz-tip-head', heading));
  for (const l of lines) {
    const row = el('div', 'viz-tip-row');
    const key = el('span', 'viz-key-line');
    key.style.background = l.color;
    row.append(key, el('strong', undefined, l.value), el('span', 'viz-tip-name', l.name));
    tip.appendChild(row);
  }
}

function placeTip(tip: HTMLElement, host: HTMLElement, x: number, y: number) {
  tip.hidden = false;
  const w = tip.offsetWidth;
  const h = tip.offsetHeight;
  const left = Math.max(4, Math.min(host.clientWidth - w - 4, x + 12));
  const top = Math.max(4, y - h - 10);
  tip.style.transform = `translate(${left}px, ${top}px)`;
}

// redrawOnResize draws now and again whenever the host's width changes, so the
// SVG is laid out in real pixels: a scaled viewBox would shrink the tick text
// to 6px on a phone.
function redrawOnResize(host: HTMLElement, draw: (width: number) => void) {
  let last = -1;
  const run = () => {
    const w = Math.floor(host.clientWidth);
    if (w > 0 && w !== last) {
      last = w;
      draw(w);
    }
  };
  if (typeof ResizeObserver !== 'undefined') new ResizeObserver(run).observe(host);
  requestAnimationFrame(run);
}

export function lineChart(spec: LineChartSpec): HTMLElement {
  const { fig, chart, table } = frame(spec.title, spec.note);
  const n = spec.xLabels.length;

  if (spec.series.length >= 2) {
    const legend = el('ul', 'viz-legend');
    for (const s of spec.series) {
      const li = el('li');
      const key = el('span', 'viz-key-line');
      key.style.background = s.color;
      li.append(key, document.createTextNode(s.name));
      legend.appendChild(li);
    }
    chart.appendChild(legend);
  }
  const host = el('div', 'viz-plot');
  chart.appendChild(host);
  const tip = tooltip();

  table.appendChild(
    dataTable(
      [spec.xHeading, ...spec.series.map((s) => s.name)],
      spec.xDetail.map((d, i) => [d, ...spec.series.map((s) => (s.values[i] === null ? spec.nullText : spec.format(s.values[i] as number)))]),
      1,
    ),
  );

  const H = 210;
  const M = { top: 12, right: 18, bottom: 30, left: 46 };
  const max = Math.max(0, ...spec.series.flatMap((s) => s.values.filter((v): v is number => v !== null)));
  const ticks = spec.yTicks ?? niceTicks(max, spec.integer === true);
  const top = ticks[ticks.length - 1];

  redrawOnResize(host, (W) => {
    const plotW = W - M.left - M.right;
    const plotH = H - M.top - M.bottom;
    const x = (i: number) => M.left + (n === 1 ? plotW / 2 : (i / (n - 1)) * plotW);
    const y = (v: number) => M.top + plotH - (v / top) * plotH;
    const root = svg('svg', { width: W, height: H, viewBox: `0 0 ${W} ${H}`, class: 'viz-svg', tabindex: 0, role: 'img' });
    root.setAttribute('aria-label', `${spec.title}. Use the arrow keys to read each run, or show the table.`);

    // y gridlines and ticks: five clean steps, hairline and recessive.
    ticks.forEach((v, k) => {
      const yy = Math.round(y(v)) + 0.5;
      root.appendChild(svg('line', { x1: M.left, x2: W - M.right, y1: yy, y2: yy, class: k === 0 ? 'viz-axis' : 'viz-grid' }));
      const t = svg('text', { x: M.left - 8, y: yy + 4, 'text-anchor': 'end', class: 'viz-tick' });
      t.textContent = spec.format(v);
      root.appendChild(t);
    });
    // x tick labels: first and last always; a middle one when there is room.
    const xticks = n <= 1 ? [0] : plotW > 420 && n > 2 ? [0, Math.floor((n - 1) / 2), n - 1] : [0, n - 1];
    for (const i of xticks) {
      const anchor = n === 1 ? 'middle' : i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle';
      const t = svg('text', { x: n === 1 ? x(i) : i === 0 ? M.left : i === n - 1 ? W - M.right : x(i), y: H - 8, 'text-anchor': anchor, class: 'viz-tick' });
      t.textContent = spec.xLabels[i];
      root.appendChild(t);
    }

    // Lines, broken at nulls, then dots with a surface ring.
    for (const s of spec.series) {
      let d = '';
      let pen = false;
      s.values.forEach((v, i) => {
        if (v === null) {
          pen = false;
          return;
        }
        d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
        pen = true;
      });
      if (d) root.appendChild(svg('path', { d, class: 'viz-line', stroke: s.color }));
      s.values.forEach((v, i) => {
        if (v === null) return;
        // A point with no neighbour draws no line, so its dot is all there is.
        const lone = (i === 0 || s.values[i - 1] === null) && (i === n - 1 || s.values[i + 1] === null);
        if (n <= 60 || lone) root.appendChild(svg('circle', { cx: x(i), cy: y(v), r: 4, class: 'viz-dot', fill: s.color }));
      });
    }

    // The crosshair snaps to the nearest run; one tooltip lists every series.
    const cross = svg('line', { x1: 0, x2: 0, y1: M.top, y2: M.top + plotH, class: 'viz-cross', visibility: 'hidden' });
    root.appendChild(cross);
    const hit = svg('rect', { x: M.left - 10, y: 0, width: plotW + 20, height: H, fill: 'transparent' });
    root.appendChild(hit);
    let at = -1;
    const show = (i: number) => {
      at = i;
      const cx = x(i);
      cross.setAttribute('x1', String(cx));
      cross.setAttribute('x2', String(cx));
      cross.setAttribute('visibility', 'visible');
      fillTip(
        tip,
        spec.xDetail[i],
        spec.series.map((s) => ({ color: s.color, value: s.values[i] === null ? spec.nullText : spec.format(s.values[i] as number), name: s.name })),
      );
      const ys = spec.series.map((s) => s.values[i]).filter((v): v is number => v !== null);
      placeTip(tip, host, cx, ys.length ? y(Math.max(...ys)) : M.top + plotH / 2);
    };
    const hide = () => {
      at = -1;
      cross.setAttribute('visibility', 'hidden');
      tip.hidden = true;
    };
    const nearest = (px: number) => (n === 1 ? 0 : Math.max(0, Math.min(n - 1, Math.round(((px - M.left) / plotW) * (n - 1)))));
    root.addEventListener('pointermove', (e) => show(nearest(e.clientX - root.getBoundingClientRect().left)));
    root.addEventListener('pointerleave', hide);
    root.addEventListener('focus', () => show(at < 0 ? n - 1 : at));
    root.addEventListener('blur', hide);
    root.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault();
        show(Math.max(0, Math.min(n - 1, (at < 0 ? n - 1 : at) + (e.key === 'ArrowLeft' ? -1 : 1))));
      } else if (e.key === 'Escape') hide();
    });

    host.replaceChildren(root, tip);
  });
  return fig;
}

export function barChart(spec: BarChartSpec): HTMLElement {
  const { fig, chart, table } = frame(spec.title, spec.note);
  const ticks = niceTicks(Math.max(0, ...spec.rows.map((r) => r.value)), true);
  const top = ticks[ticks.length - 1];
  const list = el('div', 'viz-bars');
  list.style.position = 'relative';
  const tip = tooltip();
  for (const r of spec.rows) {
    const row = el('div', 'viz-bar-row');
    row.tabIndex = 0;
    const label = el('div', 'viz-bar-label');
    label.appendChild(el('span', undefined, r.label));
    if (r.sub) label.appendChild(el('span', 'viz-bar-sub', r.sub));
    const track = el('div', 'viz-bar-track');
    const bar = el('div', 'viz-bar');
    bar.style.width = `${top > 0 ? (r.value / top) * 100 : 0}%`;
    bar.style.background = spec.color;
    track.appendChild(bar);
    row.append(label, track, el('div', 'viz-bar-value', spec.format(r.value)));
    const show = () => {
      tip.replaceChildren(el('div', 'viz-tip-head', r.label));
      for (const d of r.detail) tip.appendChild(el('div', 'viz-tip-row', d));
      const box = row.getBoundingClientRect();
      const host = list.getBoundingClientRect();
      placeTip(tip, list, box.left - host.left + track.offsetLeft + bar.offsetWidth, box.top - host.top);
    };
    row.addEventListener('pointerenter', show);
    row.addEventListener('focus', show);
    row.addEventListener('pointerleave', () => (tip.hidden = true));
    row.addEventListener('blur', () => (tip.hidden = true));
    list.appendChild(row);
  }
  list.appendChild(tip);
  chart.appendChild(list);
  table.appendChild(
    dataTable(
      [spec.labelHeading, ...spec.table.map((c) => c.heading)],
      spec.rows.map((r, i) => [r.sub ? `${r.label} (${r.sub})` : r.label, ...spec.table.map((c) => c.cells[i])]),
      1,
    ),
  );
  return fig;
}
