// Passkey (WebAuthn) registration and login. Talks to the /account/passkeys and
// /login/passkey endpoints, converting between base64url JSON and the
// ArrayBuffers the browser credential API needs. No external dependencies.
(function () {
	"use strict";

	function b64urlToBuf(s) {
		s = s.replace(/-/g, "+").replace(/_/g, "/");
		var pad = s.length % 4;
		if (pad) s += "=".repeat(4 - pad);
		var bin = atob(s);
		var buf = new Uint8Array(bin.length);
		for (var i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
		return buf.buffer;
	}

	function bufToB64url(buf) {
		var bytes = new Uint8Array(buf);
		var bin = "";
		for (var i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
		return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
	}

	function csrf() {
		var m = document.querySelector('meta[name="csrf-token"]');
		return m ? m.getAttribute("content") : "";
	}

	function postJSON(url, body, withCsrf) {
		var headers = { "Content-Type": "application/json" };
		if (withCsrf) headers["X-CSRF-Token"] = csrf();
		return fetch(url, {
			method: "POST",
			credentials: "same-origin",
			headers: headers,
			body: body ? JSON.stringify(body) : "{}"
		});
	}

	function supported() {
		return window.PublicKeyCredential && navigator.credentials;
	}

	function prepCreate(opts) {
		var p = opts.publicKey;
		p.challenge = b64urlToBuf(p.challenge);
		p.user.id = b64urlToBuf(p.user.id);
		(p.excludeCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
		return p;
	}

	function prepGet(opts) {
		var p = opts.publicKey;
		p.challenge = b64urlToBuf(p.challenge);
		(p.allowCredentials || []).forEach(function (c) { c.id = b64urlToBuf(c.id); });
		return p;
	}

	function encodeAttestation(cred) {
		return {
			id: cred.id,
			rawId: bufToB64url(cred.rawId),
			type: cred.type,
			response: {
				attestationObject: bufToB64url(cred.response.attestationObject),
				clientDataJSON: bufToB64url(cred.response.clientDataJSON)
			},
			clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {}
		};
	}

	function encodeAssertion(cred) {
		return {
			id: cred.id,
			rawId: bufToB64url(cred.rawId),
			type: cred.type,
			response: {
				authenticatorData: bufToB64url(cred.response.authenticatorData),
				clientDataJSON: bufToB64url(cred.response.clientDataJSON),
				signature: bufToB64url(cred.response.signature),
				userHandle: cred.response.userHandle ? bufToB64url(cred.response.userHandle) : null
			},
			clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {}
		};
	}

	async function register(btn) {
		if (!supported()) { alert("Dieser Browser unterstützt keine Passkeys."); return; }
		btn.disabled = true;
		try {
			var begin = await postJSON("/account/passkeys/register/begin", {}, true);
			if (!begin.ok) throw new Error("begin");
			var opts = await begin.json();
			var cred = await navigator.credentials.create({ publicKey: prepCreate(opts) });
			var finish = await postJSON("/account/passkeys/register/finish", encodeAttestation(cred), true);
			if (!finish.ok) throw new Error("finish");
			location.reload();
		} catch (e) {
			if (e && e.name !== "NotAllowedError") alert("Passkey-Registrierung fehlgeschlagen.");
		} finally {
			btn.disabled = false;
		}
	}

	async function login(btn) {
		if (!supported()) { alert("Dieser Browser unterstützt keine Passkeys."); return; }
		btn.disabled = true;
		try {
			var begin = await postJSON("/login/passkey/begin", {}, false);
			if (!begin.ok) throw new Error("begin");
			var opts = await begin.json();
			var cred = await navigator.credentials.get({ publicKey: prepGet(opts) });
			var finish = await postJSON("/login/passkey/finish", encodeAssertion(cred), false);
			if (!finish.ok) throw new Error("finish");
			var j = await finish.json();
			location.href = j.redirect || "/";
		} catch (e) {
			if (e && e.name !== "NotAllowedError") alert("Anmeldung mit Passkey fehlgeschlagen.");
			btn.disabled = false;
		}
	}

	document.addEventListener("DOMContentLoaded", function () {
		var rb = document.getElementById("passkey-register");
		if (rb) {
			if (!supported()) { rb.style.display = "none"; }
			rb.addEventListener("click", function (e) { e.preventDefault(); register(rb); });
		}
		var lb = document.getElementById("passkey-login");
		if (lb) {
			if (!supported()) { lb.style.display = "none"; }
			lb.addEventListener("click", function (e) { e.preventDefault(); login(lb); });
		}
	});
})();
