import OriginalRobotMessage from './RobotMessage-original-e079798d.js';
import VerticalScroller from './VerticalScroller-8d348f67.js';

console.warn('[MYAI_UI] patch_loaded version=custom-websearch-r4-1');

// The firmware exposes this message type but its Melon renderer selects no
// component for live messages. Keep the original component for every other
// message, including the final answer and history.
const REASONING_TYPE = 'reasoningText';
const SUCCESS_STATUS = 'MessageSuccess';
const STOP_EVENT = 'setStopReceiveMessageButtonVisibleFalse';
const liveCards = Object.create(null);

// The native layer accumulates this channel. Remove transport controls before
// presenting reasoning, hashing it, or exposing any user-visible text.
function decodeReasoning(input) {
  const prefix = '\x1eMYAI4:';
  let text = '', status = '', requestId = '', pos = 0;
  while (pos < input.length) {
    const start = input.indexOf('\x1e', pos);
    if (start < 0) { text += input.slice(pos); break; }
    text += input.slice(pos, start);
    const tail = input.slice(start);
    if (prefix.indexOf(tail) === 0) break;
    if (tail.indexOf(prefix) !== 0) { pos = start + 1; continue; }
    const end = input.indexOf('\x1f', start + prefix.length);
    if (end < 0) break;
    try {
      const control = JSON.parse(input.slice(start + prefix.length, end));
      if (typeof control.request_id === 'string' && /^A_[A-F0-9]{32}$/.test(control.request_id)) requestId = control.request_id;
      if (control.state === 'searching') status = '正在搜索网络…';
      else if (control.state === 'complete' && Number.isInteger(control.count) && control.count >= 0 && control.count <= 8) status = '已获取 ' + control.count + ' 个来源';
      else if (control.state === 'failed') status = '搜索失败，未取得最新资料';
    } catch (_) { /* Never leak an invalid or incomplete control packet. */ }
    pos = end + 1;
  }
  return { text, status, requestId };
}

function utf8Bytes(input) {
  const bytes = [];
  for (let i = 0; i < input.length; i++) {
    let cp = input.charCodeAt(i);
    if (cp >= 0xd800 && cp <= 0xdbff && i + 1 < input.length) {
      const low = input.charCodeAt(i + 1);
      if (low >= 0xdc00 && low <= 0xdfff) {
        cp = 0x10000 + ((cp - 0xd800) << 10) + (low - 0xdc00);
        i++;
      }
    }
    if (cp < 0x80) bytes.push(cp);
    else if (cp < 0x800) bytes.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
    else if (cp < 0x10000) bytes.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    else bytes.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
  }
  return bytes;
}

