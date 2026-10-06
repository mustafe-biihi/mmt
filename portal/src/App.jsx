import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import Register from './pages/Register.jsx'
import Login from './pages/Login.jsx'
import Dashboard from './pages/Dashboard.jsx'
import Simulator from './pages/Simulator.jsx'

export default function App() {
  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">MMT Wallet</span>
        <nav>
          <NavLink to="/register">Register</NavLink>
          <NavLink to="/login">Login</NavLink>
          <NavLink to="/dashboard">My Wallet</NavLink>
          <NavLink to="/simulator">USSD *836#</NavLink>
        </nav>
      </header>
      <main>
        <Routes>
          <Route path="/" element={<Navigate to="/register" replace />} />
          <Route path="/register" element={<Register />} />
          <Route path="/login" element={<Login />} />
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/simulator" element={<Simulator />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
