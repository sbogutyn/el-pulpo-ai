// Wireframe 1: Three-pane dashboard — queue | agents | logs
const Wireframe1 = ({ showLogs = true }) => (
  <div className="wf flex col">
    <FakeNav active="Queue" />
    <div className="flex grow" style={{ minHeight: 0 }}>
      {/* Queue */}
      <div className="flex col p-3 gap-3" style={{ width: showLogs ? '38%' : '55%', borderRight: '1.5px solid var(--line)' }}>
        <div className="flex between items-center">
          <div className="wf-h2">Queue <span className="sk-pill sk-pill-accent">7 open</span></div>
          <div className="flex gap-2">
            <span className="sk-pill">All</span>
            <span className="sk-pill">High</span>
            <span className="sk-pill">Mine</span>
          </div>
        </div>
        <div className="flex col gap-2 grow scroll-y" style={{ paddingRight: 4 }}>
          {TASKS.map(t => (
            <div key={t.id} className="sk-box p-3 flex col gap-2">
              <div className="flex between items-center">
                <div className="flex items-center gap-2">
                  <Prio level={t.prio} />
                  <span className="wf-mono" style={{ color: 'var(--ink-faint)' }}>{t.id}</span>
                </div>
                <span className="wf-small">{t.eta}</span>
              </div>
              <div className="wf-body">{t.title}</div>
              <div className="flex between items-center">
                <div className="flex gap-2">
                  {t.tags.map(tag => <span key={tag} className="sk-pill">#{tag}</span>)}
                </div>
                <button className="sk-btn sk-btn-primary">claim →</button>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Agents */}
      <div className="flex col p-3 gap-3 grow" style={{ borderRight: showLogs ? '1.5px solid var(--line)' : 'none' }}>
        <div className="flex between items-center">
          <div className="wf-h2">Agents <span className="wf-small">· 4 of 6 active</span></div>
          <span className="sk-pill">+ spawn</span>
        </div>
        <div className="flex col gap-2 grow scroll-y">
          {AGENTS.map(a => (
            <div key={a.id} className="sk-box p-3 flex col gap-2">
              <div className="flex between items-center">
                <div className="flex items-center gap-2">
                  <Avatar initials={a.initials} />
                  <div className="flex col">
                    <div className="wf-body" style={{ fontWeight: 700 }}>{a.name}</div>
                    <div className="wf-small flex items-center gap-2">
                      <Dot state={a.state} /> {a.state}
                    </div>
                  </div>
                </div>
                <span className="wf-mono" style={{ color: 'var(--ink-faint)' }}>{a.id}</span>
              </div>
              <div className="wf-small">→ {a.task}</div>
              {a.state === 'busy' && (
                <div className="sk-bar accent"><i style={{ width: `${a.progress * 100}%` }}></i></div>
              )}
            </div>
          ))}
        </div>
      </div>

      {/* Logs */}
      {showLogs && (
        <div className="flex col p-3 gap-2" style={{ width: 360, background: 'var(--paper-soft)' }}>
          <div className="flex between items-center">
            <div className="wf-h2">Live logs</div>
            <div className="flex gap-2">
              <span className="sk-dot sk-dot-on"></span>
              <span className="wf-small">streaming</span>
            </div>
          </div>
          <div className="flex gap-2">
            <span className="sk-pill sk-pill-filled">all</span>
            <span className="sk-pill">errors</span>
            <span className="sk-pill">warns</span>
          </div>
          <div className="sk-box-soft p-2 grow scroll-y" style={{ background: 'var(--paper)' }}>
            {LOGS.map((l, i) => <LogLine key={i} {...l} />)}
          </div>
        </div>
      )}
    </div>
  </div>
);
window.Wireframe1 = Wireframe1;
