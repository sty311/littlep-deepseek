// PC-only component test. The firmware bytecode is never evaluated here.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { SourceTextModule, SyntheticModule, createContext } from 'node:vm';

const here = new URL('.', import.meta.url);
const source = readFileSync(new URL('../patch/templates/RobotMessage-e079798d.js', here), 'utf8');
const reasoning='合成思考测试。'.repeat(250).slice(0,575), answer='合成答案测试。'.repeat(90).slice(0,479);
const proof=text=>({type:text===reasoning?'reasoningText':'text',tts_source_empty:true,content:text,characters:[...text].length,sha256:createHash('sha256').update(text).digest('hex')});
const fixture={matched:{reasoning:proof(reasoning),answer:proof(answer)},expected:{reasoning:proof(reasoning).sha256,answer:proof(answer).sha256}};
const messages = [];
const originalCalls = [];
const listeners = new Map();
const events = [];
const falcon = {
  on(name, callback) {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name).add(callback);
  },
  off(name, callback) { listeners.get(name)?.delete(callback); },
  trigger(name, data) {
    events.push({ name, data });
    for (const callback of [...(listeners.get(name) || [])]) callback({ data });
  },
  clear() { listeners.clear(); events.length = 0; }
};
const originalComponent = {
  name: 'OriginalRobotMessage',
  props: { message: Object },
  render(h) {
    originalCalls.push({ message: this.message, history: this.isHistoryMessage });
    return h('original-answer', { props: { message: this.message } });
  }
};
const scrollerComponent = { name: 'VerticalScroller' };
const context = createContext({
  console: { warn(value) { messages.push(String(value)); } },
  Array, Math, Object, String, $falcon: falcon
});
const main = new SourceTextModule(source, { context, identifier: 'RobotMessage-e079798d.js' });
await main.link(async specifier => {
  if (specifier === './RobotMessage-original-e079798d.js') {
    return new SyntheticModule(['default'], function () { this.setExport('default', originalComponent); }, { context });
  }
  if (specifier === './VerticalScroller-8d348f67.js') {
    return new SyntheticModule(['default'], function () { this.setExport('default', scrollerComponent); }, { context });
  }
  throw Error('unexpected import: ' + specifier);
});
await main.evaluate();
const wrapper = main.namespace.default;

const h = (tag, data, children) => ({ tag, data: data || {}, children: children || [] });
const sha256 = value => createHash('sha256').update(value, 'utf8').digest('hex');
const walk = (node, match) => {
  if (!node || typeof node !== 'object') return null;
  if (match(node)) return node;
  for (const child of node.children || []) {
    const found = walk(child, match);
    if (found) return found;
  }
  return null;
};
function reasoningInstance(message, options = {}) {
  const vnode = wrapper.render.call({ message, isHistoryMessage: false,
    currentChatId: options.currentChatId ?? 'test-chat',
    isLastMessage: options.isLastMessage ?? true }, h);
  assert.equal(vnode.tag.name, 'CustomReasoningCard');
  const component = vnode.tag;
  const instance = { ...vnode.data.props };
  Object.assign(instance, component.data.call(instance));
  for (const [name, method] of Object.entries(component.methods)) instance[name] = method.bind(instance);
  return { component, instance, vnode };
}
function renderState(state, phase) {
  const rendered = state.component.render.call(state.instance, h);
  state.component[phase].call(state.instance);
  return rendered;
}
function rightHeaderText(rendered) {
  const children = rendered.children[0].children[1].children;
  assert.equal(children.length, 1);
  assert.equal(typeof children[0], 'string');
  return children[0];
}
function updateContent(state, content) {
  state.instance.message.content = content;
  state.component.watch['message.content'].call(state.instance, content);
}
function updateStatus(state, status) {
  state.instance.message.status = status;
  state.component.watch['message.status'].call(state.instance, status);
}

const results = [];
function check(name, test) {
  try { test(); results.push({ name, ok: true }); }
  catch (error) { results.push({ name, ok: false, error: String(error) }); }
  finally { falcon.clear(); }
}

check('module marker and original default fields', () => {
  assert.equal(messages[0], '[MYAI_UI] patch_loaded version=custom-websearch-r4-1');
  assert.equal(wrapper.name, originalComponent.name);
  assert.equal(wrapper.props, originalComponent.props);
  assert.notEqual(wrapper.render, originalComponent.render);
});

