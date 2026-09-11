import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import {
  SSEParser, activeRequestLabel, bytes, canCancelTask, canRunWorkspaceShell, classifyStatus, commandText, completionDelta, completionState, decodeRate, draftStats, liveDecodeRate,
  errorMessage, escapeHTML, finite, generationSettings, healthStatus, importedTaskSpec, isSuccess, isTerminal, lifecycleControlState, normalizeTask,
  IT_LABELS, markdownBlocks, markdownInline, number, observedRate, pathList, percent, safeSourceURL, seconds, textBlocks,
} from '../ui-core.mjs';

function parseChunks(chunks) {
  const events = [];
  const parser = new SSEParser(event => events.push(event));
  for (const chunk of chunks) parser.push(chunk);
  parser.finish();
  return events;
}

test('SSE JSON and DONE survive every possible text chunk boundary', () => {
  const input = ': heartbeat\r\ndata: {"choices":[{"delta":{"content":"héllo 🌱"}}]}\r\n\r\ndata: [DONE]\n\n';
  const expected = parseChunks([input]);
  assert.equal(expected.length, 2);
  assert.equal(JSON.parse(expected[0].data).choices[0].delta.content, 'héllo 🌱');
  for (let split = 0; split <= input.length; split++) {
    assert.deepEqual(parseChunks([input.slice(0, split), input.slice(split)]), expected);
  }
  assert.deepEqual(parseChunks([...input]), expected);
});

test('SSE TextDecoder handles UTF-8 split inside multibyte characters', () => {
  const encoded = new TextEncoder().encode('data: {"text":"à界🛠️"}\n\n');
  for (let split = 0; split <= encoded.length; split++) {
    const decoder = new TextDecoder();
    const parsed = parseChunks([
      decoder.decode(encoded.slice(0, split), { stream: true }),
      decoder.decode(encoded.slice(split), { stream: true }), decoder.decode(),
    ]);
    assert.deepEqual(JSON.parse(parsed[0].data), { text: 'à界🛠️' });
  }
});

test('SSE supports BOM, multiline data, comments, persistent id and CR newlines', () => {
  const parsed = parseChunks(['\uFEFFid: 21\revent: update\rdata: first\rdata: second\r\r: ping\rdata:\r\r']);
  assert.deepEqual(parsed, [
    { event: 'update', data: 'first\nsecond', id: '21' },
    { event: 'message', data: '', id: '21' },
  ]);
});

test('SSE discards incomplete events and rejects oversized ones', () => {
  assert.deepEqual(parseChunks(['data: incomplete\n']), []);
  assert.deepEqual(parseChunks(['data: incomplete']), []);
  assert.throws(() => new SSEParser(() => {}, 8).push('data: 12345'), /size limit/);
  const events = [];
  const parser = new SSEParser(event => events.push(event), 20);
  for (let index = 0; index < 100; index++) parser.push('data: hi\n\n');
  assert.equal(events.length, 100);
});

test('SSE ignores NUL ids and does not strip extra whitespace', () => {
  const parsed = parseChunks(['id: retained\nid: ignored\0id\ndata:  two spaces before value\n\n']);
  assert.equal(parsed[0].id, 'retained');
  assert.equal(parsed[0].data, ' two spaces before value');
});

test('OpenAI delta accepts separate reasoning, usage-only packets and no choice', () => {
  assert.deepEqual(completionDelta({ choices: [{ delta: { content: 'code', reasoning_content: 'think' }, finish_reason: 'stop' }] }), {
    content: 'code', reasoning: 'think', finish: 'stop', usage: null, timings: null,
  });
  assert.equal(completionDelta({ choices: [], usage: { completion_tokens: 21 } }).usage.completion_tokens, 21);
  assert.equal(completionDelta({ choices: [{ delta: { content: { evil: true } } }] }).content, '');
  assert.equal(completionDelta({ choices: [{ message: { content: 'final' } }] }).content, 'final');
});

test('Unknown or non-finite metrics stay unavailable; zero is real data', () => {
  for (const value of [undefined, null, '', false, true, NaN, Infinity, 'unknown']) {
    assert.equal(finite(value), null);
    assert.equal(number(value), '—');
    assert.equal(seconds(value), '—');
    assert.equal(bytes(value), '—');
    assert.equal(percent(value), '—');
  }
  assert.equal(number(0), '0');
  assert.equal(seconds(0), '0 ms');
  assert.equal(bytes(128 * 1024 ** 3), '128 GiB');
  assert.equal(percent(0.825), '82.5%');
});

