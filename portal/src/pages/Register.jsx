import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api.js'

const empty = { fullName: '', msisdn: '', nationalId: '', pin: '', confirmPin: '' }

export default function Register() {
  const [form, setForm] = useState(empty)
  const [error, setError] = useState('')
  const [done, setDone] = useState(null)
  const [busy, setBusy] = useState(false)

  const set = (field) => (e) => setForm({ ...form, [field]: e.target.value })

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!/^\d{4}$/.test(form.pin)) return setError('PIN must be exactly 4 digits.')
    if (form.pin !== form.confirmPin) return setError('PINs do not match.')
    setBusy(true)
    try {
      const { confirmPin, ...input } = form
      setDone(await api.register(input))
      setForm(empty)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  if (done) {
    return (
      <section className="card">
        <h1>Registration successful</h1>
        <p>
          Welcome, <strong>{done.customer.fullName}</strong>. Your wallet <code>{done.wallet.accountNo}</code> is
          active.
        </p>
        <p>
          Dial <strong>*836#</strong> from <code>{done.customer.msisdn}</code> and enter your PIN to send money, cash
          out, buy airtime and pay merchants.
        </p>
        <div className="row">
          <Link className="button" to="/simulator">Try *836# now</Link>
          <Link className="button secondary" to="/login">Log in to portal</Link>
        </div>
      </section>
    )
  }

  return (
    <section className="card">
      <h1>Open an MMT wallet</h1>
      <form onSubmit={submit} className="form">
        <label>
          Full name
          <input value={form.fullName} onChange={set('fullName')} required minLength={3} maxLength={120} />
        </label>
        <label>
          Mobile number
          <input value={form.msisdn} onChange={set('msisdn')} required placeholder="e.g. 0612345678" inputMode="tel" />
        </label>
        <label>
          National ID
          <input value={form.nationalId} onChange={set('nationalId')} required minLength={4} maxLength={40} />
        </label>
        <div className="row">
          <label>
            4-digit PIN
            <input type="password" value={form.pin} onChange={set('pin')} required maxLength={4} inputMode="numeric" />
          </label>
          <label>
            Confirm PIN
            <input type="password" value={form.confirmPin} onChange={set('confirmPin')} required maxLength={4} inputMode="numeric" />
          </label>
        </div>
        {error && <p className="error">{error}</p>}
        <button disabled={busy}>{busy ? 'Registering…' : 'Register'}</button>
      </form>
    </section>
  )
}