check('575-character live reasoning renders without per-batch SHA256', () => {
  const proof = fixture.matched.reasoning;
  assert.equal(proof.characters, 575);
  assert.equal(proof.type, 'reasoningText');
  assert.equal(proof.tts_source_empty, true);
  assert.equal(sha256(proof.content), fixture.expected.reasoning);
  const state = reasoningInstance({ messageType: proof.type, content: proof.content, status: 'MessageReceiving' });
  assert.equal(state.instance.reasoningText, proof.content);
  assert.equal(state.instance.reasoningVisible, true);
  assert.equal(state.instance.reasoningStatus, 'thinking');
  assert.equal(state.instance.reasoningExpanded, true);
  const rendered = renderState(state, 'mounted');
  assert.equal(rendered.tag, 'div');
  assert.equal(rightHeaderText(rendered), '思考中 收起');
  const scroller = walk(rendered, node => node.tag === scrollerComponent);
  assert.ok(scroller);
  assert.equal(scroller.data.style.maxHeight, '110px');
  assert.equal(scroller.children[0].children[0], proof.content);
  assert.ok(messages.some(x => x.includes('phase=mounted chars=575') && x.includes('status=thinking') && !x.includes('sha256=')));
});

check('empty reasoning stays hidden and can become visible on content update', () => {
  const state = reasoningInstance({ messageType: 'reasoningText', content: '', status: 'MessageReceiving' });
  assert.equal(state.instance.reasoningVisible, false);
  assert.equal(state.instance.reasoningStatus, 'idle');
  assert.equal(renderState(state, 'mounted').children.length, 0);
  updateContent(state, '开始思考');
  assert.equal(state.instance.reasoningVisible, true);
  assert.equal(state.instance.reasoningStatus, 'thinking');
  assert.equal(renderState(state, 'updated').tag, 'div');
  updateContent(state, '');
  assert.equal(state.instance.reasoningVisible, false);
});

check('done folds card, title toggles open and closed', () => {
  const state = reasoningInstance({ messageType: 'reasoningText', content: '一步', status: 'MessageReceiving' });
  updateStatus(state, 'MessageSuccess');
  assert.equal(state.instance.reasoningStatus, 'done');
  assert.equal(state.instance.reasoningExpanded, false);
  let rendered = renderState(state, 'mounted');
  assert.ok(messages.some(x => x.includes('status=done') && x.includes('sha256=' + sha256('一步'))));
  assert.equal(rightHeaderText(rendered), '思考完成 展开');
  assert.equal(walk(rendered, node => node.tag === scrollerComponent), null);
  rendered.children[0].data.on.click();
  assert.equal(state.instance.reasoningExpanded, true);
  rendered = renderState(state, 'updated');
  assert.equal(rightHeaderText(rendered), '思考完成 收起');
  assert.ok(walk(rendered, node => node.tag === scrollerComponent));
  rendered.children[0].data.on.click();
  assert.equal(state.instance.reasoningExpanded, false);
});

check('2000-character reasoning remains in a capped scroller', () => {
  const text = '思'.repeat(2000);
  const state = reasoningInstance({ messageType: 'reasoningText', content: text, status: 'MessageReceiving' });
  const rendered = renderState(state, 'mounted');
  const scroller = walk(rendered, node => node.tag === scrollerComponent);
  assert.ok(scroller);
  assert.equal(scroller.data.style.height, '110px');
  assert.equal(scroller.children[0].children[0].length, 2000);
  assert.ok(messages.some(x => x.includes('chars=2000') && !x.includes('sha256=')));
});

check('streaming content grows, logs are throttled, final SHA is exact', () => {
  const state = reasoningInstance({ messageType: 'reasoningText', content: '', status: 'MessageReceiving' });
  renderState(state, 'mounted');
  const before = messages.length;
  let text = '';
  for (let i = 0; i < 20; i++) {
    text += '思'.repeat(75);
    updateContent(state, text);
    renderState(state, 'updated');
  }
  assert.equal(state.instance.reasoningText.length, 1500);
  assert.ok(messages.length - before <= 2, 'progress logs should be throttled');
  updateStatus(state, 'MessageSuccess');
  renderState(state, 'updated');
  assert.ok(messages.some(x => x.includes('chars=1500 sha256=' + sha256(text) + ' status=done')));
});