test('Live Go health objects and active task arrays are rendered semantically', () => {
  assert.equal(healthStatus({ status: 'ok', ranks: [true, true] }), 'ok');
  assert.equal(healthStatus({ status: 'poisoned' }), 'poisoned');
  assert.equal(healthStatus('healthy'), 'healthy');
  assert.equal(healthStatus({}), 'unknown');
  assert.equal(activeRequestLabel([], false), 'Idle');
  assert.equal(activeRequestLabel([], true), 'Active');
  assert.equal(activeRequestLabel(['task1', 'task2'], true), 'task1 · task2');
  assert.equal(activeRequestLabel(undefined, undefined), '—');
});

test('Lifecycle controls are fail-closed and recover from server state', () => {
  let view = lifecycleControlState({state:'OFF',cluster_owner:'NONE',start_allowed:true,coordinator:'OFF',nodes:[{engine_state:'OFF_VERIFIED'},{engine_state:'OFF_VERIFIED'}]}, false);
  assert.equal(view.onDisabled, false);
  assert.equal(view.offDisabled, true);
  assert.equal(view.poll, false);

  view = lifecycleControlState({state:'STARTING',cluster_owner:'GLM',start_allowed:false,coordinator:'OFF',nodes:[{engine_state:'ACTIVE'},{engine_state:'ACTIVE'}]}, false);
  assert.equal(view.onDisabled, true);
  assert.equal(view.offDisabled, true);
  assert.equal(view.poll, true);

  view = lifecycleControlState({state:'READY',cluster_owner:'GLM',start_allowed:false,coordinator:'RUNNING',nodes:[{engine_state:'ACTIVE'},{engine_state:'ACTIVE'}]}, false);
  assert.equal(view.onDisabled, true);
  assert.equal(view.offDisabled, false);

  view = lifecycleControlState({state:'ERROR',cluster_owner:'UNKNOWN',start_allowed:false,coordinator:'OFF',nodes:[{engine_state:'OFF_VERIFIED'},{engine_state:'UNKNOWN'}]}, false);
  assert.equal(view.onDisabled, true);
  assert.equal(view.offDisabled, true);
  assert.equal(view.owner, 'UNKNOWN');

  view = lifecycleControlState({state:'OFF',cluster_owner:'DS41',start_allowed:false,coordinator:'OFF',nodes:[{engine_state:'OFF_VERIFIED'},{engine_state:'OFF_VERIFIED'}]}, false);
  assert.equal(view.onDisabled, true);
  assert.equal(view.offDisabled, true);
});

test('Live CIRU metrics calculate decode from measured generation time, not HTTP TPS', () => {
  const payload = { usage: { completion_tokens: 101 }, metrics: { generation_time_ms: 4000, tokens_per_second: 13 } };
  const delta = completionDelta(payload);
  assert.equal(decodeRate(delta.timings, delta.usage), 25);
  assert.equal(decodeRate({ tokens_per_second: 13 }, payload.usage), null);
  assert.equal(decodeRate({ generation_time_ms: 0 }, payload.usage), null);
  assert.equal(decodeRate({ decode_tps: 24.8 }, payload.usage), 24.8);
});

test('Task status never treats incomplete or merely completed as passing', () => {
  for (const status of ['INCOMPLETE', 'FAILED', 'cancelled', 'completed', 'timeout', 'INTERRUPTED']) {
    assert.ok(isTerminal(status));
    assert.equal(isSuccess(status), false);
  }
  for (const status of ['queued', 'running', 'draining', 'applying', 'APPLYING']) assert.equal(isTerminal(status), false);
  assert.ok(isSuccess('PASS'));
  assert.equal(classifyStatus('unhealthy'), 'bad');
  assert.equal(classifyStatus('running'), 'neutral');
});

test('Applying remains nonterminal but cannot cancel or claim a passing result', () => {
  assert.equal(canCancelTask('applying'), false);
  assert.equal(canCancelTask('APPLYING'), false);
  assert.equal(canCancelTask('draining'), false);
  assert.equal(canCancelTask('PASS'), false);
  assert.equal(canCancelTask(undefined), false);
  assert.equal(canCancelTask('running'), true);
  assert.equal(canCancelTask('QUEUED'), true);
  assert.equal(isSuccess('applying'), false);
  assert.equal(isTerminal('applying'), false);
});

