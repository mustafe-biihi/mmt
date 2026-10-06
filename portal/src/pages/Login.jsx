import { useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { api, session } from '../api.js'

export default function Login() {
  const [msisdn, setMsisdn] = useState('')
  const [pin, setPin] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const navigate = useNavigate()

  if (session.get()) return <Navigate to="/dashboard" replace />

  async function submit(e) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const { token } = await api.login(msisdn, pin)
      session.set(token)
      navigate('/dashboard')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
      setPin('')
    }
  }

  return (
    <section className="card narrow">
      <h1>Log in</h1>
      <form onSubmit={submit} className="form">
        <label>
          Mobile number
          <input value={msisdn} onChange={(e) => setMsisdn(e.target.value)} required inputMode="tel" />
        </label>
        <label>
          PIN
          <input type="password" value={pin} onChange={(e) => setPin(e.target.value)} required maxLength={4} inputMode="numeric" />
        </label>
        {error && <p className="error">{error}</p>}
        <button disabled={busy}>{busy ? 'Checking…' : 'Log in'}</button>
      </form>
    </section>
  )
}