function sha256(input) {
  const bytes = utf8Bytes(input);
  const bitLength = bytes.length * 8;
  bytes.push(0x80);
  while (bytes.length % 64 !== 56) bytes.push(0);
  for (let i = 7; i >= 0; i--) bytes.push(Math.floor(bitLength / Math.pow(2, i * 8)) & 0xff);
  const k = [
    0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
    0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
    0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
    0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
    0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
    0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
    0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
    0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
  ];
  const h = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
  const w = new Array(64);
  for (let offset = 0; offset < bytes.length; offset += 64) {
    for (let i = 0; i < 16; i++) {
      const p = offset + i * 4;
      w[i] = ((bytes[p] << 24) | (bytes[p + 1] << 16) | (bytes[p + 2] << 8) | bytes[p + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15], b = w[i - 2];
      const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3);
      const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let a=h[0],b=h[1],c=h[2],d=h[3],e=h[4],f=h[5],g=h[6],j=h[7];
    for (let i = 0; i < 64; i++) {
      const s1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      const choice = (e & f) ^ (~e & g);
      const t1 = (j + s1 + choice + k[i] + w[i]) >>> 0;
      const s0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (s0 + majority) >>> 0;
      j=g; g=f; f=e; e=(d+t1)>>>0; d=c; c=b; b=a; a=(t1+t2)>>>0;
    }
    h[0]=(h[0]+a)>>>0; h[1]=(h[1]+b)>>>0; h[2]=(h[2]+c)>>>0; h[3]=(h[3]+d)>>>0;
    h[4]=(h[4]+e)>>>0; h[5]=(h[5]+f)>>>0; h[6]=(h[6]+g)>>>0; h[7]=(h[7]+j)>>>0;
  }
  return h.map(x => ('00000000' + x.toString(16)).slice(-8)).join('');
}

const CustomReasoningCard = {
  name: 'CustomReasoningCard',
  props: {
    message: { type: Object, required: true },
    currentChatId: { type: String, default: '' },
    isLastMessage: { type: Boolean, default: false }
  },
  data() {
    const status = this.message && this.message.status || '';
    const content = this.message && typeof this.message.content === 'string' ? this.message.content : '';
    const decoded = decodeReasoning(content);
    return {
      reasoningText: decoded.text,
      searchStatus: decoded.status,
      requestId: decoded.requestId,
      superseded: false,
      expansionTouched: false,
      reasoningVisible: decoded.text.length > 0 || decoded.status.length > 0,
      reasoningExpanded: status !== SUCCESS_STATUS,
      reasoningStatus: status === SUCCESS_STATUS ? 'done' : decoded.text.length || decoded.status.length ? 'thinking' : 'idle',
      lastStateSignature: '',
      lastProgressLogAt: 0,
      lastHashedText: null,
      finalHash: '',
      followTail: true,
      lastScrollY: 0,
      stopAcknowledged: false
    };
  },
  watch: {
    'message.content'(value) {
      const decoded = decodeReasoning(typeof value === 'string' ? value : '');
      this.reasoningText = decoded.text;
      this.searchStatus = decoded.status;
      this.requestId = decoded.requestId;
      this.registerLiveCard();
      this.reasoningVisible = this.reasoningText.length > 0 || this.searchStatus.length > 0;
      if (!this.stopAcknowledged) this.reasoningStatus = this.message.status === SUCCESS_STATUS ? 'done' : this.reasoningVisible ? 'thinking' : 'idle';
      if (this.reasoningVisible && this.reasoningExpanded && this.followTail) this.scrollReasoningToEnd();
    },
    'message.status'(value) {
      if (this.stopAcknowledged) return;
      this.reasoningStatus = value === SUCCESS_STATUS ? 'done' : this.reasoningVisible ? 'thinking' : 'idle';
      if (value === SUCCESS_STATUS) this.reasoningExpanded = false;
    }
  },
  mounted() {
    this.registerLiveCard();
    $falcon.on(STOP_EVENT, this.onStopReceiveSignal);
    this.reportRenderedState('mounted');
  },
  beforeDestroy() {
    if (this.requestId && liveCards[this.requestId] === this) delete liveCards[this.requestId];
    $falcon.off(STOP_EVENT, this.onStopReceiveSignal);
  },
  updated() {
    this.reportRenderedState('updated');
  },
  methods: {
    registerLiveCard() {
      if (!this.requestId || this.superseded) return;
      const previous = liveCards[this.requestId];
      if (previous && previous !== this) {
        previous.superseded = true;
        if (previous.expansionTouched) {
          this.reasoningExpanded = previous.reasoningExpanded;
          this.expansionTouched = true;
        }
        this.followTail = previous.followTail;
      }
      liveCards[this.requestId] = this;
    },
    onStopReceiveSignal() {
      // The stock ReasoningMessage acknowledges this existing stop signal so
      // TalkingView can finish its animation and restore the input controls.
      // Only the active live card may acknowledge; normal completion does not.
      if (this.superseded || this.stopAcknowledged || !this.isLastMessage || !this.reasoningVisible ||
          this.reasoningStatus !== 'thinking') return;
      this.stopAcknowledged = true;
      this.reasoningStatus = 'stopped';
      this.reasoningExpanded = false;
      $falcon.off(STOP_EVENT, this.onStopReceiveSignal);
      $falcon.trigger('appendingAnimationFinished', { currentChatId: this.currentChatId });
    },
    toggleReasoning() {
      this.expansionTouched = true;
      this.reasoningExpanded = !this.reasoningExpanded;
      if (this.reasoningExpanded) {
        this.followTail = true;
        this.scrollReasoningToEnd();
      }
    },
    onReasoningScroll(event) {
      const offset = event && event.contentOffset;
      const size = event && event.contentSize;
      if (!offset || !size || typeof offset.y !== 'number' || typeof size.height !== 'number') return;
      const y = offset.y;
      // The app's TalkingView uses contentOffset.y + viewport height against
      // contentSize.height. A move upward pauses follow; reaching bottom resumes.
      if (y < this.lastScrollY - 3) this.followTail = false;
      if (y + 110 >= size.height - 12) this.followTail = true;
      this.lastScrollY = y;
    },
    scrollReasoningToEnd() {
      if (!this.$nextTick) return;
      this.$nextTick(() => {
        if (this.followTail && this.reasoningExpanded &&
            this.$refs && this.$refs.reasoningBottom &&
            this.$page && this.$page.$dom && this.$page.$dom.scrollToElement) {
          this.$page.$dom.scrollToElement(this.$refs.reasoningBottom);
        }
      });
    },
    reportRenderedState(phase) {
      if (this.superseded) return;
      // This runs after the component was rendered and mounted/updated. Only
      // metadata is logged, never the question, reasoning text, or API key.
      const text = this.reasoningText || '';
      const done = this.reasoningStatus === 'done';
      const now = Date.now();
      if (!done && phase !== 'mounted' && now - this.lastProgressLogAt < 500) return;
      if (done && this.lastHashedText !== text) {
        this.finalHash = sha256(text);
        this.lastHashedText = text;
      }
      const signature = text.length + ':' + this.reasoningStatus + ':' + this.reasoningExpanded + ':' + this.reasoningVisible + ':' + this.searchStatus;
      if (signature === this.lastStateSignature && phase !== 'mounted') return;
      this.lastStateSignature = signature;
      if (!done) this.lastProgressLogAt = now;
      const chars = Array.from(text).length;
      console.warn('[LITTLEP-CUSTOM-REASONING] phase=' + phase +
        ' chars=' + chars + (done ? ' sha256=' + this.finalHash : '') +
        ' status=' + this.reasoningStatus + ' expanded=' + this.reasoningExpanded +
        ' visible=' + this.reasoningVisible + ' search=' + this.searchStatus);
    }
  },
  render(h) {
    if (!this.reasoningVisible || this.superseded) return h('div');
    const done = this.reasoningStatus === 'done';
    const stopped = this.reasoningStatus === 'stopped';
    const header = h('div', {
      style: { height: '36px', flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
      on: { click: this.toggleReasoning }
    }, [
      h('text', { style: { color: '#f1f4f8', fontSize: '24px', fontWeight: 'bold' } }, ['思考过程']),
      h('text', { style: { color: '#aeb8c8', fontSize: '20px' } }, [
        (done ? '思考完成' : stopped ? '已停止' : '思考中') + ' ' + (this.reasoningExpanded ? '收起' : '展开')
      ])
    ]);
    const children = [header];
    if (this.searchStatus) children.push(h('text', {
      style: { color: '#aeb8c8', fontSize: '20px', lineHeight: '26px' }
    }, [this.searchStatus]));
    if (this.reasoningExpanded) {
      children.push(h(VerticalScroller, {
        style: { height: '110px', maxHeight: '110px', width: '100%' },
        on: { scroll: this.onReasoningScroll }
      }, [h('text', {
        style: { color: '#dce5ef', fontSize: '22px', lineHeight: '28px', whiteSpace: 'pre-wrap' }
      }, [this.reasoningText]),
      h('div', { ref: 'reasoningBottom', style: { height: '1px' } })]));
    }
    return h('div', {
      style: { width: '100%', padding: '8px 12px', margin: '6px 0px', borderRadius: '18px', backgroundColor: '#202633' }
    }, children);
  }
};

const OriginalRender = OriginalRobotMessage.render;
const WrappedRobotMessage = Object.assign({}, OriginalRobotMessage, {
  render(h) {
    const message = this.message;
    if (message && message.messageType === REASONING_TYPE && !this.isHistoryMessage) {
      return h(CustomReasoningCard, { props: { message, currentChatId: this.currentChatId,
        isLastMessage: this.isLastMessage } });
    }
    return OriginalRender.call(this, h);
  }
});

export default WrappedRobotMessage;
