// Wireframe 5: Kanban board — Queue → In Progress → Done, with agent dock
const Wireframe5 = ({ showLogs = true }) => {
  const inProgress = AGENTS.filter(a => a.state === 'busy').map((a, i) => ({
    id: ['T-2032', 'T-2034', 'T-2033'][i] || 'T-20' + (30 - i),
    title: ['Classify support tix — pt 3', 'Summarize call transcripts', 'Embed product catalog'][i] || 'In flight',
    agent: a,
    prio: ['high', 'med', 'low'][i] || 'med',
  }));
  const done = [
    { id: 'T-2031', title: 'OCR receipts batch', agent: AGENTS[1], prio: 'med', when: '2m ago' },
    { id: 'T-2030', title: 'Translate FAQ → ES', agent: AGENTS[1], prio: 'low', when: '4m ago' },
    { id: 'T-2029', title: 'Sentiment scoring run', agent: AGENTS[0], prio: 'high', when: '7m ago' },
  ];

  const Col = ({ title, count, children, accent }) => (
    <div className="flex col grow p-3 gap-2" style={{ minWidth: 0, borderRight: '1.5px solid var(--line)' }}>
      <div className="flex between items-center">
        <div className="flex items-center gap-2">
          <div className="wf-h2">{title}</div>
          <span className={`sk-pill ${accent ? 'sk-pill-accent' : ''}`}>{count}</span>
        </div>
        <span className="sk-pill">⋯</span>
      </div>
      <div className="sk-squiggle"></div>
      <div className="flex col gap-2 grow scroll-y" style={{ paddingRight: 4 }}>
        {children}
      </div>
    </div>
  );

  return (
    <div className="wf flex col">
      <FakeNav active="Queue" />
      <div className="flex grow" style={{ minHeight: 0 }}>
        <Col title="Queue" count={TASKS.length} accent>
          {TASKS.map(t => (
            <div key={t.id} className="sk-box p-2 flex col gap-2">
              <div className="flex between items-center">
                <div className="flex items-center gap-2">
                  <Prio level={t.prio} />
                  <span className="wf-mono" style={{ color: 'var(--ink-faint)' }}>{t.id}</span>
                </div>
                <span className="wf-small">{t.eta}</span>
              </div>
              <div className="wf-body" style={{ fontSize: 14 }}>{t.title}</div>
              <button className="sk-btn sk-btn-primary" style={{ alignSelf: 'flex-start' }}>claim →</button>
            </div>
          ))}
        </Col>

        <Col title="In progress" count={inProgress.length}>
          {inProgress.map(t => (
            <div key={t.id} className="sk-box p-2 flex col gap-2">
              <div className="flex between items-center">
                <div className="flex items-center gap-2">
                  <Prio level={t.prio} />
                  <span className="wf-mono" style={{ color: 'var(--ink-faint)' }}>{t.id}</span>
                </div>
                <span className="wf-small">{Math.round(t.agent.progress * 100)}%</span>
              </div>
              <div className="wf-body" style={{ fontSize: 14 }}>{t.title}</div>
              <div className="sk-bar accent"><i style={{ width: `${t.agent.progress * 100}%` }}></i></div>
              <div className="flex items-center gap-2">
                <Avatar initials={t.agent.initials} />
                <span className="wf-small">{t.agent.name}</span>
              </div>
            </div>
          ))}
        </Col>

        <Col title="Done" count={`${done.length} today`}>
          {done.map(t => (
            <div key={t.id} className="sk-box p-2 flex col gap-1" style={{ opacity: 0.75 }}>
              <div className="flex between items-center">
                <div className="flex items-center gap-2">
                  <span className="wf-mono" style={{ color: 'var(--ink-faint)', textDecoration: 'line-through' }}>{t.id}</span>
                </div>
                <span className="wf-small">{t.when}</span>
              </div>
              <div className="wf-body" style={{ fontSize: 14 }}>✓ {t.title}</div>
              <div className="flex items-center gap-2">
                <Avatar initials={t.agent.initials} />
                <span className="wf-small">{t.agent.name}</span>
              </div>
            </div>
          ))}
        </Col>

        {/* Right dock: agents + logs */}
        <div className="flex col" style={{ width: 320, background: 'var(--paper-soft)' }}>
          <div className="p-3 flex col gap-2" style={{ borderBottom: '1.5px solid var(--line)' }}>
            <div className="wf-h3">Agents</div>
            <div className="flex col gap-2">
              {AGENTS.slice(0, 4).map(a => (
                <div key={a.id} className="flex items-center gap-2">
                  <Avatar initials={a.initials} />
                  <div className="flex col grow" style={{ minWidth: 0 }}>
                    <div className="wf-body" style={{ fontSize: 13, fontWeight: 700 }}>{a.name}</div>
                    <div className="wf-small flex items-center gap-2"><Dot state={a.state} />{a.state}</div>
                  </div>
                </div>
              ))}
              <span className="wf-small" style={{ fontStyle: 'italic' }}>+ 2 more</span>
            </div>
          </div>
          {showLogs && (
            <div className="flex col p-3 gap-2 grow" style={{ minHeight: 0 }}>
              <div className="flex between items-center">
                <div className="wf-h3">Logs</div>
                <span className="sk-dot sk-dot-on"></span>
              </div>
              <div className="grow scroll-y flex col">
                {LOGS.map((l, i) => <LogLine key={i} {...l} />)}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
window.Wireframe5 = Wireframe5;
