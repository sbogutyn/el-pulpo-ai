// Wireframe 3: Agent-centric — each agent is a card with inline activity log
const Wireframe3 = ({ showLogs = true }) => {
  const agentLogs = (name) => LOGS.filter(l => l.src === name).slice(0, 4);
  return (
    <div className="wf flex col">
      <FakeNav active="Agents" />
      <div className="flex grow" style={{ minHeight: 0 }}>
        {/* Sidebar: queue */}
        <div className="flex col p-3 gap-2" style={{ width: 280, borderRight: '1.5px solid var(--line)', background: 'var(--paper-soft)' }}>
          <div className="flex between items-center">
            <div className="wf-h2">Queue</div>
            <span className="sk-pill sk-pill-accent">{TASKS.length}</span>
          </div>
          <div className="wf-small">unclaimed tasks · workers self-serve</div>
          <hr className="sk-divider" />
          <div className="flex col gap-2 grow scroll-y">
            {TASKS.map(t => (
              <div key={t.id} className="sk-box p-2 flex col gap-1" style={{ background: 'var(--paper)' }}>
                <div className="flex between items-center">
                  <Prio level={t.prio} />
                  <span className="wf-mono" style={{ color: 'var(--ink-faint)' }}>{t.id}</span>
                </div>
                <div className="wf-body" style={{ fontSize: 13 }}>{t.title}</div>
                <div className="flex between items-center">
                  <span className="wf-small">{t.eta}</span>
                  <span className="sk-pill">drag →</span>
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Agent cards grid */}
        <div className="flex col grow p-3 gap-3 sk-grid-bg">
          <div className="flex between items-center">
            <div className="wf-h1 sk-underline">Agents (6)</div>
            <div className="flex gap-2">
              <span className="sk-pill">grid</span>
              <span className="sk-pill sk-pill-filled">cards</span>
              <span className="sk-pill">list</span>
            </div>
          </div>
          <div className="grow scroll-y" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, alignContent: 'start' }}>
            {AGENTS.map(a => (
              <div key={a.id} className="sk-box p-3 flex col gap-2">
                <div className="flex between items-start">
                  <div className="flex items-center gap-2">
                    <Avatar initials={a.initials} />
                    <div className="flex col">
                      <div className="wf-body" style={{ fontWeight: 700 }}>{a.name}</div>
                      <div className="wf-small flex items-center gap-2"><Dot state={a.state} />{a.state}</div>
                    </div>
                  </div>
                  <button className="sk-btn sk-btn-ghost">⋯</button>
                </div>
                <div className="sk-box-soft p-2">
                  <div className="wf-small" style={{ marginBottom: 4 }}>current</div>
                  <div className="wf-body">{a.task}</div>
                  {a.state === 'busy' && (
                    <div className="sk-bar accent" style={{ marginTop: 6 }}><i style={{ width: `${a.progress * 100}%` }}></i></div>
                  )}
                </div>
                {showLogs && (
                  <div className="flex col" style={{ minHeight: 60 }}>
                    {agentLogs(a.name).length === 0
                      ? <div className="wf-small" style={{ fontStyle: 'italic' }}>no recent activity</div>
                      : agentLogs(a.name).map((l, i) => <LogLine key={i} {...l} src="" />)}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
};
window.Wireframe3 = Wireframe3;
