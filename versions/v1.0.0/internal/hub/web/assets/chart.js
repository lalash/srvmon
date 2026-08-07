const SVG_NS = 'http://www.w3.org/2000/svg';
const observed = new WeakMap();

let gradientSeq = 0;

function el(name, attrs) {
  const node = document.createElementNS(SVG_NS, name);
  for (const key in attrs) {
    if (attrs[key] !== null && attrs[key] !== undefined) node.setAttribute(key, attrs[key]);
  }
  return node;
}

function extent(series, yMin, yMax) {
  let min = yMin === null || yMin === undefined ? Infinity : yMin;
  let max = yMax === null || yMax === undefined ? -Infinity : yMax;
  if (yMin === null || yMin === undefined || yMax === null || yMax === undefined) {
    for (const item of series) {
      for (const value of item.data) {
        if (!Number.isFinite(value)) continue;
        if (yMin === null || yMin === undefined) min = Math.min(min, value);
        if (yMax === null || yMax === undefined) max = Math.max(max, value);
      }
    }
  }
  if (!Number.isFinite(min)) min = 0;
  if (!Number.isFinite(max)) max = 1;
  if (max <= min) max = min + 1;
  return [min, max];
}

// Draws every chart in the dashboard: the small in-tile sparklines and the
// large throughput/connection charts are the same renderer with different
// options, which is what keeps them visually consistent.
export function renderChart(container, options) {
  const opts = Object.assign(
    {
      series: [],
      labels: [],
      height: 62,
      yMin: 0,
      yMax: null,
      pad: 4,
      format: (v) => String(Math.round(v)),
      axisFormat: null,
      refLines: [],
      tooltip: false,
      grid: false,
      axis: false,
    },
    options,
  );

  container.classList.add('chart');
  container.__chartOptions = opts;

  if (!observed.has(container)) {
    const observer = new ResizeObserver(() => {
      if (container.__chartOptions) draw(container, container.__chartOptions);
    });
    observer.observe(container);
    observed.set(container, observer);
  }
  draw(container, opts);
}

function draw(container, opts) {
  const width = Math.max(container.clientWidth || 0, 40);
  const height = opts.height;
  const axisFormat = opts.axisFormat || opts.format;
  const padLeft = opts.axis ? 52 : opts.pad;
  const padRight = opts.pad;
  const padTop = opts.pad + 2;
  const padBottom = opts.axis ? 16 : opts.pad;

  const plotW = Math.max(width - padLeft - padRight, 1);
  const plotH = Math.max(height - padTop - padBottom, 1);

  const svg = el('svg', {
    width,
    height,
    viewBox: `0 0 ${width} ${height}`,
    role: 'img',
    'aria-label': opts.series.map((s) => s.name).filter(Boolean).join(', ') || 'chart',
  });

  const [min, max] = extent(opts.series, opts.yMin, opts.yMax);
  const count = Math.max(...opts.series.map((s) => s.data.length), 0);

  const xAt = (index) => (count <= 1 ? padLeft + plotW / 2 : padLeft + (index / (count - 1)) * plotW);
  const yAt = (value) => padTop + plotH - ((value - min) / (max - min)) * plotH;

  if (opts.grid) {
    for (let i = 0; i <= 3; i++) {
      const y = padTop + (plotH / 3) * i;
      svg.appendChild(
        el('line', {
          x1: padLeft, x2: padLeft + plotW, y1: y, y2: y,
          stroke: 'currentColor', 'stroke-opacity': 0.12, 'stroke-width': 1,
        }),
      );
      if (opts.axis) {
        const label = el('text', {
          x: padLeft - 6, y: y + 3, 'text-anchor': 'end',
          'font-size': 10, fill: 'currentColor', 'fill-opacity': 0.45,
        });
        label.textContent = axisFormat(max - ((max - min) / 3) * i);
        svg.appendChild(label);
      }
    }
  }

  for (const line of opts.refLines) {
    if (!Number.isFinite(line.y)) continue;
    const y = yAt(line.y);
    if (y < padTop - 1 || y > padTop + plotH + 1) continue;
    svg.appendChild(
      el('line', {
        x1: padLeft, x2: padLeft + plotW, y1: y, y2: y,
        stroke: line.color || 'currentColor',
        'stroke-width': 1,
        'stroke-dasharray': line.dash || '3 4',
        'stroke-opacity': 0.7,
      }),
    );
  }

  for (const item of opts.series) {
    if (!item.data.length) continue;
    const points = item.data.map((value, index) => [xAt(index), yAt(Number.isFinite(value) ? value : min)]);
    const linePath = points.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(2)} ${y.toFixed(2)}`).join(' ');

    if (item.fill !== 0) {
      const id = `grad${++gradientSeq}`;
      const defs = el('defs', {});
      const gradient = el('linearGradient', { id, x1: 0, y1: 0, x2: 0, y2: 1 });
      gradient.appendChild(el('stop', { offset: '0%', 'stop-color': item.color, 'stop-opacity': item.fill ?? 0.28 }));
      gradient.appendChild(el('stop', { offset: '100%', 'stop-color': item.color, 'stop-opacity': 0 }));
      defs.appendChild(gradient);
      svg.appendChild(defs);

      const base = padTop + plotH;
      const areaPath = `${linePath} L${points[points.length - 1][0].toFixed(2)} ${base} L${points[0][0].toFixed(2)} ${base} Z`;
      svg.appendChild(el('path', { d: areaPath, fill: `url(#${id})`, stroke: 'none' }));
    }

    svg.appendChild(
      el('path', {
        d: linePath,
        fill: 'none',
        stroke: item.color,
        'stroke-width': item.width || 1.6,
        'stroke-linejoin': 'round',
        'stroke-linecap': 'round',
      }),
    );
  }

  container.replaceChildren(svg);

  if (opts.tooltip && count > 0) {
    attachTooltip(container, svg, opts, { xAt, yAt, padLeft, padTop, plotW, plotH, count });
  }
}

