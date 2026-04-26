// Shared mock data + small primitives for wireframes
const TASKS = [
  { id: 'T-2041', title: 'Classify support tickets — batch 412', prio: 'high', tags: ['nlp', 'batch'], eta: '~4m' },
  { id: 'T-2040', title: 'Summarize call transcripts (47 files)', prio: 'med', tags: ['summarize'], eta: '~12m' },
  { id: 'T-2039', title: 'Extract invoice line items', prio: 'high', tags: ['ocr', 'finance'], eta: '~2m' },
  { id: 'T-2038', title: 'Embed product catalog delta', prio: 'low', tags: ['embed'], eta: '~30m' },
  { id: 'T-2037', title: 'Translate FR→EN doc set', prio: 'med', tags: ['translate'], eta: '~8m' },
  { id: 'T-2036', title: 'Image moderation sweep', prio: 'low', tags: ['vision'], eta: '~6m' },
  { id: 'T-2035', title: 'Generate weekly report draft', prio: 'med', tags: ['writeup'], eta: '~5m' },
];

const AGENTS = [
  { id: 'A1', name: 'orca-01', initials: 'OR', state: 'busy', task: 'T-2032 · classify tix', progress: 0.62 },
  { id: 'A2', name: 'finch-02', initials: 'FN', state: 'busy', task: 'T-2034 · summarize', progress: 0.18 },
  { id: 'A3', name: 'mole-03', initials: 'ML', state: 'idle', task: '— waiting —', progress: 0 },
  { id: 'A4', name: 'newt-04', initials: 'NW', state: 'busy', task: 'T-2033 · embed', progress: 0.91 },
  { id: 'A5', name: 'pika-05', initials: 'PK', state: 'idle', task: '— waiting —', progress: 0 },
  { id: 'A6', name: 'crow-06', initials: 'CR', state: 'offline', task: '(offline)', progress: 0 },
];

const LOGS = [
  { ts: '14:02:11', src: 'orca-01', lvl: 'ok', msg: 'task T-2032 step 4/6 complete' },
  { ts: '14:02:09', src: 'finch-02', lvl: '', msg: 'fetched transcript chunk 3 (412kb)' },
  { ts: '14:02:07', src: 'newt-04', lvl: 'ok', msg: 'embedded 1,204 items in 3.1s' },
  { ts: '14:02:04', src: 'orca-01', lvl: 'warn', msg: 'rate limit nearing — backing off 200ms' },
  { ts: '14:02:00', src: 'finch-02', lvl: '', msg: 'calling summarize.long_form' },
  { ts: '14:01:55', src: 'newt-04', lvl: '', msg: 'batch 12/14 dispatched' },
  { ts: '14:01:51', src: 'mole-03', lvl: '', msg: 'idle — polling queue' },
  { ts: '14:01:47', src: 'orca-01', lvl: 'err', msg: 'parse fail on row 88 — retrying' },
  { ts: '14:01:44', src: 'finch-02', lvl: 'ok', msg: 'task T-2031 finished (8.4s)' },
  { ts: '14:01:40', src: 'orca-01', lvl: '', msg: 'token usage 14,201 / 120k' },
  { ts: '14:01:36', src: 'newt-04', lvl: '', msg: 'opened connection to vector db' },
  { ts: '14:01:31', src: 'finch-02', lvl: 'ok', msg: 'task T-2030 finished (11.2s)' },
];

const Prio = ({ level }) => (
  <span className={`sk-prio ${level}`} title={`priority: ${level}`}>
    <span></span><span></span><span></span>
  </span>
);

const Dot = ({ state }) => {
  const cls = state === 'busy' ? 'sk-dot-busy' : state === 'idle' ? 'sk-dot-on' : 'sk-dot-idle';
  return <span className={`sk-dot ${cls}`}></span>;
};

const Avatar = ({ initials }) => <span className="sk-avatar">{initials}</span>;

const FakeNav = ({ tabs = ['Queue', 'Agents', 'Logs', 'Settings'], active = 'Queue' }) => (
  <div className="fake-nav">
    <span className="crumb">⌗ task queue</span>
    <span style={{ flex: 1 }}></span>
    {tabs.map(t => (
      <span key={t} className={`tab ${t === active ? 'on' : ''}`}>{t}</span>
    ))}
    <span className="sk-avatar" style={{ marginLeft: 8 }}>me</span>
  </div>
);

const LogLine = ({ ts, src, lvl, msg }) => (
  <div className="log-line">
    <span className="ts">{ts}</span>
    <span className="src">{src}</span>
    <span className={lvl}>{msg}</span>
  </div>
);

Object.assign(window, { TASKS, AGENTS, LOGS, Prio, Dot, Avatar, FakeNav, LogLine });
