// Checks multi-tap typing in the clunck kit. Run: node apps/shared/multitap.test.js
const assert = require("assert");
globalThis.location = { host: "todo.housepage.dev", protocol: "https:" };
globalThis.window = globalThis; globalThis.parent = globalThis;
globalThis.addEventListener = () => {};
require("./clunck-app.js");
const timers = [];
globalThis.setTimeout = (fn) => { timers.push(fn); return timers.length; };
globalThis.clearTimeout = () => {};
const pause = () => timers.splice(0).forEach((f) => f());
const m = clunck.multitap(() => {});
const type = (...keys) => keys.forEach((k) => m.key(k));
type("4", "4"); pause();                 // h: 4 twice; capital at the start
assert.equal(m.text, "H");
type("4", "4", "4"); pause();            // i, lowercase after the first letter
assert.equal(m.text, "Hi");
type("2", "2", "2", "2", "2");           // wraps: a b c 2 a
assert.equal(m.text, "Hia"); assert.ok(m.pending);
type("3");                               // another key accepts the a, starts d
assert.equal(m.text, "Hiad"); pause(); assert.ok(!m.pending);
type("clear"); assert.equal(m.text, "Hia");
type("1", "0"); pause();                 // ". " then a capital again
type("2"); pause(); assert.equal(m.text, "Hia. A");
type("#", "2"); pause(); assert.equal(m.mode, "abc"); assert.equal(m.text, "Hia. Aa");
type("#", "#", "7", "7"); assert.equal(m.mode, "123"); assert.equal(m.text, "Hia. Aa77");
m.key("char", "!"); assert.equal(m.text, "Hia. Aa77!");
assert.equal(m.key("up"), false);
console.log("multitap ok");