check('card follows tail and pauses after manual upward scroll', () => {
  const state = reasoningInstance({ messageType: 'reasoningText', content: '思'.repeat(200), status: 'MessageReceiving' });
  const target = {};
  let scrolls = 0;
  state.instance.$refs = { reasoningBottom: target };
  state.instance.$page = { $dom: { scrollToElement(value) { assert.equal(value, target); scrolls++; } } };
  state.instance.$nextTick = callback => callback();
  const scroller = walk(renderState(state, 'mounted'), node => node.tag === scrollerComponent);
  assert.equal(scroller.children[1].data.ref, 'reasoningBottom');
  updateContent(state, state.instance.reasoningText + '新');
  assert.equal(scrolls, 1);
  scroller.data.on.scroll({ contentOffset: { y: 150 }, contentSize: { height: 400 } });
  scroller.data.on.scroll({ contentOffset: { y: 80 }, contentSize: { height: 400 } });
  assert.equal(state.instance.followTail, false);
  updateContent(state, state.instance.reasoningText + '段');
  assert.equal(scrolls, 1);
  scroller.data.on.scroll({ contentOffset: { y: 290 }, contentSize: { height: 400 } });
  assert.equal(state.instance.followTail, true);
  updateContent(state, state.instance.reasoningText + '尾');
  assert.equal(scrolls, 2);
  updateStatus(state, 'MessageSuccess');
  assert.equal(state.instance.reasoningExpanded, false);
  updateContent(state, state.instance.reasoningText + '完成');
  assert.equal(scrolls, 2);
  state.instance.toggleReasoning();
  assert.equal(scrolls, 3);
  assert.ok(state.instance.reasoningText.endsWith('完成'));
});

check('active thinking card acknowledges stop once and restores original completion contract', () => {
  const message = { messageType: 'reasoningText', content: '仍在思考', status: 'MessageReceiving' };
  const state = reasoningInstance(message, { currentChatId: 'cancel-chat', isLastMessage: true });
  assert.equal(state.vnode.data.props.currentChatId, 'cancel-chat');
  assert.equal(state.vnode.data.props.isLastMessage, true);
  renderState(state, 'mounted');
  falcon.trigger('setStopReceiveMessageButtonVisibleFalse');
  const completions = events.filter(x => x.name === 'appendingAnimationFinished');
  assert.equal(completions.length, 1);
  assert.equal(completions[0].data.currentChatId, 'cancel-chat');
  assert.equal(state.instance.reasoningStatus, 'stopped');
  assert.equal(state.instance.reasoningExpanded, false);
  assert.equal(rightHeaderText(state.component.render.call(state.instance, h)), '已停止 展开');
  assert.equal(message.content, '仍在思考');
  falcon.trigger('setStopReceiveMessageButtonVisibleFalse');
  assert.equal(events.filter(x => x.name === 'appendingAnimationFinished').length, 1);
  updateContent(state, '仍在思考更多');
  updateStatus(state, 'MessageSuccess');
  assert.equal(state.instance.reasoningStatus, 'stopped');
  assert.ok(!events.some(x => /tts|play/i.test(x.name)));
  state.component.beforeDestroy.call(state.instance);
  assert.equal(listeners.get('setStopReceiveMessageButtonVisibleFalse').size, 0);
});

check('normal completion, stale card, and destroyed card never acknowledge stop', () => {
  const stale = reasoningInstance({ messageType: 'reasoningText', content: '旧思考', status: 'MessageReceiving' },
    { currentChatId: 'old-chat', isLastMessage: false });
  renderState(stale, 'mounted');
  const completed = reasoningInstance({ messageType: 'reasoningText', content: '已完成', status: 'MessageSuccess' });
  renderState(completed, 'mounted');
  const destroyed = reasoningInstance({ messageType: 'reasoningText', content: '已销毁', status: 'MessageReceiving' });
  renderState(destroyed, 'mounted');
  destroyed.component.beforeDestroy.call(destroyed.instance);
  falcon.trigger('setStopReceiveMessageButtonVisibleFalse');
  assert.equal(events.filter(x => x.name === 'appendingAnimationFinished').length, 0);
  assert.equal(completed.instance.reasoningStatus, 'done');
  assert.equal(stale.instance.reasoningStatus, 'thinking');
});

check('479-character answer is unchanged and uses original component', () => {
  const proof = fixture.matched.answer;
  assert.equal(proof.characters, 479);
  assert.equal(proof.type, 'text');
  assert.equal(sha256(proof.content), fixture.expected.answer);
  const message = { messageType: proof.type, content: proof.content, status: 'MessageSuccess' };
  const before = originalCalls.length;
  const vnode = wrapper.render.call({ message, isHistoryMessage: false }, h);
  assert.equal(vnode.tag, 'original-answer');
  assert.equal(originalCalls.length, before + 1);
  assert.equal(originalCalls.at(-1).message, message);
  assert.equal(message.content, proof.content);
});

check('reasoning bypasses original answer render and history stays original', () => {
  const before = originalCalls.length;
  const message = { messageType: 'reasoningText', content: '仅供展示', status: 'MessageReceiving' };
  const vnode = wrapper.render.call({ message, isHistoryMessage: false }, h);
  assert.equal(vnode.tag.name, 'CustomReasoningCard');
  assert.equal(originalCalls.length, before);
  const history = wrapper.render.call({ message, isHistoryMessage: true }, h);
  assert.equal(history.tag, 'original-answer');
  assert.equal(originalCalls.length, before + 1);
  assert.equal(message.content, '仅供展示');
});

