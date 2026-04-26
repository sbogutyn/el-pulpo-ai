// Wireframe 4: Timeline / log-centric — big stream, queue + agents as side rails
const Wireframe4 = ({ showLogs = true }) => (
  <div className="wf flex col">
    <FakeNav active="Logs" />
    <div className="flex grow" style={{ minHeight: 0 }}>
      {/* Left rail: queue */}
      <div className="flex col p-3 gap-2" style={{ width: 240, borderRight: '1.5px solid var(--line)' }}>
        <div className="wf-h3">Queue · {TASKS.length}</div>
        <div className="flex col gap-2 grow scroll-y">
          {TASKS.map(t => (
            <div key={t.id} className="sk-box p-2 flex items-center gap-2">
              <Prio level={t.prio} />
              <div className="flex col grow" style={{ minWidth: 0 }}>
                <div className="wf-body" style={{ fontSize: 13, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.title}</div>
                <div className="wf-small">{t.id} · {t.eta}</div>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Center: timeline */}
      {showLogs ? (
        <div className="flex col grow p-3 gap-3" style={{ background: 'var(--paper-soft)', minWidth: 0 }}>
          <div className="flex between items-center">
            <div className="wf-h1 sk-underline">Activity stream</div>
            <div className="flex gap-2">
              <span className="sk-pill sk-pill-filled">live</span>
              <span className="sk-pill">last 1h</span>
              <span className="sk-pill">errors only</span>
              <span className="sk-pill">⌕ search</span>
            </div>
          </div>
          <div className="sk-box grow scroll-y p-3" style={{ position: 'relative' }}>
            {/* spine */}
            <div style={{ position: 'absolute', left: 110, top: 12, bottom: 12, width: 1.5, background: 'var(--line)', opacity: 0.4 }}></div>
            <div className="flex col gap-3">
              {LOGS.map((l, i) => (
                <div key={i} className="flex items-start gap-3">
                  <div className="wf-mono" style={{ width: 80, textAlign: 'right', color: 'var(--ink-faint)' }}>{l.ts}</div>
                  <div className="sk-dot" style={{ marginTop: 4, background: l.lvl === 'err' ? 'oklch(0.55 0.18 25)' : l.lvl === 'warn' ? 'var(--accent)' : l.lvl === 'ok' ? 'oklch(0.55 0.13 145)' : 'var(--paper)' }}></div>
                  <div className="flex col grow">
                    <div className="flex items-center gap-2">
                      <Avatar initials={(AGENTS.find(a => a.name === l.src) || {}).initials || '??'} />
                      <span className="wf-body" style={{ fontWeight: 700 }}>{l.src}</span>
                      <span className="wf-small">·</span>
                      <span className="wf-small">{l.lvl || 'info'}</span>
                    </div>
                    <div className="wf-mono" style={{ marginTop: 2, fontSize: 12, color: 'var(--ink)' }}>{l.msg}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      ) : (
        <div className="flex col grow p-3 gap-3 items-center" style={{ justifyContent: 'center', background: 'var(--paper-soft)' }}>
          <div className="wf-h2" style={{ color: 'var(--ink-faint)' }}>logs hidden</div>
          <div className="wf-small">toggle them back on in tweaks</div>
        </div>
      )}

      {/* Right rail: agents */}
      <div className="flex col p-3 gap-2" style={{ width: 240, borderLeft: '1.5px solid var(--line)' }}>
        <div className="wf-h3">Agents</div>
        {AGENTS.map(a => (
          <div key={a.id} className="sk-box p-2 flex col gap-1">
            <div className="flex items-center gap-2">
              <Avatar initials={a.initials} />
              <div className="flex col grow" style={{ minWidth: 0 }}>
                <div className="wf-body" style={{ fontSize: 13, fontWeight: 700 }}>{a.name}</div>
                <div className="wf-small flex items-center gap-2"><Dot state={a.state} />{a.state}</div>
              </div>
            </div>
            {a.state === 'busy' && (
              <>
                <div className="wf-small" style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{a.task}</div>
                <div className="sk-bar accent"><i style={{ width: `${a.progress * 100}%` }}></i></div>
              </>
            )}
          </div>
        ))}
      </div>
    </div>
  </div>
);
window.Wireframe4 = Wireframe4;