function attachTooltip(container, svg, opts, geo) {
  const tip = document.createElement('div');
  tip.className = 'chart-tip';
  container.appendChild(tip);

  const crosshair = el('line', {
    y1: geo.padTop, y2: geo.padTop + geo.plotH,
    stroke: 'currentColor', 'stroke-opacity': 0.3, 'stroke-width': 1, visibility: 'hidden',
  });
  svg.appendChild(crosshair);

  const markers = opts.series.map((item) => {
    const dot = el('circle', { r: 3, fill: item.color, stroke: 'var(--card)', 'stroke-width': 1.5, visibility: 'hidden' });
    svg.appendChild(dot);
    return dot;
  });

  const hide = () => {
    tip.style.opacity = '0';
    crosshair.setAttribute('visibility', 'hidden');
    markers.forEach((dot) => dot.setAttribute('visibility', 'hidden'));
  };

  svg.addEventListener('mouseleave', hide);
  svg.addEventListener('mousemove', (event) => {
    const box = svg.getBoundingClientRect();
    const relative = event.clientX - box.left;
    const ratio = geo.plotW <= 0 ? 0 : (relative - geo.padLeft) / geo.plotW;
    const index = Math.min(geo.count - 1, Math.max(0, Math.round(ratio * (geo.count - 1))));
    const x = geo.xAt(index);

    crosshair.setAttribute('x1', x);
    crosshair.setAttribute('x2', x);
    crosshair.setAttribute('visibility', 'visible');

    const rows = [];
    opts.series.forEach((item, i) => {
      const value = item.data[index];
      if (!Number.isFinite(value)) {
        markers[i].setAttribute('visibility', 'hidden');
        return;
      }
      markers[i].setAttribute('cx', x);
      markers[i].setAttribute('cy', geo.yAt(value));
      markers[i].setAttribute('visibility', 'visible');
      rows.push(`<span style="color:${item.color}">■</span> ${item.name || ''} <b>${opts.format(value)}</b>`);
    });

    const label = opts.labels[index] ? `<div style="opacity:.6">${opts.labels[index]}</div>` : '';
    tip.innerHTML = label + rows.join('<br>');
    tip.style.left = `${Math.min(Math.max(x, 60), container.clientWidth - 60)}px`;
    tip.style.top = `${geo.padTop + 8}px`;
    tip.style.opacity = '1';
  });
}