check('UI log contains metadata only, never reasoning, answer, or API key', () => {
  const joined = messages.join('\n');
  assert.ok(!joined.includes(fixture.matched.reasoning.content));
  assert.ok(!joined.includes(fixture.matched.answer.content));
  assert.ok(!joined.includes('sk-'));
});


check('search status packets are separated from reasoning across all split points', () => {
  const control = '\x1eMYAI4:' + JSON.stringify({state:'searching',count:0}) + '\x1f';
  const complete = '\x1eMYAI4:' + JSON.stringify({state:'complete',count:3}) + '\x1f';
  const text = '原始思考';
  const state = reasoningInstance({messageType:'reasoningText',content:text,status:'MessageReceiving'});
  for (let n=0;n<=control.length;n++) {
    updateContent(state, text+control.slice(0,n));
    assert.equal(state.instance.reasoningText,text);
    const view=JSON.stringify(state.component.render.call(state.instance,h));
    assert.ok(!view.includes('MYAI4'));
  }
  assert.equal(state.instance.searchStatus,'正在搜索网络…');
  updateContent(state,text+control+complete+'继续思考');
  assert.equal(state.instance.reasoningText,text+'继续思考');
  assert.equal(state.instance.searchStatus,'已获取 3 个来源');
  updateStatus(state,'MessageSuccess');renderState(state,'updated');
  assert.equal(state.instance.finalHash,sha256(text+'继续思考'));
  assert.ok(!events.some(x=>/tts|play/i.test(x.name)));
});
check('search-only visible state preserves stop acknowledgement and no packet leaks', () => {
  const content='\x1eMYAI4:{"state":"searching","count":0}\x1f';
  const state=reasoningInstance({messageType:'reasoningText',content,status:'MessageReceiving'});
  assert.equal(state.instance.reasoningText,'');
  assert.equal(state.instance.reasoningVisible,true);
  renderState(state,'mounted');
  falcon.trigger('setStopReceiveMessageButtonVisibleFalse');
  assert.equal(events.filter(x=>x.name==='appendingAnimationFinished').length,1);
  updateContent(state,content+'\x1eMYAI4:{"state":"failed"}\x1f');
  assert.equal(state.instance.searchStatus,'搜索失败，未取得最新资料');
  assert.equal(state.instance.reasoningStatus,'stopped');
  updateContent(state,content+'\x1eMYAI4:{invalid}\x1f');
  assert.equal(state.instance.reasoningText,'');
});


check('tool-round native message replacement keeps one visible cumulative card and manual fold', () => {
  const id='A_0123456789ABCDEF0123456789ABCDEF';
  const init='\x1eMYAI4:'+JSON.stringify({state:'init',count:0,request_id:id})+'\x1f';
  const first=reasoningInstance({messageType:'reasoningText',content:init+'第一轮',status:'MessageReceiving'});
  renderState(first,'mounted');first.instance.toggleReasoning();
  updateStatus(first,'MessageSuccess');
  const second=reasoningInstance({messageType:'reasoningText',content:init+'第一轮第二轮',status:'MessageReceiving'});
  renderState(second,'mounted');
  assert.equal(first.instance.superseded,true);
  assert.equal(first.component.render.call(first.instance,h).children.length,0);
  assert.equal(second.instance.reasoningExpanded,false);
  assert.equal(second.instance.reasoningText,'第一轮第二轮');
  updateContent(first,init+'第一轮');
  assert.equal(second.instance.superseded,false);
  falcon.trigger('setStopReceiveMessageButtonVisibleFalse');
  assert.equal(events.filter(x=>x.name==='appendingAnimationFinished').length,1);
  first.component.beforeDestroy.call(first.instance);
  second.component.beforeDestroy.call(second.instance);
});

const report = {
  ok: results.every(x => x.ok),
  engine: process.version,
  fixture: 'synthetic inline text',
  fixture_reasoning: { characters: fixture.matched.reasoning.characters, sha256: fixture.matched.reasoning.sha256 },
  fixture_answer: { characters: fixture.matched.answer.characters, sha256: fixture.matched.answer.sha256 },
  checks: results
};
// Report is printed only; no private fixture files.
for (const result of results) process.stdout.write((result.ok ? 'PASS ' : 'FAIL ') + result.name + (result.error ? ': ' + result.error : '') + '\n');
if (!report.ok) process.exitCode = 1;
