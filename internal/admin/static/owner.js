/*
 * guzva — owner panel enhancements. Everything here is optional: the link
 * is a readonly field anyone can select by hand, and browsers have their
 * own print command. No server calls.
 */
(function () {
  'use strict';
  var copy = document.getElementById('share-copy');
  var url = document.getElementById('share-url');
  if (copy && url) {
    var label = copy.textContent;
    copy.addEventListener('click', function () {
      var done = function () {
        copy.textContent = copy.dataset.done || label;
        setTimeout(function () { copy.textContent = label; }, 2000);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(url.value).then(done, function () { url.select(); });
      } else {
        url.select();
        try { document.execCommand('copy'); done(); } catch (e) { /* selected; user copies */ }
      }
    });
    url.addEventListener('focus', function () { url.select(); });
  }
  var print = document.getElementById('poster-print');
  if (print) print.addEventListener('click', function () { window.print(); });
})();
