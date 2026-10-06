import { useEffect, useRef, useState } from 'react'
import { ussd } from '../api.js'

const SERVICE_CODE = '*836#'
const SESSION_SECONDS = 30
const KEYS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#']

// A feature-phone emulator: dial *836#, then answer each USSD screen.
export default function Simulator() {
  const [msisdn, setMsisdn] = useState(() => localStorage.getItem('mmt.sim.msisdn') || '252610000001')
  const [dial, setDial] = useState(SERVICE_CODE)
  const [phase, setPhase] = useState('idle') // idle | active | ended
  const [screen, setScreen] = useState('')
  const [reply, setReply] = useState('')
  const [busy, setBusy] = useState(false)
  const [secondsLeft, setSecondsLeft] = useState(SESSION_SECONDS)
  const sessionRef = useRef({ id: '', inputs: [] })
  const replyRef = useRef(null)

  // Mirror the server's 30s idle timeout: the countdown restarts after every screen.
  useEffect(() => {
    if (phase !== 'active' || busy) return
    if (secondsLeft <= 0) {
      setPhase('ended')
      setScreen('Session timed out.')
      return
    }
    const t = setTimeout(() => setSecondsLeft((s) => s - 1), 1000)
    return () => clearTimeout(t)
  }, [phase, busy, secondsLeft])

  useEffect(() => {
    if (phase === 'active') replyRef.current?.focus()
  }, [phase, screen])

  async function hop(inputs) {
    setBusy(true)
    try {
      const res = await ussd({
        sessionId: sessionRef.current.id,
        phoneNumber: msisdn,
        serviceCode: SERVICE_CODE,
        text: inputs.join('*'),
      })
      sessionRef.current.inputs = inputs
      setScreen(res.message)
      setPhase(res.end ? 'ended' : 'active')
      setSecondsLeft(SESSION_SECONDS)
    } catch {
      setScreen('Connection problem or invalid MMI code.')
      setPhase('ended')
    } finally {
      setBusy(false)
      setReply('')
    }
  }

  function call(e) {
    e?.preventDefault()
    if (dial.trim() !== SERVICE_CODE) {
      setScreen('Invalid MMI code.')
      setPhase('ended')
      return
    }
    localStorage.setItem('mmt.sim.msisdn', msisdn)
    sessionRef.current = { id: crypto.randomUUID(), inputs: [] }
    hop([])
  }

  function send(e) {
    e.preventDefault()
    if (busy) return
    hop([...sessionRef.current.inputs, reply.trim()])
  }

  function hangUp() {
    setPhase('idle')
    setScreen('')
    setReply('')
  }

  function press(key) {
    if (phase === 'active') setReply((r) => r + key)
    else if (phase === 'idle') setDial((d) => d + key)
  }

  return (
    <section className="sim">
      <div className="sim-info card">
        <h1>USSD simulator</h1>
        <p className="muted">
          Pretend to be a customer's handset. Requests go through the API gateway to the MMT USSD engine exactly
          like an aggregator callback. The session expires after {SESSION_SECONDS}s of inactivity.
        </p>
        <label>
          Phone number (MSISDN)
          <input value={msisdn} onChange={(e) => setMsisdn(e.target.value)} disabled={phase === 'active'} />
        </label>
        <p className="muted small">
          Demo accounts (when SEED_DEMO=true): 252610000001 and 252610000002, PIN 1234. Agents: 2001, 2002.
          Merchants: 1001, 1002, 1003.
        </p>
      </div>

      <div className="phone">
        <div className="speaker" />
        <div className="screen">
          {phase === 'idle' ? (
            <form onSubmit={call} className="dialer">
              <input value={dial} onChange={(e) => setDial(e.target.value)} aria-label="Dial" />
              <small>Dial {SERVICE_CODE} and press Call</small>
            </form>
          ) : (
            <>
              <pre className="ussd-text">{busy && !screen ? 'USSD code running…' : screen}</pre>
              {phase === 'active' && (
                <form onSubmit={send} className="reply">
                  <input ref={replyRef} value={reply} onChange={(e) => setReply(e.target.value)} disabled={busy} aria-label="Reply" />
                  <span className={`timer ${secondsLeft <= 10 ? 'warn' : ''}`}>{secondsLeft}s</span>
                </form>
              )}
            </>
          )}
        </div>
        <div className="softkeys">
          {phase === 'idle' && <button className="call" onClick={call} disabled={busy}>Call</button>}
          {phase === 'active' && (
            <>
              <button className="secondary" onClick={hangUp}>Cancel</button>
              <button onClick={send} disabled={busy}>Send</button>
            </>
          )}
          {phase === 'ended' && <button onClick={hangUp}>OK</button>}
        </div>
        <div className="keypad">
          {KEYS.map((k) => (
            <button key={k} type="button" onClick={() => press(k)}>{k}</button>
          ))}
          <button type="button" className="wide" onClick={() => (phase === 'active' ? setReply((r) => r.slice(0, -1)) : setDial((d) => d.slice(0, -1)))}>
            ⌫ Clear
          </button>
        </div>
      </div>
    </section>
  )
}
