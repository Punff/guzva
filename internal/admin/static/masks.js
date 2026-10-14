/*
 * guzva — privacy-mask overlay. Enhancement-only: the form posts and
 * validates without this script (admin.go setMasks is the contract). It
 * draws the typed rectangles live over the venue's current print, in grid
 * cells, so the installer sees where a mask lands. No server calls.
 */
(function () {
  'use strict';
  var form = document.getElementById('mask-form');
  var wrap = document.getElementById('mask-preview');
  if (!form || !wrap) return;

  var NS = 'http://www.w3.org/2000/svg';
  var GW = 320, GH = 240, MASKS = 4;

  // Positioned by explicit CSS (#mask-preview svg in web/app.css): this
  // script runs after the Tailwind scan, so runtime classes would not exist.
  var svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 ' + GW + ' ' + GH);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('aria-hidden', 'true');
  wrap.appendChild(svg);

  function field(name) {
    var el = form.elements[name];
    if (!el || el.value.trim() === '') return null;
    var v = parseInt(el.value, 10);
    return isNaN(v) ? null : v;
  }

  function draw() {
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    for (var m = 0; m < MASKS; m++) {
      var x0 = field('m' + m + 'x0'), y0 = field('m' + m + 'y0');
      var x1 = field('m' + m + 'x1'), y1 = field('m' + m + 'y1');
      if (x0 === null || y0 === null || x1 === null || y1 === null) continue;
      if (x1 <= x0 || y1 <= y0) continue; // the server rejects it; draw nothing
      var r = document.createElementNS(NS, 'rect');
      r.setAttribute('x', x0); r.setAttribute('y', y0);
      r.setAttribute('width', x1 - x0); r.setAttribute('height', y1 - y0);
      r.setAttribute('fill', 'var(--accent)');
      r.setAttribute('fill-opacity', '0.3');
      r.setAttribute('stroke', 'var(--accent)');
      r.setAttribute('stroke-width', '1');
      svg.appendChild(r);
    }
  }

  form.addEventListener('input', draw);
  form.addEventListener('change', draw);
  draw();
})();