test('finish_reason stop without SSE DONE is not a completed response', () => {
  assert.equal(completionState(false, 'stop', 'apparently final'), 'incomplete-stream');
  assert.equal(completionState(false, 'length', 'partial'), 'incomplete-stream');
  assert.equal(completionState(true, 'length', 'partial'), 'incomplete-cap');
  assert.equal(completionState(true, 'stop', ''), 'no-complete-final');
  assert.equal(completionState(true, 'tool_calls', 'text'), 'no-complete-final');
  assert.equal(completionState(true, 'stop', 'final'), 'complete');
});

test('Task envelopes preserve authoritative top-level status', () => {
  const result = normalizeTask({ task: { id: 't1', status: 'cancelled', result: { status: 'pass', files: { 'a.c': '' }, patch: '+a' } } });
  assert.equal(result.status, 'cancelled');
  assert.equal(result.id, 't1');
  assert.deepEqual(result.files_changed, ['a.c']);
  assert.equal(result.diff, '+a');
  assert.deepEqual(result.attempts, []);
});

test('Path lists deduplicate, preserve path text and cannot become HTML', () => {
  assert.deepEqual(pathList(' src/a.c\ninclude/a.h, src/a.c\n'), ['src/a.c', 'include/a.h']);
  assert.equal(escapeHTML('<img src=x onerror="alert(1)">&\''), '&lt;img src=x onerror=&quot;alert(1)&quot;&gt;&amp;&#39;');
  assert.equal(errorMessage({ error: { message: '<script>x</script>' } }), '<script>x</script>');
  assert.equal(errorMessage({ error: {} }, 'fallback'), 'fallback');
});

test('Task JSON import allows only task fields, never automatic apply or credentials', () => {
  const input = { task: 'Fix a parser', repo: '/repo', allowed_paths: ['src/a.c'], files: ['src/a.c'], test_command: ['./verify'], build_command: ['cc', 'src/a.c', '-o', 'file with space'], profile: 'fast', test_files: { 'verify.c': '/independent/verify.c' }, apply: true, auth: 'secret', endpoint: 'https://example.com', sandbox_policy: 'host', max_tokens: 4096 };
  const spec = importedTaskSpec(input);
  assert.equal(spec.apply, undefined);
  assert.equal(spec.auth, undefined);
  assert.equal(spec.endpoint, undefined);
  assert.equal(spec.sandbox_policy, undefined);
  assert.deepEqual(spec.test_files, { 'verify.c': '/independent/verify.c' });
  assert.equal(spec.build_command, "cc src/a.c -o 'file with space'");
  assert.equal(commandText(['echo', "it's", '']), "echo 'it'\\''s' ''");
  assert.throws(() => importedTaskSpec({ ...input, max_tokens: 0 }), /Invalid max_tokens/);
  assert.equal(importedTaskSpec({ ...input, max_tokens: 32768 }).max_tokens, 32768);
  assert.throws(() => importedTaskSpec({ ...input, max_tokens: 32769 }), /Invalid max_tokens/);
  assert.equal(spec.profile, undefined);
  assert.equal(importedTaskSpec({ ...input, reasoning_effort: 'max' }).reasoning_effort, 'max');
  assert.throws(() => importedTaskSpec({ ...input, max_repairs: 7 }), /Invalid max_repairs/);
  assert.equal(importedTaskSpec({ ...input, max_repairs: 6 }).max_repairs, 6);
  assert.equal(importedTaskSpec({ ...input, timeout: 1800 }).timeout, 1800);
  assert.throws(() => importedTaskSpec({ ...input, timeout: 1801 }), /Invalid timeout/);
  assert.throws(() => importedTaskSpec({ ...input, timeout: 7200 }), /Invalid timeout/);
  assert.throws(() => importedTaskSpec({ ...input, test_files: [] }), /test_files/);
  assert.throws(() => importedTaskSpec({ ...input, test_command: [1] }), /Commands/);
});

