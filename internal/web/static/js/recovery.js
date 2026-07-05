// Copy the freshly generated recovery codes to the clipboard.
(function () {
	"use strict";
	var btn = document.querySelector("[data-copy-codes]");
	var list = document.querySelector("[data-codes]");
	if (!btn || !list) return;

	btn.addEventListener("click", function () {
		var codes = Array.prototype.map.call(
			list.querySelectorAll("code"),
			function (c) { return c.textContent.trim(); }
		).join("\n");
		var done = function () {
			var old = btn.textContent;
			btn.textContent = "Kopiert ✓";
			setTimeout(function () { btn.textContent = old; }, 1500);
		};
		if (navigator.clipboard && navigator.clipboard.writeText) {
			navigator.clipboard.writeText(codes).then(done, done);
		} else {
			var ta = document.createElement("textarea");
			ta.value = codes;
			document.body.appendChild(ta);
			ta.select();
			try { document.execCommand("copy"); } catch (e) { /* ignore */ }
			document.body.removeChild(ta);
			done();
		}
	});
})();
