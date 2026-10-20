	// An explicit invoice selection proposes that document's open amount.
	// Subsequent edits remain possible for partial payments or account credit.
	document.querySelectorAll("[data-payment-invoice]").forEach(function (select) {
		/** Shows only the settlement breakdown of the selected invoice. */
		function showBreakdown() {
			document.querySelectorAll("[data-payment-breakdown]").forEach(function (panel) {
				panel.hidden = panel.getAttribute("data-payment-breakdown") !== select.value;
			});
		}
		select.addEventListener("change", showBreakdown);
		showBreakdown();
		select.addEventListener("change", function () {
			var field = select.form && select.form.querySelector("[data-payment-amount]");
			var option = select.options[select.selectedIndex];
			if (!field || !option) return;
			var amount = option.dataset.remaining || "";
			field.value = /^\d+\.\d{2}$/.test(amount) && Number(amount) > 0 ? amount : "";
		});
	});

	// Row filtering (Ausbaukarte 76). Text search, an "only open" checkbox and a
	// category select all hide rows of the SAME list, so they share one
	// visibility pass keyed by the target selector — otherwise the last control
	// to fire wins and the other one silently stops working.
	(function () {
		var groups = {}; // target selector -> { search, open, category, empty, count }
		function group(sel) {
			if (!groups[sel]) groups[sel] = { sel: sel };
			return groups[sel];
		}
		document.querySelectorAll("[data-search]").forEach(function (input) {
			var g = group(input.getAttribute("data-search"));
			g.search = input;
			if (input.getAttribute("data-search-empty")) g.empty = document.querySelector(input.getAttribute("data-search-empty"));
			if (input.getAttribute("data-search-count")) g.count = document.querySelector(input.getAttribute("data-search-count"));
		});
		document.querySelectorAll("[data-filter]").forEach(function (sel) {
			group(sel.getAttribute("data-filter")).category = sel;
		});
		var openCb = document.querySelector("[data-open-filter]");

		Object.keys(groups).forEach(function (sel) {
			var g = groups[sel];
			function apply() {
				var q = g.search ? g.search.value.toLowerCase() : "";
				var cat = g.category ? g.category.value : "";
				var onlyOpen = openCb && openCb.checked;
				var shown = 0, total = 0;
				document.querySelectorAll(sel).forEach(function (item) {
					total++;
					var hit = !q || item.textContent.toLowerCase().indexOf(q) >= 0;
					if (hit && cat) hit = (item.getAttribute("data-category") || "") === cat;
					if (hit && onlyOpen && !item.hasAttribute("data-open")) hit = false;
					item.style.display = hit ? "" : "none";
					if (hit) shown++;
				});
				if (g.empty) g.empty.hidden = shown !== 0 || total === 0;
				if (g.count) {
					g.count.textContent = shown === total
						? total + " Einträge"
						: shown + " von " + total + " Einträgen";
				}
			}
			if (g.search) g.search.addEventListener("input", apply);
			if (g.category) g.category.addEventListener("change", apply);
			if (openCb && g.search) openCb.addEventListener("change", apply);
			apply();
		});
	})();

	// Batch invoice totals reflect only the selected, eligible preview rows.
	// Integer cents avoid binary floating-point rounding; the server remains the
	// authority for issuing and revalidates every content hash on submission.
	document.querySelectorAll("[data-batch-invoice]").forEach(function (form) {
		var summary = form.querySelector("[data-batch-summary]");
		var fallback = form.querySelector("[data-batch-fallback]");
		var submit = form.querySelector("[data-batch-submit]");
		if (!summary || !fallback || !submit || typeof BigInt !== "function") return;
		/** Formats integer cents as a German euro amount without float rounding. */
		function money(cents) {
			var negative = cents < BigInt(0);
			var digits = (negative ? -cents : cents).toString().padStart(3, "0");
			return (negative ? "−" : "") + digits.slice(0, -2).replace(/\B(?=(\d{3})+(?!\d))/g, ".") + "," + digits.slice(-2) + " €";
		}
		/** Recomputes the selected count and gross total, gates submit, and returns whether the selection can be issued. */
		function update() {
			var count = 0, total = BigInt(0), valid = true;
			form.querySelectorAll('input[name="neighbor_id"]:checked:not(:disabled)').forEach(function (box) {
				var gross = box.getAttribute("data-invoice-gross") || "";
				if (!/^-?\d+\.\d{2}$/.test(gross)) { valid = false; return; }
				total += BigInt(gross.replace(".", ""));
				count++;
			});
			var label = count + (count === 1 ? " Rechnung" : " Rechnungen") + " ausgewählt · Summe brutto " + money(total);
			summary.textContent = valid ? label : "Auswahlbetrag nicht verfügbar. Bitte die Seite neu laden.";
			summary.hidden = false;
			fallback.hidden = true;
			if (form.dataset.submitting !== "1") submit.disabled = !valid || count === 0;
			var confirmation = label + ". Jetzt endgültig festschreiben? Festgeschriebene Rechnungen können nur per Storno korrigiert werden.";
			submit.setAttribute("data-confirm", confirmation);
			form.setAttribute("data-confirm", confirmation);
			return valid && count > 0;
		}
		form.addEventListener("change", update);
		form.addEventListener("reset", function () { setTimeout(update, 0); });
		window.addEventListener("pageshow", function () { setTimeout(update, 0); });
		// This listener precedes the generic busy/confirmation handlers below.
		form.addEventListener("submit", function (e) {
			if (!update()) { e.preventDefault(); e.stopImmediatePropagation(); }
		});
		update();
	});

	// Carry-over: toggle all neighbour checkboxes at once.
	document.querySelectorAll("[data-carry-toggle-all]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var form = btn.closest("form");
			if (!form) return;
			var boxes = form.querySelectorAll("[data-carry-check]");
			var anyChecked = Array.prototype.some.call(boxes, function (b) { return b.checked; });
			boxes.forEach(function (b) { b.checked = !anyChecked; });
		});
	});

	// Same-page shortcuts can target a collapsed form disclosure. Open it before
	// the browser follows the hash, then move focus to the first field so the
	// action has an immediate, keyboard-accessible result.
	document.querySelectorAll("[data-open-details]").forEach(function (trigger) {
		trigger.addEventListener("click", function () {
			var selector = trigger.getAttribute("data-open-details");
			if (!selector || selector.charAt(0) !== "#") return;
			var details = document.getElementById(selector.slice(1));
			if (!details || details.tagName !== "DETAILS") return;
			details.open = true;
			var field = details.querySelector("input:not([type='hidden']), select, textarea");
			if (field) requestAnimationFrame(function () { field.focus({ preventScroll: true }); });
		});
	});

	// Client-side validation: German messages, an inline error element and ARIA
	// wiring so screen readers announce the problem (not just a transient native
	// bubble that vanishes on the next click).
	document.querySelectorAll("input, select, textarea").forEach(function (el) {
		function clear() {
			el.classList.remove("is-invalid");
			el.removeAttribute("aria-invalid");
			el.setCustomValidity("");
			var host = el.closest(".field") || el.parentNode;
			var box = host && host.querySelector(".field__err");
			if (box) {
				var remaining = (el.getAttribute("aria-describedby") || "").split(/\s+/).filter(function (id) { return id && id !== box.id; });
				box.remove();
				if (remaining.length) el.setAttribute("aria-describedby", remaining.join(" "));
				else el.removeAttribute("aria-describedby");
			}
		}
		el.addEventListener("invalid", function (e) {
			// Suppress the native validation bubble; the inline .field__err below
			// (wired via aria-describedby) is the visible message. The field stays
			// invalid, so the form still won't submit.
			e.preventDefault();
			var msg = "Bitte dieses Feld ausfüllen.";
			if (!el.validity.valueMissing) {
				var validity = el.validity;
				if (validity.customError) msg = el.validationMessage;
				else if (validity.tooShort) msg = "Bitte mindestens " + el.minLength + " Zeichen eingeben.";
				else if (validity.rangeUnderflow) msg = "Bitte einen Wert ab " + el.min.replace(".", ",") + " eingeben.";
				else if (validity.rangeOverflow) msg = "Bitte höchstens " + el.max.replace(".", ",") + " eingeben.";
				else if (validity.stepMismatch) msg = "Bitte einen Wert in Schritten von " + el.step.replace(".", ",") + " eingeben.";
				else if (validity.typeMismatch && el.type === "email") msg = "Bitte eine gültige E-Mail-Adresse eingeben (z. B. name@hof.at).";
				else if (validity.badInput) msg = "Bitte eine Zahl eingeben.";
				else msg = "Bitte einen gültigen Wert eingeben.";
			}
			el.setCustomValidity(msg);
			el.classList.add("is-invalid");
			el.setAttribute("aria-invalid", "true");
			var host = el.closest(".field") || el.parentNode;
			var box = host.querySelector(".field__err");
			if (!box) {
				box = document.createElement("span");
				box.className = "field__err";
				box.setAttribute("role", "alert");
				if (!el.id) el.id = "f" + Math.random().toString(36).slice(2, 8);
				box.id = el.id + "-err";
				el.setAttribute("aria-describedby", ((el.getAttribute("aria-describedby") || "") + " " + box.id).trim());
				host.appendChild(box);
			}
			box.textContent = msg;
		});
		el.addEventListener("input", clear);
		el.addEventListener("change", clear);
	});

	// A submit button that carries a name only reaches the server while it is
	// enabled and while the submission carries a submitter. Both of those get
	// broken below — the busy state disables the button, and the confirm modal
	// re-sends with form.submit() — so the pair is parked in a hidden field
	// instead. Without this, a form with several named actions (the bulk
	// storno/delete row) arrives with no action at all.
	function carrySubmitter(form, submitter) {
		var old = form.querySelector("input[data-submitter]");
		if (old) old.remove();
		if (!submitter || !submitter.name) return;
		var h = document.createElement("input");
		h.type = "hidden";
		h.name = submitter.name;
		h.value = submitter.value;
		h.setAttribute("data-submitter", "");
		form.appendChild(h);
	}

	// Submit feedback: a POST form that passes validation shows a spinning state
	// on its primary button. Submission still proceeds; the server redirects.
	// Skipped for data-confirm forms (the modal drives those via form.submit()).
	document.querySelectorAll("form").forEach(function (form) {
		if ((form.getAttribute("method") || "").toLowerCase() !== "post") return;
		form.addEventListener("submit", function (e) {
			var asksFirst = form.hasAttribute("data-confirm") ||
				(e.submitter && e.submitter.hasAttribute && e.submitter.hasAttribute("data-confirm"));
			if (asksFirst && form.dataset.confirmed !== "1") return;
			// Block a second submission (double-click or double-Enter) while the
			// first POST is in flight — the server redirects, so this navigates away.
			if (form.dataset.submitting === "1") { e.preventDefault(); return; }
			form.dataset.submitting = "1";
			// The CLICKED submit gets the busy state (e.submitter also covers
			// formaction siblings and ghost-styled submits — selecting by the
			// btn--primary class coupled behavior to styling).
			var btn = (e.submitter && e.submitter.tagName === "BUTTON")
				? e.submitter
				: form.querySelector("button[type='submit']");
			carrySubmitter(form, e.submitter);
			if (btn) { btn.classList.add("is-submitting"); btn.setAttribute("aria-busy", "true"); btn.disabled = true; }
		});
	});

	// Confirm destructive actions with a modern modal dialog (falls back to
	// native confirm when <dialog> is unsupported).
	var modal = document.getElementById("confirmModal");
	var msgEl = modal ? modal.querySelector("[data-modal-msg]") : null;
	var inputEl = modal ? modal.querySelector("[data-modal-input]") : null;
	var okBtn = modal ? modal.querySelector("[data-modal-ok]") : null;
	// Optional checkbox row (data-confirm-check): "also apply to the linked
	// booking?" — the answer lands in the form field named by
	// data-confirm-check-name ("1" = yes, "" = no).
	var checkWrap = modal ? modal.querySelector("[data-modal-check]") : null;
	var checkInput = modal ? modal.querySelector("[data-modal-check-input]") : null;
	var checkLabel = modal ? modal.querySelector("[data-modal-check-label]") : null;
	var pendingCheckName = null;
	var pendingForm = null;

	if (modal && typeof modal.showModal === "function") {
		modal.addEventListener("close", function () {
			var form = pendingForm;
			pendingForm = null;
			if (modal.returnValue === "confirm" && form) {
				// Copy an optional reason (e.g. void reason) into the form before submit.
				if (inputEl && !inputEl.hidden) {
					var target = form.querySelector("input[name='reason']");
					if (target) target.value = inputEl.value.trim();
				}
				// Copy the checkbox answer (e.g. cascade over a linked booking).
				if (checkWrap && !checkWrap.hidden && pendingCheckName) {
					var ct = form.querySelector("input[name='" + pendingCheckName + "']");
					if (ct) ct.value = (checkInput && checkInput.checked) ? "1" : "";
				}
				form.dataset.confirmed = "1";
				// form.submit() fires no submit event, so the double-submit lock
				// below never arms for confirmed POSTs — arm it here and give the
				// triggering button the same busy feedback as plain forms, or a
				// second click would fire a SECOND, unconfirmed POST.
				form.dataset.submitting = "1";
				var sb = form.querySelector("button[type='submit'], button:not([type])");
				if (sb) { sb.classList.add("is-submitting"); sb.setAttribute("aria-busy", "true"); sb.disabled = true; }
				form.submit(); // does not re-trigger the submit listener
			}
			// Never leak a typed reason (or its label) into the next modal.
			if (inputEl) inputEl.value = "";
			pendingCheckName = null;
		});
		// Enter inside the reason field must CONFIRM: the dialog's implicit
		// submission picks its FIRST submit button, which is "Abbrechen" — the
		// user would type a reason, hit Enter, and silently cancel.
		if (inputEl && okBtn) {
			inputEl.addEventListener("keydown", function (e) {
				// Ignore the Enter that COMMITS an IME composition (isComposing /
				// legacy keyCode 229) — otherwise finishing a German word by
				// pressing Enter would confirm the storno before the reason is done.
				if (e.key === "Enter" && !e.isComposing && e.keyCode !== 229) {
					e.preventDefault();
					okBtn.click();
				}
			});
		}
	}

	// bfcache: a Back-restored page keeps dataset flags and button locks from
	// the previous visit — confirmed forms would bypass the modal and locked
	// forms would swallow every submit. Re-arm on restore.
	window.addEventListener("pageshow", function (e) {
		if (!e.persisted) return;
		document.querySelectorAll("form").forEach(function (f) {
			delete f.dataset.confirmed;
			delete f.dataset.submitting;
		});
		document.querySelectorAll("button.is-submitting").forEach(function (b) {
			b.classList.remove("is-submitting");
			b.disabled = false;
			b.removeAttribute("aria-busy");
		});
	});

	// The message and the optional reason prompt come from the SUBMITTER when it
	// carries them, otherwise from the form — so one form can ask a different
	// question per action.
	function confirmAttrs(form, submitter) {
		if (submitter && submitter.hasAttribute && submitter.hasAttribute("data-confirm")) {
			return {
				message: submitter.getAttribute("data-confirm"),
				reason: submitter.getAttribute("data-confirm-reason"),
				check: submitter.getAttribute("data-confirm-check"),
				checkName: submitter.getAttribute("data-confirm-check-name"),
			};
		}
		return {
			message: form.getAttribute("data-confirm"),
			reason: form.getAttribute("data-confirm-reason"),
			check: form.getAttribute("data-confirm-check"),
			checkName: form.getAttribute("data-confirm-check-name"),
		};
	}

	// Auto-submitting selects (the Preisvergleich basis picker). Dropped by
	// accident in the filter rewrite while the attribute stayed in the markup.
	document.querySelectorAll("select[data-autosubmit]").forEach(function (sel) {
		sel.addEventListener("change", function () {
			if (sel.form) sel.form.submit();
		});
	});

	// The question sits either on the form or on one of its buttons (one form,
	// several actions asking different things). Collected by hand rather than with
	// ":has()": an engine without it rejects the whole selector list as a
	// SyntaxError, which aborts this file and takes every confirmation with it —
	// including the irreversible Festschreibung, which would then submit silently.
	var confirmForms = [];
	document.querySelectorAll("form[data-confirm], button[data-confirm]").forEach(function (el) {
		var f = el.tagName === "FORM" ? el : el.form;
		if (f && confirmForms.indexOf(f) < 0) confirmForms.push(f);
	});
	confirmForms.forEach(function (form) {
		form.addEventListener("submit", function (e) {
			if (form.dataset.confirmed === "1") return;
			var attrs = confirmAttrs(form, e.submitter);
			if (!attrs.message) return; // this action asks nothing
			var message = attrs.message;
			var reasonLabel = attrs.reason;
			if (!modal || typeof modal.showModal !== "function") {
				if (!window.confirm(message)) { e.preventDefault(); return; }
				// Native fallback: prompt for the reason if one was requested;
				// cancelling the prompt aborts the whole action (parity with
				// Abbrechen/ESC in the modal path).
				if (reasonLabel !== null) {
					var v = window.prompt(reasonLabel);
					if (v === null) { e.preventDefault(); return; }
					var target = form.querySelector("input[name='reason']");
					if (target) target.value = v.trim();
				}
				// Checkbox question: OK = also apply to the linked booking.
				if (attrs.check) {
					var ctf = form.querySelector("input[name='" + (attrs.checkName || "cascade") + "']");
					if (ctf) ctf.value = window.confirm(attrs.check) ? "1" : "";
				}
				return;
			}
			e.preventDefault();
			carrySubmitter(form, e.submitter);
			pendingForm = form;
			if (msgEl) msgEl.textContent = message;
			if (okBtn) {
				// Destructive intent: declared per form (data-confirm-danger) or
				// derived from a danger-styled button inside it, so a forgotten
				// tag cannot silently soften a delete. The OK button's static
				// btn--primary plus this toggle yields the filled crimson (F1).
				var danger = form.hasAttribute("data-confirm-danger") ||
					!!form.querySelector(".btn--danger, .iconact--danger");
				okBtn.classList.toggle("btn--danger", danger);
			}
			if (inputEl) {
				if (reasonLabel !== null) {
					inputEl.hidden = false;
					inputEl.placeholder = reasonLabel;
					inputEl.setAttribute("aria-label", reasonLabel);
					inputEl.value = "";
				} else {
					inputEl.hidden = true;
				}
			}
			if (checkWrap) {
				if (attrs.check) {
					checkWrap.hidden = false;
					if (checkLabel) checkLabel.textContent = attrs.check;
					// Checked by default: the pair was booked as ONE Einsatz, so
					// acting on both is the expected case; unticking narrows it.
					if (checkInput) checkInput.checked = true;
					pendingCheckName = attrs.checkName || "cascade";
				} else {
					checkWrap.hidden = true;
					pendingCheckName = null;
				}
			}
			modal.returnValue = "";
			modal.showModal();
			if (inputEl && !inputEl.hidden) inputEl.focus();
		});
	});


	// Time helper on the booking form: "Von – Bis minus Pause", or a stopwatch that
	// survives reloads and app switches, fills the existing hours field. Its inputs
	// carry no name, so the submitted form, the draft store and the offline queue
	// never see them and the stacked submit handlers stay untouched.
	document.querySelectorAll("[data-time-helper]").forEach(function (box) {
		var form = box.closest("form");
		var hours = form && form.querySelector("[data-hours]");
		if (!hours) return;
		var from = box.querySelector("[data-time-from]"), to = box.querySelector("[data-time-to]"),
			pause = box.querySelector("[data-time-break]"), status = box.querySelector("[data-time-status]"),
			toggle = box.querySelector("[data-stopwatch-toggle]"), display = box.querySelector("[data-stopwatch-display]"),
			discard = box.querySelector("[data-stopwatch-reset]");
		var uid = document.querySelector('meta[name="user-id"]');
		var key = "treckrr-stopwatch-" + (uid ? uid.content : "anon");
		var memo = null, timer = null; // memo keeps the stopwatch when storage is blocked
		/** Parses "HH:MM" from a time input into minutes after midnight, or null. */
		function minutesOf(value) {
			var m = /^(\d{1,2}):(\d{2})/.exec(value || "");
			return m ? Number(m[1]) * 60 + Number(m[2]) : null;
		}
		/** Formats whole minutes as "2 h 20 min". */
		function duration(minutes) { return Math.floor(minutes / 60) + " h " + (minutes % 60) + " min"; }
		/** Writes the minutes as decimal hours (two places, at least 0.01) and lets the price preview react. */
		function apply(minutes) {
			var h = Math.max(0.01, Math.round(minutes / 60 * 100) / 100);
			hours.value = h.toFixed(2);
			hours.dispatchEvent(new Event("input", { bubbles: true }));
			hours.dispatchEvent(new Event("change", { bubbles: true }));
			status.textContent = duration(minutes) + " → " + h.toFixed(2).replace(".", ",") + " h übernommen.";
		}
		/** Applies Von – Bis minus Pause once both times are set; a Bis before Von crosses midnight. */
		function fromRange() {
			var a = minutesOf(from.value), b = minutesOf(to.value);
			if (a === null || b === null) return;
			var span = b - a;
			if (span <= 0) span += 1440;
			var rest = Math.max(0, Math.floor(Number(pause.value) || 0));
			if (rest >= span) { status.textContent = "Die Pause ist so lang wie die Arbeitszeit oder länger."; return; }
			apply(span - rest);
		}
		[from, to, pause].forEach(function (el) { el.addEventListener("input", fromRange); el.addEventListener("change", fromRange); });
		/** Returns the running stopwatch {start} from storage or memory, or null. */
		function running() {
			try {
				var saved = JSON.parse(localStorage.getItem(key) || "null");
				if (saved && typeof saved.start === "number" && saved.start <= Date.now()) return saved;
			} catch (e) { /* storage blocked: fall back to this page's memory */ }
			return memo;
		}
		/** Persists the stopwatch so another tab or a reload continues it. */
		function store(value) {
			memo = value;
			try {
				if (value) localStorage.setItem(key, JSON.stringify(value)); else localStorage.removeItem(key);
			} catch (e) { /* private mode: the stopwatch lasts this page only */ }
		}
		/** Formats elapsed milliseconds as H:MM:SS. */
		function clock(ms) {
			var s = Math.max(0, Math.floor(ms / 1000)), m = Math.floor(s % 3600 / 60), r = s % 60;
			return Math.floor(s / 3600) + ":" + (m < 10 ? "0" : "") + m + ":" + (r < 10 ? "0" : "") + r;
		}
		/** Formats a Date as HH:MM for the time inputs. */
		function hhmm(d) { return (d.getHours() < 10 ? "0" : "") + d.getHours() + ":" + (d.getMinutes() < 10 ? "0" : "") + d.getMinutes(); }
		/** Syncs button label, discard button and the ticking display with the stopwatch state. */
		function render() {
			var run = running();
			toggle.textContent = run ? "Stoppuhr anhalten und übernehmen" : "Stoppuhr starten";
			toggle.setAttribute("aria-pressed", run ? "true" : "false");
			discard.hidden = !run;
			display.textContent = clock(run ? Date.now() - run.start : 0);
			if (run && !timer) timer = setInterval(render, 1000);
			if (!run && timer) { clearInterval(timer); timer = null; }
		}
		toggle.addEventListener("click", function () {
			var run = running();
			if (!run) {
				store({ start: Date.now() });
				status.textContent = "Stoppuhr läuft seit " + hhmm(new Date()) + " Uhr, auch wenn die Seite geschlossen wird.";
			} else {
				var minutes = Math.max(1, Math.round((Date.now() - run.start) / 60000));
				from.value = hhmm(new Date(run.start));
				to.value = hhmm(new Date());
				pause.value = "";
				store(null);
				apply(minutes);
			}
			render();
		});
		discard.addEventListener("click", function () { store(null); status.textContent = "Stoppuhr verworfen."; render(); });
		window.addEventListener("storage", function (e) { if (e.key === key) render(); });
		if (running()) box.open = true;
		render();
	});

	// Share target picker: photos shared into the installed app wait in
	// IndexedDB (written by sw.js). They are moved into the form's file input
	// and uploaded through the normal booking photo route, CSRF included.
	document.querySelectorAll("[data-share-target]").forEach(function (root) {
		var form = root.querySelector("[data-share-form]");
		if (!form || !("indexedDB" in window) || typeof DataTransfer !== "function") return;
		var input = form.querySelector("[data-share-files]"), preview = root.querySelector("[data-share-preview]"),
			empty = root.querySelector("[data-share-empty]"), thumbs = root.querySelector("[data-share-thumbs]");
		/** Runs one request against the parked-share store and resolves with its result. */
		function withStore(mode, work) {
			return new Promise(function (resolve, reject) {
				var open = indexedDB.open("treckrr-share", 1);
				open.onupgradeneeded = function () { open.result.createObjectStore("pending"); };
				open.onerror = function () { reject(open.error); };
				open.onsuccess = function () {
					var db = open.result, tx = db.transaction("pending", mode), req = work(tx.objectStore("pending"));
					tx.oncomplete = function () { db.close(); resolve(req ? req.result : undefined); };
					tx.onerror = function () { db.close(); reject(tx.error); };
				};
			});
		}
		/** Removes the parked share so it can never be attached twice. */
		function forget() { return withStore("readwrite", function (store) { return store.delete("shared"); }); }
		withStore("readonly", function (store) { return store.get("shared"); }).then(function (record) {
			var files = record && Array.isArray(record.files) ? record.files : [];
			if (!files.length) return;
			var list = new DataTransfer();
			files.forEach(function (f, i) {
				var file = new File([f.blob], f.name || ("foto-" + (i + 1) + ".jpg"), { type: f.type || "image/jpeg" });
				list.items.add(file);
				var img = document.createElement("img");
				img.src = URL.createObjectURL(file);
				img.alt = file.name;
				img.width = 96;
				img.height = 96;
				thumbs.appendChild(img);
			});
			input.files = list.files;
			root.querySelector("[data-share-count]").textContent = files.length + (files.length === 1 ? " Foto" : " Fotos") + " bereit";
			preview.hidden = false;
			form.hidden = false;
			empty.hidden = true;
		}).catch(function () { /* nothing parked or storage blocked: the empty hint stays */ });
		form.addEventListener("submit", function (e) {
			e.preventDefault();
			var target = form.querySelector('input[name="target"]:checked');
			if (!target || !/^\/entries\/\d+\/photos$/.test(target.value) || !input.files.length) return;
			form.action = target.value;
			forget().then(function () { form.submit(); }, function () { form.submit(); });
		});
		root.querySelector("[data-share-discard]").addEventListener("click", function () {
			forget().then(function () { window.location.href = "/buchungen"; }, function () { window.location.href = "/buchungen"; });
		});
	});
