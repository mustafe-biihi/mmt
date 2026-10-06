# MMT – Mobile Money Transfer

Customers register on a web portal, then use USSD (`*836#`) to move money between wallet accounts.

```
 Browser (portal, :3000) ─┐
                          ├──► API Gateway (Go, :8080) ──► MMT core (Go, :8081, internal only)
 USSD aggregator / sim ───┘     CORS · rate limit ·          login · sessions · USSD engine ·
                                request IDs · routing         transaction engine
                                                                 │            │
                                                            PostgreSQL      Redis
                                                            (customers,   (web sessions 15m,
                                                             wallets,      USSD sessions 30s)
                                                             ledger)
```

| Module | Path | Responsibility |
|---|---|---|
| API Gateway | [gateway/](gateway/) | Single entry point. Allow-lists `/api/*` and `POST /ussd`, injects the shared `X-Gateway-Key`, and handles rate limiting and CORS. Standard library only. |
| MMT core | [mmt/](mmt/) | Registration, PIN login with lockout, Redis sessions, USSD state machine, double-entry transaction engine. Rejects any request without the gateway key. |
| Web portal | [portal/](portal/) | React (Vite): register, login, wallet/history dashboard, and a USSD phone simulator. |

## Run it

```bash
docker compose up -d --build
```

- Portal: http://localhost:3000 (Register · My Wallet · **USSD *836#** simulator)
- Gateway: http://localhost:8080 (`/healthz`)

With `SEED_DEMO=true` (off by default; set it in `.env`), two funded demo customers are created: `252610000001` and `252610000002`, both with PIN `1234` and USD 100.

### Local development (without containers for the Go services)

```bash
docker compose up -d postgres redis
cd mmt && go run ./cmd/mmt          # :8081, SEED_DEMO=true to seed
cd gateway && go run .              # :8080
cd portal && npm install && npm run dev   # :5173, proxies /api and /ussd to the gateway
```

## USSD flow (`*836#`)

```
Dial *836#  → "Welcome to MMT, <name>  Enter your PIN:"   (3 wrong PINs locks the account)
            → MMT Services
              1. Send Money        → recipient phone → amount → PIN
              2. Cash Out          → agent code      → amount → PIN   (1% fee)
              3. Airtime Recharge  → my/other number → amount → PIN
              4. Merchant Payment  → merchant code   → amount → PIN
              5. Check Balance
              6. Mini Statement    (last 5)
              7. Change PIN        → current → new → confirm
              0. Exit
```

- The menu is loaded from the `services` table. To enable, disable or reorder services, or change fees and limits, edit that table; no deploy is needed.
- The session state lives in Redis under `mmt:ussd:<sessionId>` with a **30s idle TTL** (`USSD_SESSION_TTL`). Each step refreshes the TTL. After it expires, the customer sees "Session expired".
- Every money transaction (send, cash out, airtime, merchant) must be authorised by entering the PIN on the confirmation screen. Wrong PINs count toward the same 3-attempt lockout. `0` cancels.
- `0` on an input screen goes back to the main menu.
- **One active session per customer.** A second `*836#` dial is refused while a session is still open (lock key `mmt:ussd-active:<customerId>`, same 30s TTL). A second portal login returns `409 SESSION_ACTIVE` until the customer logs out or the 15-minute web session expires (`mmt:websession-active:<customerId>`). The two channels are independent.
- Sample agents: `2001`, `2002`. Sample merchants: `1001`, `1002`, `1003`.

**Aggregator contract.** `POST /ussd` accepts a form post (or JSON) with `sessionId`, `serviceCode`, `phoneNumber` and `text`, where `text` is all inputs so far joined by `*`. The reply is `text/plain` starting with `CON ` (continue) or `END ` (close). This matches Africa's Talking and similar aggregators.

## API (via gateway)

| Method | Path | Auth |
|---|---|---|
| POST | `/api/auth/register` `{fullName, msisdn, nationalId, pin}` | – |
| POST | `/api/auth/login` `{msisdn, pin}` → `{token}` | – |
| POST | `/api/auth/logout` | Bearer |
| GET | `/api/me` | Bearer |
| GET | `/api/transactions?limit=20` | Bearer |
| GET | `/api/services` | – |
| POST | `/api/admin/cash-in` `{msisdn, amount}` | `X-Admin-Key` |
| POST | `/ussd` | – (aggregator) |

To fund a wallet (deposit from the treasury):

```bash
curl -X POST localhost:8080/api/admin/cash-in -H "X-Admin-Key: dev-admin-key" \
  -H "Content-Type: application/json" -d '{"msisdn":"0615550001","amount":"50"}'
```

## Money handling

- Amounts are stored as actual values in `NUMERIC(18,2)` columns, so `1000` means **1,000.00**. The API returns real amounts (e.g. `"balance": 736.99`). Internally Go holds them as exact integers (`money.Amount`), so there is no floating-point rounding.
- Percentage fees are rounded half-up to the cent (1% of 250.50 = 2.51).
- Fund wallets with `POST /api/admin/cash-in`, not by editing `wallets.balance`. A direct edit has no ledger entry, so the balance no longer matches the ledger.
- Each posting runs in one DB transaction. It locks the involved wallet rows `FOR UPDATE` in id order (so concurrent postings cannot deadlock), checks the balance, writes a `transactions` row and balanced `ledger_entries`, and only then updates the balances.
- A DB constraint keeps non-system wallets from going negative.
- The treasury wallet goes negative by the amount of e-money issued, so the sum of all wallet balances is always 0.

## Configuration

See [.env.example](.env.example). **Change `GATEWAY_KEY` and `ADMIN_KEY` outside local development.** MMT settings: `USSD_SESSION_TTL` (30s), `WEB_SESSION_TTL` (15m), `MAX_PIN_ATTEMPTS` (3), `CURRENCY` (USD), `COUNTRY_CODE` (252).

## Tests

```bash
cd mmt && go test ./...
cd gateway && go test ./...
```
