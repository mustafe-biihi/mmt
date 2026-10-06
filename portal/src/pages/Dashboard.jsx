import { useEffect, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { api, formatMoney, session } from '../api.js'

const typeLabels = {
  P2P: 'Send Money',
  CASHOUT: 'Cash Out',
  CASHIN: 'Cash In',
  AIRTIME: 'Airtime',
  MERCHANT: 'Merchant Payment',
}

export default function Dashboard() {
  const [me, setMe] = useState(null)
  const [txns, setTxns] = useState([])
  const [error, setError] = useState('')
  const navigate = useNavigate()

  async function load() {
    try {
      const [profile, history] = await Promise.all([api.me(), api.transactions()])
      setMe(profile)
      setTxns(history.transactions)
    } catch (err) {
      if (err.status === 401) navigate('/login')
      else setError(err.message)
    }
  }

  useEffect(() => {
    if (session.get()) load()
  }, [])

  if (!session.get()) return <Navigate to="/login" replace />

  async function logout() {
    await api.logout().catch(() => {})
    session.clear()
    navigate('/login')
  }

  if (!me) return <section className="card">{error ? <p className="error">{error}</p> : 'Loading…'}</section>

  const { customer, wallet } = me
  return (
    <>
      <section className="card balance">
        <div>
          <p className="muted">{customer.fullName} · {customer.msisdn}</p>
          <h1>{wallet.balanceDisplay}</h1>
          <p className="muted">Wallet {wallet.accountNo} · {wallet.status}</p>
        </div>
        <div className="row">
          <button className="secondary" onClick={load}>Refresh</button>
          <button className="secondary" onClick={logout}>Log out</button>
        </div>
      </section>

      <section className="card">
        <h2>Recent transactions</h2>
        {txns.length === 0 ? (
          <p className="muted">No transactions yet. Dial *836# to get started.</p>
        ) : (
          <table className="stack">
            <thead>
              <tr>
                <th>Date</th>
                <th>Type</th>
                <th>Details</th>
                <th>Ref</th>
                <th className="num">Debit</th>
                <th className="num">Credit</th>
                <th className="num">Balance</th>
              </tr>
            </thead>
            <tbody>
              {txns.map((t) => (
                <tr key={t.reference}>
                  <td data-label="Date">{new Date(t.createdAt).toLocaleString()}</td>
                  <td data-label="Type">{typeLabels[t.type] || t.type}</td>
                  <td data-label="Details" className="wrap">{t.direction === 'DEBIT' ? t.description : `From ${t.counterparty}`}</td>
                  <td data-label="Ref"><code>{t.reference}</code></td>
                  {/* Debit includes the fee so Balance = previous balance − Debit + Credit. */}
                  <td data-label="Debit" className={`num debit ${t.direction === 'DEBIT' ? '' : 'empty'}`}>
                    {t.direction === 'DEBIT' && (
                      <span>
                        {formatMoney(t.amount + t.fee, wallet.currency)}
                        {t.fee > 0 && <small><br />incl. fee {formatMoney(t.fee, wallet.currency)}</small>}
                      </span>
                    )}
                  </td>
                  <td data-label="Credit" className={`num credit ${t.direction === 'CREDIT' ? '' : 'empty'}`}>
                    {t.direction === 'CREDIT' && formatMoney(t.amount, wallet.currency)}
                  </td>
                  <td data-label="Balance" className="num">{formatMoney(t.balanceAfter, wallet.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  )
}
