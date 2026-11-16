// clunck app kit. Every app that runs inside the clunck phone (apps/clunck) includes this file, and so
// does the phone itself (for keyFromEvent). apps/publish.sh copies it into each app when publishing:
// edit it here in apps/shared/, not in a copy.
//
// An app gets keys from the phone's keypad (by postMessage) or from a real keyboard, as one name:
//   "0"-"9" "*" "#"   "up" "down" "left" "right" "ok"   "soft-left" "soft-right"   "back" "clear"
//   and "char": a character typed on a real keyboard, passed as the second argument.
// "end" (the red key) never reaches an app: the phone closes the app.
(() => {
	const BASE = location.host.split(".").slice(1).join("."); // housepage.dev, keeping a port if there is one
	const PHONE = `${location.protocol}//clunck.${BASE}`;
	const inPhone = window.parent !== window;
	const post = (msg) => inPhone && window.parent.postMessage(msg, PHONE);

	const KEYS = {
		ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right", Enter: "ok",
		Escape: "back", Backspace: "clear", "[": "soft-left", "]": "soft-right", End: "end", "*": "*", "#": "#",
	};
	// returns [key] or ["char", c] for a keydown event, or null when it is not a phone key
	function keyFromEvent(e) {
		if (e.metaKey || e.ctrlKey || e.altKey) return null;
		if (KEYS[e.key]) return [KEYS[e.key]];
		if (/^[0-9]$/.test(e.key)) return [e.key];
		if (e.key.length === 1) return ["char", e.key];
		return null;
	}

	let handler = null;
	function onKey(fn) {
		if (!handler) {
			addEventListener("message", (e) => {
				if (e.origin === PHONE && e.data?.type === "key") handler(e.data.key, e.data.char);
			});
			addEventListener("keydown", (e) => {
				const k = keyFromEvent(e);
				if (!k) return;
				e.preventDefault();
				if (k[0] === "end") exit();
				else handler(...k);
			});
		}
		handler = fn;
	}

	function exit() {
		if (inPhone) post({ type: "exit" });
		else location.href = PHONE + "/";
	}

	// Inside the phone the phone plays the sound: browsers only allow audio in the frame the user touched.
	let audio;
	function beep(freq = 880, ms = 60) {
		if (inPhone) return post({ type: "beep", freq, ms });
		try {
			audio ||= new AudioContext();
			const o = audio.createOscillator(), g = audio.createGain();
			o.type = "square"; o.frequency.value = freq; g.gain.value = 0.04;
			o.connect(g).connect(audio.destination);
			o.start(); o.stop(audio.currentTime + ms / 1000);
		} catch { /* no audio, no beep */ }
	}

	// Multi-tap typing: press 2 three times for "c"; a pause or another key accepts the letter.
	// # cycles the mode: Abc (capital at the start of a sentence), abc, ABC, 123.
	const LETTERS = { 1: ".,?!'-1", 2: "abc2", 3: "def3", 4: "ghi4", 5: "jkl5", 6: "mno6", 7: "pqrs7", 8: "tuv8", 9: "wxyz9", 0: " 0" };
	const MODES = ["Abc", "abc", "ABC", "123"];
	function multitap(onChange, max = 120) {
		let text = "", mode = "Abc", last = null, i = 0, timer = 0;
		const settle = () => { clearTimeout(timer); last = null; };
		const upper = () => mode === "ABC" || (mode === "Abc" && /(^|[.!?]\s+)$/.test(text));
		return {
			get text() { return text; },
			get mode() { return mode; },
			get pending() { return last !== null; }, // the last letter can still change
			set(t) { settle(); text = t; },
			key(k, ch) { // returns false for keys it does not use
				if (k === "#") { settle(); mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length]; }
				else if (k === "clear") { settle(); text = text.slice(0, -1); }
				else if (k === "char") { settle(); if (text.length < max) text += ch; }
				else if (k in LETTERS) {
					if (mode === "123") { settle(); if (text.length < max) text += k; }
					else if (k === last) {
						text = text.slice(0, -1);
						i = (i + 1) % LETTERS[k].length;
						text += upper() ? LETTERS[k][i].toUpperCase() : LETTERS[k][i];
					} else {
						settle();
						if (text.length >= max) { onChange(); return true; }
						last = k; i = 0;
						text += upper() ? LETTERS[k][0].toUpperCase() : LETTERS[k][0];
					}
					if (last) { clearTimeout(timer); timer = setTimeout(() => { last = null; onChange(); }, 900); }
				} else return false;
				onChange();
				return true;
			},
		};
	}

	window.clunck = {
		onKey,
		softkeys: (left = "", center = "", right = "") => post({ type: "softkeys", left, center, right }),
		exit,
		beep,
		multitap,
		keyFromEvent,
	};
})();
