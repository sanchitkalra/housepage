// clunck, the phone: home screen, app menu, and each app (its own housepage site) running in an iframe.
// Key presses go to the open app by postMessage; the app sends back its softkey labels, beeps and exit.
const BASE = location.host.split(".").slice(1).join("."); // housepage.dev, keeping a port if there is one
const appUrl = (id) => `${location.protocol}//${id}.${BASE}/`;
const APPS = [ // ponytail: a fixed list; adding an app means editing this and republishing clunck
	{ id: "snake", name: "Snake", icon: "🐍" },
	{ id: "todo", name: "To-do", icon: "✅" },
];
const COLS = 3;
const $ = (id) => document.getElementById(id);

let screen = "home", selected = 0, openApp = null;

function softkeys(left = "", center = "", right = "") {
	$("sk-left").textContent = left;
	$("sk-center").textContent = center;
	$("sk-right").textContent = right;
}

function show(name) {
	screen = name;
	$("home").hidden = name !== "home";
	$("menu").hidden = name !== "menu";
	$("app").hidden = name !== "app";
	if (name === "home") softkeys("Menu", "", "");
	if (name === "menu") { renderMenu(); softkeys("Select", "", "Back"); }
}

function renderMenu() {
	$("grid").replaceChildren(...APPS.map((app, i) => {
		const tile = document.createElement("div");
		tile.className = "tile" + (i === selected ? " sel" : "");
		const icon = document.createElement("span");
		icon.className = "icon";
		icon.textContent = app.icon;
		const label = document.createElement("span");
		label.textContent = `${i + 1} ${app.name}`;
		tile.append(icon, label);
		tile.addEventListener("click", () => { selected = i; open(i); });
		return tile;
	}));
}

function open(i) {
	openApp = APPS[i];
	const frame = $("app");
	frame.onload = () => { $("loading").hidden = true; frame.focus(); };
	frame.src = appUrl(openApp.id);
	$("loading").hidden = false;
	show("app");
	softkeys();
}

function close() {
	const frame = $("app");
	frame.onload = null;
	frame.src = "about:blank";
	$("loading").hidden = true;
	openApp = null;
	show("menu");
}

function press(key, char) {
	navigator.vibrate?.(8);
	if (screen === "app") {
		if (key === "end") return close();
		$("app").contentWindow?.postMessage({ type: "key", key, char }, new URL(appUrl(openApp.id)).origin);
		return;
	}
	if (key === "end") return show("home");
	if (screen === "home") {
		if (key === "soft-left" || key === "ok") show("menu");
		return;
	}
	// the menu
	const n = APPS.length;
	if (key === "left") selected = (selected - 1 + n) % n;
	else if (key === "right") selected = (selected + 1) % n;
	else if (key === "up" && selected - COLS >= 0) selected -= COLS;
	else if (key === "down" && selected + COLS < n) selected += COLS;
	else if (/^[1-9]$/.test(key) && +key <= n) return open(+key - 1);
	else if (key === "ok" || key === "soft-left") return open(selected);
	else if (key === "soft-right" || key === "back") return show("home");
	renderMenu();
}

// messages from the open app, and only from it
let audio;
addEventListener("message", (e) => {
	if (!openApp || e.source !== $("app").contentWindow || e.origin !== new URL(appUrl(openApp.id)).origin) return;
	const m = e.data || {};
	if (m.type === "softkeys") softkeys(String(m.left || ""), String(m.center || ""), String(m.right || ""));
	else if (m.type === "exit") close();
	else if (m.type === "beep") {
		try {
			audio ||= new AudioContext();
			const o = audio.createOscillator(), g = audio.createGain();
			o.type = "square";
			o.frequency.value = Math.min(Math.max(+m.freq || 880, 100), 4000);
			g.gain.value = 0.04;
			o.connect(g).connect(audio.destination);
			o.start(); o.stop(audio.currentTime + Math.min(+m.ms || 60, 500) / 1000);
		} catch { /* no audio, no beep */ }
	}
});

// the keypad
const DIGITS = [["1", ".,?!"], ["2", "abc"], ["3", "def"], ["4", "ghi"], ["5", "jkl"], ["6", "mno"],
	["7", "pqrs"], ["8", "tuv"], ["9", "wxyz"], ["*", "+"], ["0", "␣"], ["#", "⇧"]];
document.querySelector(".digits").append(...DIGITS.map(([key, sub]) => {
	const b = document.createElement("button");
	b.dataset.key = key;
	b.setAttribute("aria-label", key);
	b.innerHTML = `<b>${key}</b><small>${sub}</small>`;
	return b;
}));
document.querySelectorAll("#keypad button").forEach((b) => {
	// pointerdown, not click: it fires at once, and preventDefault keeps the keyboard focus where it was
	b.addEventListener("pointerdown", (e) => {
		e.preventDefault();
		audio?.resume?.();
		b.classList.add("down");
		press(b.dataset.key);
	});
	for (const t of ["pointerup", "pointerleave", "pointercancel"]) b.addEventListener(t, () => b.classList.remove("down"));
	b.addEventListener("click", (e) => { if (e.detail === 0) press(b.dataset.key); }); // Enter or Space on a focused button
});

// the real keyboard (when the app's frame has focus, the app gets the keys itself)
addEventListener("keydown", (e) => {
	const k = clunck.keyFromEvent(e);
	if (!k || (k[0] === "char" && screen !== "app")) return;
	e.preventDefault();
	press(...k);
});

// the clock
const clock = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit" });
function tick() {
	const now = new Date();
	$("status-time").textContent = clock.format(now);
	// the big clock draws AM/PM small, so a 12-hour time fits the screen
	const parts = clock.formatToParts(now);
	const small = document.createElement("small");
	small.textContent = parts.filter((p) => p.type === "dayPeriod").map((p) => p.value).join("");
	$("home-time").replaceChildren(parts.filter((p) => p.type !== "dayPeriod").map((p) => p.value).join("").trim(), small);
	$("home-date").textContent = now.toLocaleDateString([], { weekday: "long", day: "numeric", month: "long" });
}
tick();
setInterval(tick, 10_000);

// Fit the phone to the window. On a phone (the max-width: 500px rules in shell.css) only the screen is
// scaled and the keypad takes the rest; elsewhere the whole handset is scaled as one picture.
const phoneLayout = matchMedia("(max-width: 500px)");
function fit() {
	const phone = $("phone"), screenEl = $("screen"), box = $("screen-box"), stage = $("stage");
	const pad = getComputedStyle(document.body); // the safe areas, when the page runs under the status bar
	const height = innerHeight - parseFloat(pad.paddingTop) - parseFloat(pad.paddingBottom);
	for (const el of [phone, screenEl]) el.style.transform = "";
	for (const el of [stage, box]) { el.style.width = ""; el.style.height = ""; }
	if (phoneLayout.matches) {
		const width = document.body.clientWidth - parseFloat(pad.paddingLeft) - parseFloat(pad.paddingRight);
		const s = Math.min(width / 240, (height * 0.56) / 320); // the keypad keeps at least 44% of the height
		screenEl.style.transform = `scale(${s})`;
		box.style.width = `${240 * s}px`;
		box.style.height = `${320 * s}px`;
		return;
	}
	const w = phone.offsetWidth, h = phone.offsetHeight;
	const s = Math.min((innerWidth - 16) / w, (height - 16 - $("hint").offsetHeight) / h, 1.5);
	phone.style.transform = `scale(${s})`;
	stage.style.width = `${w * s}px`;
	stage.style.height = `${h * s}px`;
}
addEventListener("resize", fit);
fit();
show("home");