test('Frontend source has no dynamic HTML/eval, persisted token, CDN or external dependency', async () => {
  const script = await readFile(new URL('../app.js', import.meta.url), 'utf8');
  const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
  assert.doesNotMatch(script, /(?:innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\s*\(|new Function|sessionStorage)/);
  const persistedKeys = [...script.matchAll(/localStorage\.(?:getItem|setItem)\('([^']+)'/g)].map(match => match[1]);
  assert.deepEqual(persistedKeys, ['haloclu.preferences', 'strixglm.language', 'haloclu.preferences']);
  assert.match(script, /localStorage\.setItem\('haloclu\.preferences', JSON\.stringify\(state\.preferences\)\)/);
  assert.match(script, /state\.preferences = displayPreferences\(value\)/);
  assert.doesNotMatch(html, /(?:src|href)=["']https?:\/\//);
  assert.match(script, /textContent/);
  assert.match(script, /window\.confirm/);
  assert.match(script, /apply: false/);
  assert.equal([...script.matchAll(/credentials: 'same-origin'/g)].length, 3, 'All three API fetch paths use the remembered session only on this origin');
  assert.equal([...script.matchAll(/referrerPolicy: 'same-origin'/g)].length, 3, 'Cookie-backed mutations need a same-origin Origin even with the page no-referrer policy');
  assert.doesNotMatch(script, /credentials: 'include'|document\.cookie/);
  assert.match(script, /'X-HaloClu-Session': '1'/);
  assert.match(script, /request\('\/v1\/auth\/session', \{ method: 'POST'/);
  assert.match(script, /request\('\/v1\/auth\/session', \{ method: 'DELETE'/);
  assert.match(html, /id="code-repairs"[^>]*max="6"/);
  assert.match(html, /id="code-timeout"[^>]*max="600"/);
  assert.match(script, /options\.generation_timeout_seconds/);
  assert.match(html, /<select id="chat-cap"/);
  assert.match(html, /value="32768"/);
  assert.match(script, /generationSettings/);
  assert.doesNotMatch(html, /data-profile|code-profile|Fast|Balanced|Quality/);
  assert.match(script, /continuous_usage_stats: true/);
  assert.match(html, /Low does not disable thinking/);
  assert.match(html, /force reasoning closure/);
  assert.match(html, /Last attempt TPS/);
  assert.match(html, /<html lang="en">/);
  assert.match(script, /completionState\(done, finish, text\)/);
  const referencedIDs = [...script.matchAll(/\$\('([^']+)'\)/g)].map(match => match[1]);
  for (const id of referencedIDs) assert.ok(html.includes(`id="${id}"`), `Missing HTML element: ${id}`);
  const definedIDs = [...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]);
  assert.equal(new Set(definedIDs).size, definedIDs.length, 'Duplicate HTML IDs');
});

test('Markdown fences are removed and code is never parsed as HTML or inline markup', () => {
  assert.deepEqual(markdownBlocks('```c\nint main() { return 0; }\n```'), [{ type: 'code', language: 'c', text: 'int main() { return 0; }' }]);
  assert.equal(markdownBlocks('~~~~html\n<script>alert(1)</script>\n~~~~')[0].text, '<script>alert(1)</script>');
  assert.equal(markdownBlocks('```cpp\npartial output')[0].text, 'partial output');
  assert.equal(markdownBlocks('````js\n```nested\n````')[0].text, '```nested');
});

test('Markdown supports headings, list starts, quotes, tables and safe inline formatting', () => {
  const blocks = markdownBlocks('# Heading\n\n3. first\n4. second\n\n> quote\n\n| name | value |\n|---|---:|\n| x | `1` |');
  assert.deepEqual(blocks.map(block => block.type), ['heading', 'list', 'quote', 'table']);
  assert.equal(blocks[1].start, 3);
  assert.deepEqual(blocks[3].rows, [['x', '`1`']]);
  assert.deepEqual(markdownInline('**bold** `code` *em*').filter(token => token.type !== 'text').map(token => token.type), ['strong', 'code', 'em']);
  assert.equal(markdownInline('[safe](https://example.com)')[0].href, 'https://example.com/');
  assert.equal(markdownInline('[bad](javascript:alert)')[0].type, 'text');
});

test('English is the default; Italian includes attachment and workspace controls', () => {
  for (const label of ['Language', 'Coding workspace', 'Attach files', 'Export JSON', 'Create session', 'Terminal diagnostics']) assert.equal(typeof IT_LABELS[label], 'string');
});

test('Custom workspace shell requires explicit backend capability and idle READY state', () => {
  assert.equal(canRunWorkspaceShell({ state: 'READY', capabilities: { shell: true } }), true);
  for (const state of ['CONNECTED', 'RUNNING', 'STARTING', 'ABORTING', 'CLOSED']) assert.equal(canRunWorkspaceShell({ state, capabilities: { shell: true } }), false);
  assert.equal(canRunWorkspaceShell({ state: 'READY', capabilities: { terminal: true } }), false);
  assert.equal(canRunWorkspaceShell(null), false);
});

test('Live decode excludes prefill; HTTP rate includes it and final engine rate takes precedence', () => {
  const usage = {completion_tokens:575};
  assert.equal(liveDecodeRate(usage, 21000, 69000), 574/48);
  assert.equal(observedRate(usage, 69), 575/69);
  assert.equal(decodeRate({generation_time_ms:48000}, usage), 574/48);
  for (const first of [null, undefined, NaN, Infinity, -1, 69000, 70000]) assert.equal(liveDecodeRate(usage, first, 69000), null);
  for (const completion_tokens of [null, undefined, '575', 0, 1, 1.5]) assert.equal(liveDecodeRate({completion_tokens}, 21000, 69000), null);
  assert.equal(liveDecodeRate({completion_tokens:2}, 0, 1000), 1);
  assert.equal(liveDecodeRate({completion_tokens:2}, 1000, 1001), null, 'No artificial spike from a sub-millisecond first event');
  assert.equal(liveDecodeRate(usage, 21000, NaN), null);
});

test('Draft statistics use real current or persisted engine metrics, with zero acceptance valid', () => {
  const spec = {draft_acceptance_rate:0.22279411764705884, mean_acceptance_length:2.1139705882352944};
  assert.deepEqual(draftStats({speculative_decoding:spec}), {acceptance:spec.draft_acceptance_rate,length:spec.mean_acceptance_length});
  assert.deepEqual(draftStats({raw:{speculative_decoding:spec}}), draftStats({speculative_decoding:spec}));
  assert.deepEqual(draftStats({acceptance:0}), {acceptance:0,length:null});
  assert.deepEqual(draftStats(null), {acceptance:null,length:null});
  assert.deepEqual(draftStats({speculative_decoding:{draft_acceptance_rate:1.2,mean_acceptance_length:0}}), {acceptance:null,length:null});
});

test('Observed TPS uses cumulative real token usage, never text or stream chunks', () => {
  assert.equal(observedRate({ completion_tokens: 40 }, 2), 20);
  assert.equal(observedRate({ completion_tokens: 0 }, 2), 0);
  for (const value of [undefined, '40', 2.1, -1]) assert.equal(observedRate({ completion_tokens: value }, 2), null);
  assert.equal(observedRate({ content: 'a'.repeat(1000), chunks: 50 }, 2), null);
  assert.equal(observedRate({ completion_tokens: 40 }, 0), null);
});

test('Generation controls submit explicit reasoning and total window; auto omits output cap', () => {
  const options = { reasoning_modes: ['low', 'high', 'max'], context_options: [4096, 8192, 65536], max_output_tokens: 32768 };
  assert.deepEqual(generationSettings('max', '65536', '', options), { reasoning_effort: 'max', context_tokens: 65536 });
  assert.equal(generationSettings('low', '8192', '32768', options).max_tokens, 32768); // Server rejects actual input + output overflow before GPU.
  assert.throws(() => generationSettings('fast', '8192', '', options), /Reasoning/);
  assert.throws(() => generationSettings('low', '9999', '', options), /context/);
  assert.throws(() => generationSettings('low', '8192', '32769', options), /Response/);
});

test('Safe prose/code blocks preserve all text without interpreting markup', () => {
  const original = 'Prosa <img src=x>\n```js\n<script>alert(1)</script>\n```\nFine.';
  const blocks = textBlocks(original);
  assert.equal(blocks.map(block => block.text).join(''), original);
  assert.equal(blocks.filter(block => block.code).length, 1);
  assert.equal(textBlocks('```incomplete')[0].code, false);
});

test('Catalog links reject executable protocols and credential-bearing URLs', () => {
  assert.equal(safeSourceURL('https://example.com/revision'), 'https://example.com/revision');
  for (const url of ['javascript:alert(1)', 'data:text/html,x', 'file:///etc/passwd', 'https://token@example.com', '/local/path']) assert.equal(safeSourceURL(url), null);
});

test('Go explicitly embeds production frontend and brand assets, not tests or documentation', async () => {
  const source = await readFile(new URL('../assets.go', import.meta.url), 'utf8');
  const match = source.match(/^\/\/go:embed (.+)$/m);
  assert.ok(match, 'Missing explicit production asset embed declaration');
  assert.deepEqual(match[1].trim().split(/\s+/).sort(), ['app.js', 'assets/haloclu-horizontal.png', 'assets/haloclu-icon.png', 'assets/haloclu-social.png', 'downloads.mjs', 'favicon.ico', 'index.html', 'prompt-meter.mjs', 'styles.css', 'ui-core.mjs']);
});
