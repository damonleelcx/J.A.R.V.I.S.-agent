#!/usr/bin/env node
/* Does the echo guard tell FORGE's own voice from a real interruption?
 *
 * In hands-free the microphone is open while she speaks, so it hears the
 * speakers. Every word she said came back as a transcript, was submitted as
 * though the person had said it, answered, spoken, and heard again. Observed in
 * production on 2026-09-07: "hello I am" and "Forge" arrived as user messages,
 * both fragments of her own replies.
 *
 * The Go fence (internal/httpapi/voice_echo_test.go) asserts the guard EXISTS
 * and runs before the transcript is submitted. It cannot assert the guard is
 * RIGHT — that needs the function run against real transcripts, which is this.
 *
 * The cases below are not invented: the echo ones are what actually appeared in
 * the loop. The interruption ones are what must still get through, because
 * closing the mic while she speaks would end the loop and end barge-in with it.
 *
 *   make test-echo    (or: node scripts/echo-guard-check.js)
 */
// in production, plus genuine interruptions that must still get through.
const fs = require('fs');
const src = fs.readFileSync(__dirname + '/../internal/httpapi/assets/voice.js', 'utf8');
const g = { window:{}, document:{addEventListener(){},removeEventListener(){}},
            addEventListener(){}, removeEventListener(){}, setTimeout, clearTimeout };
g.global = g;
new Function('global', src.replace('})(window);', '})(global);'))(g);

const V = g.ForgeVoice.Voice;
const v = Object.create(V.prototype);
v._spoken = ''; v._echoTail = null;

let fail = 0;
function check(spoken, heard, wantEcho, why) {
  v._nowSpeaking(spoken);
  const got = v._isOwnEcho(heard);
  const ok = got === wantEcho;
  if (!ok) fail++;
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  heard=${JSON.stringify(heard).padEnd(34)} -> ${got ? 'echo' : 'interruption'}  ${why}`);
}

const reply1 = 'Hello. I am FORGE, your engineering partner. What are we building or analyzing today?';
const reply2 = 'Hello. I am ready to work on your design or engineering tasks.';

console.log('--- fragments actually seen in the loop (must be ECHO) ---');
check(reply1, 'hello I am', true, 'from the screenshot');
check(reply1, 'Forge',      true, 'from the screenshot');
check(reply2, 'hello',      true, 'from the screenshot');
check(reply1, 'what are we building', true, 'her own question');

console.log('--- real interruptions (must still get through) ---');
check(reply1, 'stop',                     false, 'a barge-in');
check(reply1, 'design me a bracket',      false, 'new instruction');
check(reply1, 'no wait use aluminium',    false, 'correction mid-reply');

console.log('--- nothing being spoken: never echo ---');
v._spoken = '';
console.log(`  ${v._isOwnEcho('hello') === false ? 'PASS' : 'FAIL'}  silence -> interruption`);
if (v._isOwnEcho('hello') !== false) fail++;

console.log(fail === 0 ? '\nALL CLASSIFICATIONS CORRECT' : `\n${fail} MISCLASSIFIED`);
process.exit(fail ? 1 : 0);
