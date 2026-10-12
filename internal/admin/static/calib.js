/*
 * guzva — calibration preview (design-pass2 §3).
 *
 * Enhancement-only: the calibration form posts and validates exactly as it
 * did before this script existed (admin.go setCalib is the contract). With
 * JS off, nothing here runs and the form still works — §8's rule.
 *
 * What it adds: the venue's current print with the mask rectangles drawn
 * LIVE over it (grid coordinates are cell numbers; now the installer sees
 * where mask 1 lands instead of imagining it), and a tone ramp showing how
 * the t0/t1/t2 thresholds (and invert) map room light onto the four print
 * tones. No server calls — everything is read from the form inputs.
 */
(function () {
  'use strict';
  var form = document.getElementById('calib-form');
  var wrap = document.getElementById('calib-preview');
  var still = document.getElementById('calib-still');
  var ramp = document.getElementById('calib-ramp');
  if (!form || !wrap || !still) return;

  var NS = 'http://www.w3.org/2000/svg';
  var GW = 160, GH = 120, MASKS = 4;

  // The overlay must sit exactly on the picture, in grid cells. Its
  // positioning ships as explicit CSS (#calib-preview svg in web/app.css),
  // NOT as utility classes: this script runs after the Tailwind scan, so a
  // runtime class string would silently match nothing (W2/B1 shape) and the
  // SVG would render as a static block below the picture.
  var svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 ' + GW + ' ' + GH);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('aria-hidden', 'true');
  wrap.appendChild(svg);

  if (ramp) ramp.classList.remove('hidden');
  var segs = [];
  if (ramp) {
    for (var i = 0; i < 4; i++) {
      var s = document.createElement('div');
      s.style.flex = 'none';
      ramp.appendChild(s);
      segs.push(s);
    }
  }

  function field(name) {
    var el = form.elements[name];
    if (!el) return null;
    var v = parseInt(el.value, 10);
    return el.value.trim() === '' || isNaN(v) ? null : v;
  }

  function drawMasks() {
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
      r.setAttribute('stroke-width', '0.5');
      svg.appendChild(r);
    }
  }

  function drawRamp() {
    if (!segs.length) return;
    var t0 = field('t0'), t1 = field('t1'), t2 = field('t2');
    if (t0 === null) t0 = 64;
    if (t1 === null) t1 = 128;
    if (t2 === null) t2 = 192;
    // Clamp for display only; the server rejects nonsense with a sentence.
    var lo = Math.max(0, Math.min(t0, t1, t2));
    var hi = Math.min(255, Math.max(t0, t1, t2));
    var w = [t0 - lo, t1 - t0, t2 - t1, hi - t2];
    var names = ['--s0', '--s1', '--s2', '--s3'];
    var invert = form.elements['invert'] && form.elements['invert'].checked;
    var total = Math.max(1, w[0] + w[1] + w[2] + w[3]);
    for (var i = 0; i < 4; i++) {
      segs[i].style.width = (w[i] / total * 100) + '%';
      var tone = invert ? names[3 - i] : names[i];
      segs[i].style.background = 'var(' + tone + ')';
    }
  }

  function draw() { drawMasks(); drawRamp(); }

  // Cover both typed and steppers/spinners.
  form.addEventListener('input', draw);
  form.addEventListener('change', draw);
  still.addEventListener('load', function () { /* picture arrived; overlay is drawn already */ });
  draw();
})();
