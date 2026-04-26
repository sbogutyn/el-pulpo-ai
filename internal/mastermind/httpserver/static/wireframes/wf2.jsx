// Wireframe 2: Queue-first with horizontal agent strip on top
const Wireframe2 = ({ showLogs = true }) => (
  <div className="wf flex col">
    <FakeNav active="Queue" />
    {/* Agent strip */}
    <div className="p-3 flex col gap-2" style={{ borderBottom: '1.5px solid var(--line)', background: 'var(--paper-soft)' }}>
      <div className="flex between items-center">
        <div className="wf-h3">Agents</div>
        <div className="wf-small">4 busy · 2 idle · 1 offline</div>
      </div>
      <div className="flex gap-2 scroll-y" style={{ overflowX: 'auto', paddingBottom: 4 }}>
        {AGENTS.map(a => (
          <div key={a.id} className="sk-box p-2 flex col gap-2" style={{ minWidth: 200, background: 'var(--paper)' }}>
            <div className="flex items-center gap-2">
              <Avatar initials={a.initials} />
              <div className="flex col grow">
                <div className="wf-body" style={{ fontWeight: 700 }}>{a.name}</div>
                <div className="wf-small flex items-center gap-2"><Dot state={a.state} />{a.state}</div>
              </div>
            </div>
            <div className="wf-small" style={{ minHeight: 18 }}>→ {a.task}</div>
            {a.state === 'busy' && (
              <div className="sk-bar accent"><i style={{ width: `${a.progress * 100}%` }}></i></div>
            )}
          </div>
        ))}
      </div>
    </div>

    {/* Main */}
    <div className="flex grow" style={{ minHeight: 0 }}>
      <div className="flex col grow p-3 gap-3">
        <div className="flex between items-center">
          <div className="wf-h1 sk-underline">Queue</div>
          <div className="flex gap-2 items-center">
            <span className="wf-small">sort by</span>
            <span className="sk-pill sk-pill-filled">priority</span>
            <span className="sk-pill">age</span>
            <span className="sk-pill">eta</span>
          </div>
        </div>
        <div className="sk-box grow scroll-y">
          <table style={{ width: '100%', borderCollapse: 'collapse', fontFamily: 'Kalam, cursive' }}>
            <thead>
              <tr style={{ borderBottom: '1.5px solid var(--line)', background: 'var(--paper-soft)' }}>
                {['', 'id', 'task', 'tags', 'eta', ''].map((h, i) => (
                  <th key={i} className="wf-small p-2" style={{ textAlign: 'left', textTransform: 'uppercase', letterSpacing: 1, fontWeight: 400 }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {TASKS.map((t, i) => (
                <tr key={t.id} style={{ borderBottom: '1px dashed var(--line)' }}>
                  <td className="p-2"><Prio level={t.prio} /></td>
                  <td className="p-2 wf-mono" style={{ color: 'var(--ink-faint)' }}>{t.id}</td>
                  <td className="p-2 wf-body">{t.title}</td>
                  <td className="p-2">
                    <div className="flex gap-2">
                      {t.tags.map(tag => <span key={tag} className="sk-pill">#{tag}</span>)}
                    </div>
                  </td>
                  <td className="p-2 wf-small">{t.eta}</td>
                  <td className="p-2"><button className="sk-btn sk-btn-primary">claim</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {showLogs && (
        <div className="flex col p-3 gap-2" style={{ width: 340, borderLeft: '1.5px solid var(--line)' }}>
          <div className="wf-h2">Activity</div>
          <div className="grow scroll-y flex col gap-1">
            {LOGS.map((l, i) => <LogLine key={i} {...l} />)}
          </div>
        </div>
      )}
    </div>
  </div>
);
window.Wireframe2 = Wireframe2;
